package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// ABI 常量

const (
	// ABIVersion ABI 版本标识（lanmei.plugin/v1）：Guest 的 plugin_info 返回值与宿主发送的
	// init/handle 请求都必须携带该值，不匹配即拒绝加载。
	ABIVersion = "lanmei.plugin/v1"

	// HostNamespace Host Function 的 Wasm 导入命名空间（lanmei:host/v1）；
	// 插件通过它导入状态存储等宿主能力，对应 schema/plugin/lanmei-plugin.wit 中 world 的 import host。
	HostNamespace = "lanmei:host/v1"

	// ExportPluginInfo 必需 Guest 导出：返回插件元数据，对应 WIT plugin interface 的 plugin-info。
	ExportPluginInfo = "lanmei_plugin_info"

	// ExportInit 必需 Guest 导出：一次性初始化，对应 WIT plugin interface 的 init。
	ExportInit = "lanmei_init"

	// ExportHandle 必需 Guest 导出：命令与工具调用事件入口，对应 WIT plugin interface 的 handle。
	ExportHandle = "lanmei_handle"

	// ExportStart 可选 Guest 导出：启动通知，对应 WIT plugin interface 的 start；不存在时宿主跳过。
	ExportStart = "lanmei_start"

	// ExportStop 可选 Guest 导出：停止通知，对应 WIT plugin interface 的 stop；不存在时宿主跳过。
	ExportStop = "lanmei_stop"
)

// 运行时限制

// RuntimeLimits 定义 Wasm 插件的运行时资源限制，覆盖调用超时、内存、输入输出与状态存储上限。
// 字段零值不会被自动替换为默认值，未显式赋值的字段应取自 DefaultLimits。
type RuntimeLimits struct {
	CallTimeoutSec     int           // 单次导出调用超时（秒），默认 3
	MaxMemoryPages     int           // Wasm 最大内存页数，默认 256（16 MiB）
	MaxGuestInputJSON  int           // Guest 输入 JSON 最大字节数，默认 256 KiB
	MaxGuestOutputJSON int           // Guest 输出 JSON 最大字节数，默认 64 KiB
	MaxOutputCount     int           // 单次文本输出最大条数，默认 8
	MaxTextLen         int           // 单条文本最大 UTF-8 字节数，默认 4096
	MaxStateKeyLen     int           // State key 最大 UTF-8 字节数，默认 256
	MaxStateValueLen   int           // State value 最大字节数，默认 64 KiB
	MaxStateTTL        time.Duration // State 最大 TTL，默认 30 天
	MaxExtismVars      int           // Extism vars 总字节数，默认 1 MiB
	MaxWasmFileSize    int64         // Wasm 文件最大字节数，默认 16 MiB
}

// DefaultLimits 首版默认运行时限制，是 NewRuntime 与 NewWasmManager 未显式提供限制时的兜底取值。
var DefaultLimits = RuntimeLimits{
	CallTimeoutSec:     3,
	MaxMemoryPages:     256,
	MaxGuestInputJSON:  256 * 1024,
	MaxGuestOutputJSON: 64 * 1024,
	MaxOutputCount:     8,
	MaxTextLen:         4096,
	MaxStateKeyLen:     256,
	MaxStateValueLen:   64 * 1024,
	MaxStateTTL:        30 * 24 * time.Hour,
	MaxExtismVars:      1 * 1024 * 1024,
	MaxWasmFileSize:    16 * 1024 * 1024,
}

// Guest Export DTO：lanmei_plugin_info

// PluginInfoRequest 宿主调用 lanmei_plugin_info 时传入的 JSON，
// 对应 schema/plugin/lanmei-plugin.wit 中 plugin-info 的 host-abi-version 参数。
type PluginInfoRequest struct {
	HostABIVersion string `json:"host_abi_version"`
}

// PluginInfoResponse Guest 对 lanmei_plugin_info 的响应，承载插件身份、命令/工具声明与角色申请，
// 对应 WIT 的 plugin-info-response；该导出可能被多次调用（安装检查、加载），实现应无副作用。
type PluginInfoResponse struct {
	ABIVersion     string        `json:"abi_version"`
	ID             string        `json:"id"`
	Name           string        `json:"name"`
	Description    string        `json:"description"`
	Version        string        `json:"version"`
	Commands       []CommandDecl `json:"commands"`
	RequestedRoles []RoleRequest `json:"requested_roles"`
	Tools          []ToolDecl    `json:"tools,omitempty"`
}

