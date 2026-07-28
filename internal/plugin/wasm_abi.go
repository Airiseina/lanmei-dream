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

// ============================================================
// ABI 常量
// ============================================================

const (
	ABIVersion       = "lanmei.plugin/v1"
	HostNamespace    = "lanmei:host/v1"
	ExportPluginInfo = "lanmei_plugin_info"
	ExportInit       = "lanmei_init"
	ExportHandle     = "lanmei_handle"
	ExportStart      = "lanmei_start"
	ExportStop       = "lanmei_stop"
)

// ============================================================
// 运行时限制
// ============================================================

// RuntimeLimits 定义 Wasm 插件的运行时资源限制。
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

// DefaultLimits 首版默认限制。
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

// ============================================================
// Guest Export DTO：lanmei_plugin_info
// ============================================================

// PluginInfoRequest 宿主调用 lanmei_plugin_info 时传入的 JSON。
type PluginInfoRequest struct {
	HostABIVersion string `json:"host_abi_version"`
}

// PluginInfoResponse Guest 返回的插件身份和声明。
type PluginInfoResponse struct {
	ABIVersion     string        `json:"abi_version"`
	ID             string        `json:"id"`
	Name           string        `json:"name"`
	Description    string        `json:"description"`
	Version        string        `json:"version"`
	Commands       []CommandDecl `json:"commands"`
	RequestedRoles []RoleRequest `json:"requested_roles"`
}

// CommandDecl 插件声明的斜杠命令。
type CommandDecl struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// RoleRequest 插件申请的角色。
type RoleRequest struct {
	Role     string `json:"role"`
	Required bool   `json:"required"`
	Reason   string `json:"reason"`
}

// Validate 校验 PluginInfoResponse。
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
	if len(r.Commands) == 0 {
		return fmt.Errorf("%w: 必须至少声明一个命令", ErrInvalidMetadata)
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
	return nil
}

// ============================================================
// Guest Export DTO：lanmei_init
// ============================================================

// InitRequest 宿主调用 lanmei_init 时传入的 JSON。
type InitRequest struct {
	ABIVersion       string            `json:"abi_version"`
	PluginID         string            `json:"plugin_id"`
	InstallationID   string            `json:"installation_id"`
	Config           map[string]string `json:"config"`
	GrantedRoles     []string          `json:"granted_roles"`
	EffectiveActions []string          `json:"effective_actions"`
}

// InitResponse Guest 返回的初始化结果。
type InitResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// ============================================================
// Guest Export DTO：lanmei_handle
// ============================================================

// EventType 事件类型枚举。
type EventType string

const (
	EventTypeCommand EventType = "command"
)

// HandleRequest 宿主调用 lanmei_handle 时传入的 JSON。
type HandleRequest struct {
	ABIVersion string      `json:"abi_version"`
	EventID    string      `json:"event_id"`
	EventType  EventType   `json:"event_type"`
	Timestamp  string      `json:"timestamp"`
	Message    MessageInfo `json:"message"`
	Command    CommandInfo `json:"command"`
}

// MessageInfo 当前消息的上下文信息。
type MessageInfo struct {
	MessageID string     `json:"message_id,omitempty"`
	Text      string     `json:"text"`
	Raw       string     `json:"raw"`
	User      UserInfo   `json:"user"`
	Group     *GroupInfo `json:"group"`
	IsGroup   bool       `json:"is_group"`
}

// UserInfo 用户信息。
type UserInfo struct {
	ID       string `json:"id"`
	Nickname string `json:"nickname,omitempty"`
}

// GroupInfo 群组信息。
type GroupInfo struct {
	ID string `json:"id"`
}

// CommandInfo 宿主解析后的命令信息。
type CommandInfo struct {
	Name       string   `json:"name"`
	Args       []string `json:"args"`
	RawArgs    string   `json:"raw_args"`
	RawMessage string   `json:"raw_message"`
}

// HandleResponse Guest 返回的处理结果。
type HandleResponse struct {
	Handled bool         `json:"handled"`
	Outputs []OutputItem `json:"outputs"`
}

// OutputItem 单条输出。
type OutputItem struct {
	Type    string `json:"type"`
	Content string `json:"content"`
}

// Validate 校验 HandleResponse。
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

// ============================================================
// Guest Export DTO：lanmei_start / lanmei_stop
// ============================================================

// StartRequest 宿主调用 lanmei_start 时传入的 JSON。
type StartRequest struct {
	StartedAt string `json:"started_at"`
}

// StopRequest 宿主调用 lanmei_stop 时传入的 JSON。
type StopRequest struct {
	Reason string `json:"reason"`
}

// StopReason 卸载原因枚举。
type StopReason string