// CommandDecl 插件声明的斜杠命令，对应 WIT command-decl；
// Name 不含 / 前缀，且不得含空白与控制字符。
type CommandDecl struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// ToolDecl 插件声明的 AI 工具，对应 WIT tool-decl；宿主据此把工具注册到工具表供 LLM 调用。
type ToolDecl struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// RoleRequest 插件申请的内置角色；Required=true 时宿主在加载阶段校验该角色已授予，否则拒绝加载。
type RoleRequest struct {
	Role     string `json:"role"`
	Required bool   `json:"required"`
	Reason   string `json:"reason"`
}

// Validate 校验插件元数据：ABI 版本、plugin_id 格式与长度、名称/版本长度、
// 至少声明一个命令或工具，以及命令名与工具名的字符集和唯一性。
//
// 返回：ABI 版本不匹配返回包装 ErrABIIncompatible 的错误，其余不合法返回包装 ErrInvalidMetadata 的错误。
func (r *PluginInfoResponse) Validate() error {
	if r.ABIVersion != ABIVersion {
		return fmt.Errorf("%w: 期望 %q，实际 %q", ErrABIIncompatible, ABIVersion, r.ABIVersion)
	}
	if !pluginIDPattern.MatchString(r.ID) {
		return fmt.Errorf("%w: plugin_id 格式无效: %q（必须匹配 %s）", ErrInvalidMetadata, r.ID, pluginIDPattern)
	}
	if len(r.ID) > 64 {
		return fmt.Errorf("%w: plugin_id 超长（%d > 64）", ErrInvalidMetadata, len(r.ID))
	}
	if len(r.Name) == 0 || len(r.Name) > 255 {
		return fmt.Errorf("%w: name 长度超限", ErrInvalidMetadata)
	}
	if len(r.Version) == 0 || len(r.Version) > 64 {
		return fmt.Errorf("%w: version 长度超限", ErrInvalidMetadata)
	}
	if len(r.Commands) == 0 && len(r.Tools) == 0 {
		return fmt.Errorf("%w: 必须至少声明一个命令或工具", ErrInvalidMetadata)
	}

	cmdNames := make(map[string]bool, len(r.Commands))
	for _, cmd := range r.Commands {
		name := strings.TrimSpace(cmd.Name)
		if name == "" {
			return fmt.Errorf("%w: 命令名不能为空", ErrInvalidMetadata)
		}
		for _, ch := range name {
			if ch == '/' || unicode.IsControl(ch) || unicode.IsSpace(ch) {
				return fmt.Errorf("%w: 命令名 %q 含非法字符", ErrInvalidMetadata, name)
			}
		}
		if cmdNames[name] {
			return fmt.Errorf("%w: 命令名 %q 重复", ErrInvalidMetadata, name)
		}
		cmdNames[name] = true
	}

	toolNames := make(map[string]bool, len(r.Tools))
	for _, td := range r.Tools {
		name := strings.TrimSpace(td.Name)
		if name == "" {
			return fmt.Errorf("%w: 工具名不能为空", ErrInvalidMetadata)
		}
		for _, ch := range name {
			if unicode.IsControl(ch) || unicode.IsSpace(ch) {
				return fmt.Errorf("%w: 工具名 %q 含非法字符", ErrInvalidMetadata, name)
			}
		}
		if toolNames[name] {
			return fmt.Errorf("%w: 工具名 %q 重复", ErrInvalidMetadata, name)
		}
		toolNames[name] = true
	}
	return nil
}

// Guest Export DTO：lanmei_init

// InitRequest 宿主调用 lanmei_init 时传入的 JSON，对应 WIT init-request：
// 告知安装实例身份、配置与宿主最终授予的角色/动作；GrantedRoles 与 EffectiveActions 由宿主从策略读取，Guest 不可伪造。
type InitRequest struct {
	ABIVersion       string            `json:"abi_version"`
	PluginID         string            `json:"plugin_id"`
	InstallationID   string            `json:"installation_id"`
	Config           map[string]string `json:"config"`
	GrantedRoles     []string          `json:"granted_roles"`
	EffectiveActions []string          `json:"effective_actions"`
}

// InitResponse Guest 对 lanmei_init 的返回：OK=false 时 Error 说明失败原因，宿主将放弃加载并关闭实例。
type InitResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// Guest Export DTO：lanmei_handle

// EventType 事件类型枚举，对应 WIT 的 event-type；JSON 取值见下方常量。
type EventType string

const (
	// EventTypeCommand 命令事件：宿主按 /<命令名> 前缀匹配后路由给插件。
	EventTypeCommand EventType = "command"

	// EventTypeToolCall AI 工具调用事件：LLM 选择插件注册的工具时触发。
	EventTypeToolCall EventType = "tool_call"
)

// HandleRequest 宿主调用 lanmei_handle 时传入的 JSON，对应 WIT handle-request；
// command 事件填充 Command，tool_call 事件填充 ToolCall。
type HandleRequest struct {
	ABIVersion string        `json:"abi_version"`
	EventID    string        `json:"event_id"`
	EventType  EventType     `json:"event_type"`
	Timestamp  string        `json:"timestamp"`
	Message    MessageInfo   `json:"message"`
	Command    CommandInfo   `json:"command"`
	ToolCall   *ToolCallInfo `json:"tool_call,omitempty"`
}

// MessageInfo 事件的消息上下文（WIT message-info）：身份与群组信息由宿主填充，私聊时 Group 为 nil。
type MessageInfo struct {
	MessageID string     `json:"message_id,omitempty"`
	Text      string     `json:"text"`
	Raw       string     `json:"raw"`
	User      UserInfo   `json:"user"`
	Group     *GroupInfo `json:"group"`
	IsGroup   bool       `json:"is_group"`
}

// UserInfo 用户信息（WIT user-info）：ID 为平台侧用户标识。
type UserInfo struct {
	ID       string `json:"id"`
	Nickname string `json:"nickname,omitempty"`
}

// GroupInfo 群组信息（WIT group-info），仅群聊事件非空。
type GroupInfo struct {
	ID string `json:"id"`
}

// CommandInfo 宿主解析后的命令信息（WIT command-info）：Name 不含 / 前缀，Args 为按空白切分的参数。
type CommandInfo struct {
	Name       string   `json:"name"`
	Args       []string `json:"args"`
	RawArgs    string   `json:"raw_args"`
	RawMessage string   `json:"raw_message"`
}

// ToolCallInfo 工具调用信息（WIT tool-call-info）：Arguments 为 JSON 编码的原始参数串。
type ToolCallInfo struct {
	ToolName  string `json:"tool_name"`
	Arguments string `json:"arguments"` // JSON 编码的参数
	CallID    string `json:"call_id"`
}

// HandleResponse Guest 对 lanmei_handle 的响应（WIT handle-response）：Handled=false 时 Outputs 必须为空。
type HandleResponse struct {
	Handled bool         `json:"handled"`
	Outputs []OutputItem `json:"outputs"`
}

// OutputItem 单条文本输出（WIT output-item）：Type 目前仅支持 "text"。
type OutputItem struct {
	Type    string `json:"type"`
	Content string `json:"content"`
}

// Validate 校验输出是否满足 ABI 约束：handled=false 不得携带输出，输出条数、类型、UTF-8 合法性与单条长度均须在限制内。
//
// 参数：
//   - limits：运行时限制，调用方须保证非 nil（宿主侧由 Runtime 传入配置值或 DefaultLimits）
//
// 返回：违反任一约束时返回包装 ErrOutputInvalid 的错误。
func (r *HandleResponse) Validate(limits *RuntimeLimits) error {
	if !r.Handled && len(r.Outputs) > 0 {
		return fmt.Errorf("%w: handled=false 时 outputs 必须为空", ErrOutputInvalid)
	}
	if len(r.Outputs) > limits.MaxOutputCount {
		return fmt.Errorf("%w: 输出条数 %d 超限（最大 %d）", ErrOutputInvalid, len(r.Outputs), limits.MaxOutputCount)
	}
	for i, o := range r.Outputs {
		if o.Type != "text" {
			return fmt.Errorf("%w: 不支持的输出类型 %q（仅支持 text）", ErrOutputInvalid, o.Type)
		}
		if !utf8.ValidString(o.Content) {
			return fmt.Errorf("%w: 输出 #%d 非 UTF-8", ErrOutputInvalid, i)
		}
		if len(o.Content) > limits.MaxTextLen {
			return fmt.Errorf("%w: 输出 #%d 长度 %d 超限（最大 %d）", ErrOutputInvalid, i, len(o.Content), limits.MaxTextLen)
		}
	}
	return nil
}