const (
	StopReasonShutdown     StopReason = "shutdown"
	StopReasonUnload       StopReason = "unload"
	StopReasonUpgrade      StopReason = "upgrade"
	StopReasonInitRollback StopReason = "init_rollback"
)

// GenericOKResponse 用于 start/stop/init 的通用成功响应。
type GenericOKResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// ============================================================
// Host Function DTO
// ============================================================

// HostResponse 统一 Host Function 响应信封。
type HostResponse struct {
	OK    bool        `json:"ok"`
	Data  interface{} `json:"data,omitempty"`
	Error *HostError  `json:"error,omitempty"`
}

// HostError Host Function 错误。
type HostError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Host 错误码字符串常量。
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

// HostCodeFrom 将 Host 哨兵错误映射到 JSON code 字符串。
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

// StateGetRequest state_get 请求。
type StateGetRequest struct {
	Key string `json:"key"`
}

// StateGetData state_get 成功数据。
type StateGetData struct {
	Found bool   `json:"found"`
	Value string `json:"value"`
}

// StateSetRequest state_set 请求。
type StateSetRequest struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	TTLMs int64  `json:"ttl_ms"`
}

// StateDeleteRequest state_delete 请求。
type StateDeleteRequest struct {
	Key string `json:"key"`
}

// HostOK 构造成功响应。
func HostOK(data interface{}) HostResponse {
	return HostResponse{OK: true, Data: data}
}

// HostErr 构造错误响应。
func HostErr(code, message string) HostResponse {
	return HostResponse{
		OK:    false,
		Error: &HostError{Code: code, Message: message},
	}
}

// ============================================================
// JSON 解码辅助
// ============================================================

// UnmarshalGuestInput 解码 Guest 输入 JSON，检查长度限制。
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

// MarshalHostResponse 编码 Host 响应。
func MarshalHostResponse(resp HostResponse) ([]byte, error) {
	return json.Marshal(resp)
}

// ============================================================
// 校验器
// ============================================================

var pluginIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// ValidatePluginID 校验插件 ID 格式。
func ValidatePluginID(id string) error {
	if !pluginIDPattern.MatchString(id) {
		return fmt.Errorf("plugin_id 格式无效: %q（必须匹配 %s）", id, pluginIDPattern)
	}
	return nil
}

// ValidateStateKey 校验 state key。
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

// ValidateStateValue 校验 state value。
func ValidateStateValue(value string, limits *RuntimeLimits) error {
	if len(value) > limits.MaxStateValueLen {
		return fmt.Errorf("%w: value 长度 %d 超限（最大 %d）", ErrHostValueTooLarge, len(value), limits.MaxStateValueLen)
	}
	return nil
}

// ValidateTTL 校验 TTL。
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

// ValidateCommandDecl 校验一个命令声明，并检查与已注册命令的冲突。
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

// ============================================================
// 插件主体生成
// ============================================================

// PluginPrincipal 构造 Casbin 插件主体。
func PluginPrincipal(pluginID, installationID string) string {
	return "plugin::" + pluginID + "::" + installationID
}

// UserPrincipal 构造 Casbin 用户主体。
// 格式：user::<platform>::<platformUserID>，如 user::qq::123456 或 user::wechat::wxid_xxx
func UserPrincipal(platform, platformUserID string) string {
	return "user::" + platform + "::" + platformUserID
}

// SystemPrincipal 构造 Casbin 系统主体。
func SystemPrincipal(name string) string {
	return "system::" + name
}

// ============================================================
// GrantedRoles/EffectiveActions 排序辅助
// ============================================================

// SortedStrings 返回排序后的副本。
func SortedStrings(ss []string) []string {
	sorted := make([]string, len(ss))
	copy(sorted, ss)
	sort.Strings(sorted)
	return sorted
}

// ============================================================
// ABI 错误包装
// ============================================================

var (
	ErrABIIncompatible    = errors.New("ABI 版本不兼容")
	ErrMissingExport      = errors.New("缺少必需导出")
	ErrInvalidMetadata    = errors.New("插件元数据无效")
	ErrPluginConflict     = errors.New("插件 ID 或命令冲突")
	ErrPluginNotInstalled = errors.New("插件未安装")
	ErrPluginNotLoaded    = errors.New("插件未加载")
	ErrPermissionDenied   = errors.New("权限不足")
	ErrCallTimeout        = errors.New("调用超时")
	ErrGuestFailed        = errors.New("Guest 执行失败")
	ErrOutputInvalid      = errors.New("输出无效")
	ErrStateLimitExceeded = errors.New("状态限制超出")
)

// ============================================================
// Host Function 动作映射
// ============================================================

// HostFunctionActions 映射 Host Function 名称到 Casbin 动作。
var HostFunctionActions = map[string]string{
	"state_get":    "state.read",
	"state_set":    "state.write",
	"state_delete": "state.delete",
}