// Guest Export DTO：lanmei_start / lanmei_stop

// StartRequest 宿主调用 lanmei_start 时传入的 JSON（WIT start 的 started-at 参数），StartedAt 为 RFC 3339 UTC 时间。
type StartRequest struct {
	StartedAt string `json:"started_at"`
}

// StopRequest 宿主调用 lanmei_stop 时传入的 JSON（WIT stop 的 reason 参数），Reason 取值见 StopReason。
type StopRequest struct {
	Reason string `json:"reason"`
}

// StopReason 停止原因枚举，宿主调用 lanmei_stop 时通过 reason 传入，Guest 可据此区分卸载场景。
type StopReason string

const (
	// StopReasonShutdown 宿主整体退出。
	StopReasonShutdown StopReason = "shutdown"

	// StopReasonUnload 管理员卸载插件。
	StopReasonUnload StopReason = "unload"

	// StopReasonUpgrade 升级替换前停止旧实例。
	StopReasonUpgrade StopReason = "upgrade"

	// StopReasonInitRollback 初始化失败后的回滚停止。
	StopReasonInitRollback StopReason = "init_rollback"
)

// GenericOKResponse init/start/stop 共用的通用响应（WIT generic-ok-response）：OK=false 时 Error 必填。
type GenericOKResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// Host Function DTO

// HostResponse 统一 Host Function 响应信封（对应 WIT host-response）：
// 成功时 OK=true 且 Data 为对应操作的数据结构，失败时 Error 携带错误码。
type HostResponse struct {
	OK    bool        `json:"ok"`
	Data  interface{} `json:"data,omitempty"`
	Error *HostError  `json:"error,omitempty"`
}

// HostError Host Function 错误（WIT host-error）：Code 为稳定错误码（见 ErrCode* 常量），
// Message 仅供排障，Guest 应按 Code 分支处理。
type HostError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Host 错误码字符串常量，是 Host Function 错误信封 error.code 的取值（对应 WIT host-error.code）。
const (
	ErrCodeInvalidRequest   = "invalid_request"
	ErrCodePermissionDenied = "permission_denied"
	ErrCodeKeyTooLarge      = "key_too_large"
	ErrCodeValueTooLarge    = "value_too_large"
	ErrCodeTTLOutOfRange    = "ttl_out_of_range"
	ErrCodeStateUnavailable = "state_unavailable"
	ErrCodeInternalError    = "internal_error"
)

// Host 错误码 sentinel errors（用于校验函数返回，在 Host Function 层转换为 JSON code）。
var (
	ErrHostInvalidRequest   = errors.New(ErrCodeInvalidRequest)
	ErrHostKeyTooLarge      = errors.New(ErrCodeKeyTooLarge)
	ErrHostValueTooLarge    = errors.New(ErrCodeValueTooLarge)
	ErrHostTTLOutOfRange    = errors.New(ErrCodeTTLOutOfRange)
	ErrHostStateUnavailable = errors.New(ErrCodeStateUnavailable)
	ErrHostInternalError    = errors.New(ErrCodeInternalError)
)

// HostCodeFrom 将 Host 哨兵错误映射到 JSON 错误码字符串；无法识别的错误统一归为 internal_error。
//
// 参数：
//   - err：校验或鉴权返回的错误，可为包装后的哨兵错误
//
// 返回：ErrCode* 常量之一。
func HostCodeFrom(err error) string {
	switch {
	case errors.Is(err, ErrHostInvalidRequest):
		return ErrCodeInvalidRequest
	case errors.Is(err, ErrHostKeyTooLarge):
		return ErrCodeKeyTooLarge
	case errors.Is(err, ErrHostValueTooLarge):
		return ErrCodeValueTooLarge
	case errors.Is(err, ErrHostTTLOutOfRange):
		return ErrCodeTTLOutOfRange
	case errors.Is(err, ErrHostStateUnavailable):
		return ErrCodeStateUnavailable
	case errors.Is(err, ErrPermissionDenied):
		return ErrCodePermissionDenied
	default:
		return ErrCodeInternalError
	}
}

// StateGetRequest state_get 请求（WIT state-get-request）：Key 为 Guest 逻辑 key，物理 key 由宿主按安装实例加前缀。
type StateGetRequest struct {
	Key string `json:"key"`
}

// StateGetData state_get 成功数据：Found=false 表示键不存在，此时 Value 为空串。
type StateGetData struct {
	Found bool   `json:"found"`
	Value string `json:"value"`
}

// StateSetRequest state_set 请求（WIT state-set-request）：TTLMs=0 表示永不过期，正数不得超过 RuntimeLimits.MaxStateTTL。
type StateSetRequest struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	TTLMs int64  `json:"ttl_ms"`
}

// StateDeleteRequest state_delete 请求（WIT state-delete-request）。
type StateDeleteRequest struct {
	Key string `json:"key"`
}

// CompareAndSwapRequest compare_and_swap 请求（WIT compare-and-swap-request）：仅当当前值等于 OldValue 时写入 NewValue。
type CompareAndSwapRequest struct {
	Key      string `json:"key"`
	OldValue string `json:"old_value"`
	NewValue string `json:"new_value"`
	TTLMs    int64  `json:"ttl_ms"`
}

// CompareAndSwapData compare_and_swap 成功数据：Swapped 表示本次是否发生交换。
type CompareAndSwapData struct {
	Swapped bool `json:"swapped"`
}

// IncrByRequest incr_by 请求（WIT incr-by-request）：Delta 为增量。
type IncrByRequest struct {
	Key   string `json:"key"`
	Delta int64  `json:"delta"`
}

// IncrByData incr_by 成功数据：Value 为自增后的值。
type IncrByData struct {
	Value int64 `json:"value"`
}

// SetIfNotExistsRequest set_if_not_exists 请求（WIT set-if-not-exists-request）：键不存在时写入 Value。
type SetIfNotExistsRequest struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	TTLMs int64  `json:"ttl_ms"`
}

// SetIfNotExistsData set_if_not_exists 成功数据：Set 表示本次是否写入。
type SetIfNotExistsData struct {
	Set bool `json:"set"`
}

// HostOK 构造成功的 Host Function 响应。
//
// 参数：
//   - data：成功数据，须与对应 Host Function 的 Data 结构一致；无数据时传 struct{}{}，传 nil 则响应省略 data 字段
//
// 返回：OK=true 的 HostResponse。
func HostOK(data interface{}) HostResponse {
	return HostResponse{OK: true, Data: data}
}

// HostErr 构造失败的 Host Function 响应。
//
// 参数：
//   - code：错误码，须为 ErrCode* 常量之一
//   - message：面向插件作者的可读说明，不得包含策略表、数据库错误、物理 key 或调用栈等内部细节
//
// 返回：OK=false 且 Error 非空的 HostResponse。
func HostErr(code, message string) HostResponse {
	return HostResponse{
		OK:    false,
		Error: &HostError{Code: code, Message: message},
	}
}

// JSON 解码辅助

// UnmarshalGuestInput 解码 ABI JSON 载荷并检查长度不超过 limits.MaxGuestInputJSON；
// Runtime 用它解码 Guest 导出函数的返回值。首版允许未知字段，便于同一 ABI 版本内向后兼容。
//
// 参数：
//   - data：待解码字节
//   - v：解码目标
//   - limits：运行时限制，调用方须保证非 nil
//
// 返回：超长或 JSON 非法时返回错误。
func UnmarshalGuestInput(data []byte, v interface{}, limits *RuntimeLimits) error {
	if len(data) > limits.MaxGuestInputJSON {
		return fmt.Errorf("输入 JSON 长度 %d 超限（最大 %d）", len(data), limits.MaxGuestInputJSON)
	}
	// 首版允许未知字段（便于 v1 内向后兼容）
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("JSON 解码失败: %w", err)
	}
	return nil
}

// MarshalHostResponse 将 Host Function 响应编码为 JSON。
//
// 参数：
//   - resp：待编码的响应
//
// 返回：JSON 字节；编码失败时返回错误（常规数据结构不会失败）。
func MarshalHostResponse(resp HostResponse) ([]byte, error) {
	return json.Marshal(resp)
}

// 校验器

var pluginIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// ValidatePluginID 校验插件 ID 是否符合 ^[a-z][a-z0-9_]{0,63}$。
//
// 参数：
//   - id：插件 ID
//
// 返回：格式非法时返回错误。
func ValidatePluginID(id string) error {
	if !pluginIDPattern.MatchString(id) {
		return fmt.Errorf("plugin_id 格式无效: %q（必须匹配 %s）", id, pluginIDPattern)
	}
	return nil
}

// ValidateStateKey 校验 Guest 传入的 state key：非空、长度不超过 MaxStateKeyLen、
// 不含控制字符与 "/"、".." 路径字符。
//
// 参数：
//   - key：Guest 逻辑 key（物理 key 由宿主按安装实例加前缀生成）
//   - limits：运行时限制，调用方须保证非 nil
//
// 返回：非法时返回包装 ErrHostInvalidRequest 或 ErrHostKeyTooLarge 的错误。
func ValidateStateKey(key string, limits *RuntimeLimits) error {
	if key == "" {
		return fmt.Errorf("%w: key 不能为空", ErrHostInvalidRequest)
	}
	if len(key) > limits.MaxStateKeyLen {
		return fmt.Errorf("%w: key 长度 %d 超限（最大 %d）", ErrHostKeyTooLarge, len(key), limits.MaxStateKeyLen)
	}
	for _, r := range key {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: key 含控制字符", ErrHostInvalidRequest)
		}
	}
	if strings.Contains(key, "/") || strings.Contains(key, "..") {
		return fmt.Errorf("%w: key 含非法路径字符", ErrHostInvalidRequest)
	}
	return nil
}

// ValidateStateValue 校验 state value 的字节长度不超过 limits.MaxStateValueLen。
//
// 参数：
//   - value：待写入的状态值
//   - limits：运行时限制，调用方须保证非 nil
//
// 返回：超长时返回包装 ErrHostValueTooLarge 的错误。
func ValidateStateValue(value string, limits *RuntimeLimits) error {
	if len(value) > limits.MaxStateValueLen {
		return fmt.Errorf("%w: value 长度 %d 超限（最大 %d）", ErrHostValueTooLarge, len(value), limits.MaxStateValueLen)
	}
	return nil
}

// ValidateTTL 校验并换算 Guest 传入的 TTL：0 表示永不过期，正数不得超过 limits.MaxStateTTL，负数非法。
//
// 参数：
//   - ttlMs：Guest 传入的毫秒数
//   - limits：运行时限制，调用方须保证非 nil
//
// 返回：换算后的 time.Duration（ttlMs=0 时为 0）；负数或超限时返回包装 ErrHostTTLOutOfRange 的错误。
func ValidateTTL(ttlMs int64, limits *RuntimeLimits) (time.Duration, error) {
	if ttlMs < 0 {
		return 0, fmt.Errorf("%w: ttl_ms 不能为负", ErrHostTTLOutOfRange)
	}
	if ttlMs == 0 {
		return 0, nil
	}
	d := time.Duration(ttlMs) * time.Millisecond
	if d > limits.MaxStateTTL {
		return 0, fmt.Errorf("%w: TTL %v 超限（最大 %v）", ErrHostTTLOutOfRange, d, limits.MaxStateTTL)
	}
	return d, nil
}

// ValidateCommandDecl 校验单个命令声明：名称非空、不含 "/"、空白与控制字符，且不与已注册命令冲突。
//
// 参数：
//   - cmd：待校验的命令声明
//   - reservedNames：已占用的命令名集合（宿主内置命令与已注册插件的命令）
//
// 返回：非法或冲突时返回错误，冲突时返回包装 ErrPluginConflict 的错误。
func ValidateCommandDecl(cmd CommandDecl, reservedNames map[string]bool) error {
	name := strings.TrimSpace(cmd.Name)
	if name == "" {
		return fmt.Errorf("命令名不能为空")
	}
	if reservedNames[name] {
		return fmt.Errorf("%w: 命令 %q 与已注册命令冲突", ErrPluginConflict, name)
	}
	for _, r := range name {
		if r == '/' || unicode.IsControl(r) || unicode.IsSpace(r) {
			return fmt.Errorf("命令名 %q 含非法字符", name)
		}
	}
	return nil
}

// 插件主体生成

// PluginPrincipal 构造插件安装实例的 Casbin 主体，格式 plugin::<pluginID>::<installationID>。
// 授权精确到安装实例，必须由宿主用可信的安装记录构造，不能采信 Wasm 请求中的 plugin_id/installation_id。
//
// 参数：
//   - pluginID：插件 ID
//   - installationID：安装实例 ID（同一插件可安装多次，各自独立授权）
//
// 返回：主体字符串。
func PluginPrincipal(pluginID, installationID string) string {
	return "plugin::" + pluginID + "::" + installationID
}

// UserPrincipal 构造用户 Casbin 主体，格式 user::<platform>::<platformUserID>。
//
// 参数：
//   - platform：平台标识，如 qq、wechat
//   - platformUserID：平台侧用户 ID
//
// 返回：主体字符串，如 user::qq::123456。
func UserPrincipal(platform, platformUserID string) string {
	return "user::" + platform + "::" + platformUserID
}

// SystemPrincipal 构造宿主系统主体，格式 system::<name>。
//
// 参数：
//   - name：系统主体名，如 startup、manager
//
// 返回：主体字符串。
func SystemPrincipal(name string) string {
	return "system::" + name
}

// GrantedRoles/EffectiveActions 排序辅助

// SortedStrings 返回按字典序升序排序的副本，不修改入参。
//
// 参数：
//   - ss：待排序的字符串切片
//
// 返回：新的已排序切片；入参为 nil 时返回空切片（非 nil）。
func SortedStrings(ss []string) []string {
	sorted := make([]string, len(ss))
	copy(sorted, ss)
	sort.Strings(sorted)
	return sorted
}

// ABI 错误包装

// ABI 错误哨兵，可通过 errors.Is 判定失败类别。
var (
	// ErrABIIncompatible 插件声明的 ABI 版本与宿主不一致。
	ErrABIIncompatible = errors.New("ABI 版本不兼容")

	// ErrMissingExport Wasm 缺少 lanmei_plugin_info、lanmei_init、lanmei_handle 等必需导出。
	ErrMissingExport = errors.New("缺少必需导出")

	// ErrInvalidMetadata 插件元数据无效，如 plugin_id 格式、名称/版本长度、命令或工具声明不符合 ABI 约束。
	ErrInvalidMetadata = errors.New("插件元数据无效")

	// ErrPluginConflict 插件 ID 已被安装，或命令名与已注册命令冲突。
	ErrPluginConflict = errors.New("插件 ID 或命令冲突")

	// ErrPluginNotInstalled 找不到对应的安装记录。
	ErrPluginNotInstalled = errors.New("插件未安装")

	// ErrPluginNotLoaded 插件仍处于加载状态，不允许执行当前操作（如删除前必须先卸载）。
	ErrPluginNotLoaded = errors.New("插件未加载")

	// ErrPermissionDenied Casbin 鉴权拒绝，表示主体未持有指定动作。
	ErrPermissionDenied = errors.New("权限不足")

	// ErrCallTimeout Guest 导出调用超过 RuntimeLimits.CallTimeoutSec。
	ErrCallTimeout = errors.New("调用超时")

	// ErrGuestFailed Guest 执行失败：trap、非零 exit code 或返回 ok=false。
	ErrGuestFailed = errors.New("Guest 执行失败")

	// ErrOutputInvalid Guest 输出不符合 ABI 约束，如 handled=false 却带输出、输出类型非 text、超长等。
	ErrOutputInvalid = errors.New("输出无效")

	// ErrStateLimitExceeded 状态存储使用量超出配额限制。
	ErrStateLimitExceeded = errors.New("状态限制超出")
)

// Host Function 动作映射

// HostFunctionActions 映射 Host Function 名称到 Casbin 动作（key 为 ABI 中的函数名，value 为动作标识）。
var HostFunctionActions = map[string]string{
	"state_get":         "state.read",
	"state_set":         "state.write",
	"state_delete":      "state.delete",
	"compare_and_swap":  "state.write",
	"incr_by":           "state.write",
	"set_if_not_exists": "state.write",
}
