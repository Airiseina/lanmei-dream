// Package main 是蓝妹 Wasm 插件的 Go PDK 参考实现：每日签到命令插件，演示 ABI 闭环 ——
// lanmei_plugin_info 声明命令和角色，lanmei_init 验证配置，lanmei_handle 通过
// state_get/state_set 读写签到状态并返回文本回复。
//
// ABI 约定：宿主与 Guest 之间只传 UTF-8 JSON —— 导出函数用 pdk.Input() 读请求、pdk.Output() 写响应，
// 返回码 0 表示成功，非 0 返回码或 trap 会被宿主视为调用失败；Host Function 无法直接传字符串，
// 只能通过 Extism 内存偏移（PTR i64）传参，详见 stateGet、stateSet 与 callStateGet、callStateSet。
//
// 构建（需要 TinyGo 0.30+ 和 extism/go-pdk）：
//
//	tinygo build -o signin.wasm -target wasi main.go
//
// 构建后将 signin.wasm 通过 `/插件 安装 <HTTPS 直链>` 安装（当前没有本地投放目录通道）。
package main

import (
	"encoding/json"
	"time"

	"github.com/extism/go-pdk"
)

// PluginInfoResponse 匹配 ABI lanmei.plugin/v1。
type PluginInfoResponse struct {
	ABIVersion     string        `json:"abi_version"`
	ID             string        `json:"id"`
	Name           string        `json:"name"`
	Description    string        `json:"description"`
	Version        string        `json:"version"`
	Commands       []CommandDecl `json:"commands"`
	RequestedRoles []RoleRequest `json:"requested_roles"`
}

// CommandDecl 插件声明的斜杠命令，对应 WIT 的 command-decl。
//
// 字段：
//   - Name：命令名，不含 / 前缀，不得包含空白、控制字符或 /，同一插件内不得重复
//   - Description：命令说明，供宿主与用户展示
//
// 注意：元数据校验要求插件至少声明一个命令或工具，否则无法安装。
type CommandDecl struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// RoleRequest 插件在元数据中申请的内置角色，对应 WIT plugin-info-response 的 requested-permissions
// （JSON ABI 中表现为 requested_roles 数组元素）。
//
// 字段：
//   - Role：角色标识，如 role::plugin_command_basic
//   - Required：为 true 时宿主在加载阶段校验该角色已授予，未授予则拒绝加载
//   - Reason：申请理由，用于说明权限用途
//
// 注意：申请不等于授予，最终以宿主策略为准；Host Function 每次调用都会重新鉴权，
// 不要依据 granted_roles/effective_actions 在 Guest 侧自行放行。
type RoleRequest struct {
	Role     string `json:"role"`
	Required bool   `json:"required"`
	Reason   string `json:"reason"`
}

// InitRequest 宿主调用 lanmei_init 时传入的请求，对应 WIT 的 init-request：
// 告知安装实例身份、配置与宿主最终授予的角色/动作。
//
// 字段：
//   - ABIVersion：ABI 版本，固定为 "lanmei.plugin/v1"
//   - PluginID：插件 ID
//   - InstallationID：本次安装实例 ID，宿主据此隔离插件状态
//   - Config：安装配置键值对，可为空
//   - GrantedRoles：宿主最终授予的角色列表
//   - EffectiveActions：宿主最终授予的动作列表（如 state.read、state.write）
//
// 注意：GrantedRoles 与 EffectiveActions 由宿主从策略读取，Guest 不可伪造；
// 鉴权始终在宿主侧强制执行，不要只依赖这些字段做权限判断。
type InitRequest struct {
	ABIVersion       string            `json:"abi_version"`
	PluginID         string            `json:"plugin_id"`
	InstallationID   string            `json:"installation_id"`
	Config           map[string]string `json:"config"`
	GrantedRoles     []string          `json:"granted_roles"`
	EffectiveActions []string          `json:"effective_actions"`
}

// InitResponse lanmei_init 的应答，对应 WIT 的 generic-ok-response。
//
// 字段：
//   - OK：true 表示初始化成功；false 时宿主放弃加载并关闭实例
//   - Error：失败原因，成功时可省略（json:"error,omitempty"）
//
// 注意：校验配置不通过时应返回 ok=false 并填写 error，而不是 panic 或返回非 0 返回码。
type InitResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// HandleRequest 宿主调用 lanmei_handle 时传入的事件请求，对应 WIT 的 handle-request。
//
// 字段：
//   - ABIVersion：ABI 版本，固定为 "lanmei.plugin/v1"
//   - EventID：事件唯一 ID，由宿主生成
//   - EventType：事件类型，"command" 或 "tool_call"；本插件未声明工具，只会收到 "command"
//   - Timestamp：事件时间，RFC 3339 UTC 字符串
//   - Message：消息上下文（见 MessageInfo），身份与群组信息由宿主填充
//   - Command：宿主解析后的命令（见 CommandInfo）
//
// 注意：tool_call 事件会在请求中携带本结构未声明的 tool_call 字段，
// encoding/json 解码时自动忽略未知字段。
type HandleRequest struct {
	ABIVersion string      `json:"abi_version"`
	EventID    string      `json:"event_id"`
	EventType  string      `json:"event_type"`
	Timestamp  string      `json:"timestamp"`
	Message    MessageInfo `json:"message"`
	Command    CommandInfo `json:"command"`
}

// MessageInfo 事件的消息上下文，对应 WIT 的 message-info。
//
// 字段：
//   - Text：消息文本，当前宿主填充为原始消息全文
//   - Raw：原始消息全文，当前与 Text 相同
//   - User：发送者信息（见 UserInfo），由宿主填充
//   - IsGroup：是否为群聊消息
//
// 注意：宿主还会下发 message_id（可缺省）与 group（群聊为对象、私聊为 null）字段，
// 本结构未声明这两个字段，encoding/json 解码时自动忽略；身份与群组信息只信宿主填充值。
type MessageInfo struct {
	Text    string   `json:"text"`
	Raw     string   `json:"raw"`
	User    UserInfo `json:"user"`
	IsGroup bool     `json:"is_group"`
}

// UserInfo 消息发送者信息，对应 WIT 的 user-info。
//
// 字段：
//   - ID：平台侧用户标识，来自宿主可信来源
//   - Nickname：昵称，宿主可能缺省（json:"nickname,omitempty"）
type UserInfo struct {
	ID       string `json:"id"`
	Nickname string `json:"nickname,omitempty"`
}

// CommandInfo 宿主解析后的命令信息，对应 WIT 的 command-info。
//
// 字段：
//   - Name：命令名，不含 / 前缀
//   - Args：按空白切分后的参数列表
//   - RawArgs：去掉 "/命令名" 前缀并去除首尾空白后的原始参数串
//   - RawMessage：未经处理的原始消息全文（含 /命令名）
//
// 注意：命令已由宿主解析，Guest 不应再从 Message.Text 自行切分。
type CommandInfo struct {
	Name       string   `json:"name"`
	Args       []string `json:"args"`
	RawArgs    string   `json:"raw_args"`
	RawMessage string   `json:"raw_message"`
}

// HandleResponse lanmei_handle 的响应，对应 WIT 的 handle-response。
//
// 字段：
//   - Handled：是否处理了本事件；false 时 Outputs 必须为空
//   - Outputs：回复内容列表，宿主始终回复触发本次调用的事件来源，不能指定目标用户或群
//
// 注意：输出仅支持 type="text"，单次最多 8 条、单条不超过 4096 字节，整个响应 JSON 不超过 64 KiB；
// 违反任一 ABI 约束时宿主丢弃本次响应并报错。
type HandleResponse struct {
	Handled bool         `json:"handled"`
	Outputs []OutputItem `json:"outputs"`
}

// OutputItem 单条回复内容，对应 WIT 的 output-item。
//
// 字段：
//   - Type：输出类型，当前仅支持 "text"
//   - Content：文本内容，须为合法 UTF-8，单条不超过 4096 字节
//
// 注意：输出不可指定接收者，宿主始终回复原事件。
type OutputItem struct {
	Type    string `json:"type"`
	Content string `json:"content"`
}

// StateGetRequest state_get 的请求（WIT state-get-request）。
//
// 字段：
//   - Key：Guest 逻辑 key（如 user:last_sign_date:provider=signin），不超过 256 字节；
//     宿主按 plugin:<installation_id>:<key> 加前缀隔离，因此无需自带插件前缀，
//     且不得包含 plugin_id、安装 ID、/、.. 或控制字符
type StateGetRequest struct {
	Key string `json:"key"`
}

// StateGetResponse state_get 的响应信封（WIT host-response）：成功时 OK=true 且 Data 为 StateGetData，
// 失败时 OK=false 且 data 字段缺省，失败详情见 error.code。
//
// 字段：
//   - OK：宿主是否成功处理本次请求；false 时 Data 保持零值，不能据此判断业务结果
//   - Data：查询结果（StateGetData）
//
// 注意：本示例未声明 error 字段，无法区分失败原因；需要时自行声明并按 error.code 处理。
type StateGetResponse struct {
	OK   bool         `json:"ok"`
	Data StateGetData `json:"data"`
}

// StateGetData state_get 成功响应 data 的内容（宿主 ABI 定义，对应 wasm_abi.md 中的状态查询结果）。
//
// 字段：
//   - Found：key 是否存在；false 表示不存在
//   - Value：已存的值；Found=false 时为空串
type StateGetData struct {
	Found bool   `json:"found"`
	Value string `json:"value"`
}

// StateSetRequest state_set 的请求（WIT state-set-request）。
//
// 字段：
//   - Key：逻辑 key，规则同 StateGetRequest
//   - Value：待写入的值，不超过 64 KiB
//   - TTLMs：过期时间（毫秒）；0 表示永不过期，正数最大 30 天，负数无效
type StateSetRequest struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	TTLMs int64  `json:"ttl_ms"`
}

// StateSetResponse state_set 的响应信封（WIT host-response）：成功为 {"ok":true,"data":{}}，
// 失败为 {"ok":false,"error":{"code":"…","message":"…"}}。
//
// 字段：
//   - OK：本次写入是否成功；本示例未检查该字段，正式插件应检查并按 error.code 处理失败
//
// 注意：permission_denied、value_too_large、ttl_out_of_range、state_unavailable 等业务错误
// 通过信封返回，不会 trap；错误码含义见 internal/plugin/wasm_abi.md。
type StateSetResponse struct {
	OK bool `json:"ok"`
}

// stateGet 调用宿主的 state_get，读取本安装实例的私有状态。
//
// 参数：
//   - 请求指针（uint64）：指向请求 JSON（StateGetRequest）的 Extism 内存偏移，内存须由 PDK 分配
//
// 返回：指向响应 JSON（StateGetResponse）的 Extism 内存偏移，读取后须释放
//
// 注意：//go:wasmimport 告诉 TinyGo 本函数没有 Go 实现，调用按 (PTR i64) -> (PTR i64) 的
// Wasm 导入签名进入宿主的 lanmei:host/v1 命名空间；Wasm 边界不能直接传字符串，因此入参与
// 返回值都是 Extism 内存偏移。权限不足、key 非法、存储不可用等错误不会 trap，而是返回
// {"ok":false,"error":{"code":"…"}} 信封，必须解析响应并检查 ok；错误码见 internal/plugin/wasm_abi.md。
//
//go:wasmimport lanmei:host/v1 state_get
func stateGet(uint64) uint64

// stateSet 调用宿主的 state_set，写入本安装实例的私有状态。
//
// 参数：
//   - 请求指针（uint64）：指向请求 JSON（StateSetRequest）的 Extism 内存偏移，内存须由 PDK 分配
//
// 返回：指向响应 JSON（StateSetResponse）的 Extism 内存偏移，读取后须释放
//
// 注意：调用约定与错误信封同 stateGet；成功返回 {"ok":true,"data":{}}，ttl_out_of_range、
// value_too_large 等失败同样以 {"ok":false,"error":{...}} 返回。
//
//go:wasmimport lanmei:host/v1 state_set
func stateSet(uint64) uint64

// callStateGet 用 JSON 字节调用 stateGet：分配请求内存、读取响应并在返回前释放两端内存。
//
// 参数：
//   - input：StateGetRequest 的 JSON 字节
//
// 返回：StateGetResponse 的 JSON 字节；本函数不解析信封，调用方需自行解码并检查 ok
func callStateGet(input []byte) []byte {
	request := pdk.AllocateBytes(input)
	defer request.Free()
	response := pdk.FindMemory(stateGet(request.Offset()))
	defer response.Free()
	return response.ReadBytes()
}

// callStateSet 用 JSON 字节调用 stateSet：分配请求内存、读取响应并在返回前释放两端内存。
//
// 参数：
//   - input：StateSetRequest 的 JSON 字节
//
// 返回：StateSetResponse 的 JSON 字节；本示例忽略返回值，正式插件应解码并检查 ok
func callStateSet(input []byte) []byte {
	request := pdk.AllocateBytes(input)
	defer request.Free()
	response := pdk.FindMemory(stateSet(request.Offset()))
	defer response.Free()
	return response.ReadBytes()
}

const (
	abiVersion   = "lanmei.plugin/v1"
	roleBasic    = "role::plugin_command_basic"
	stateKeySign = "user:last_sign_date:provider=signin"
	dateFormat   = "2006-01-02"
)

// lanmei_plugin_info 必需导出：向宿主声明插件元信息（WIT plugin interface 的 plugin-info）。
// 安装检查与加载阶段由宿主调用，可能被多次调用，实现必须无副作用。
//
// 参数：
//   - host_abi_version：宿主传入的 ABI 版本（Extism 输入 JSON，如 {"host_abi_version":"lanmei.plugin/v1"}），本示例不解析
//
// 返回：通过 Extism 输出返回 PluginInfoResponse 的 JSON；函数返回码 0 表示成功，
// 非 0 返回码、trap 或超时会被宿主视为调用失败。
//
//export lanmei_plugin_info
func lanmeiPluginInfo() int32 {
	resp := PluginInfoResponse{
		ABIVersion:  abiVersion,
		ID:          "signin",
		Name:        "签到",
		Description: "每日签到并累计积分",
		Version:     "1.0.0",
		Commands: []CommandDecl{
			{Name: "签到", Description: "每日试试手气"},
		},
		RequestedRoles: []RoleRequest{
			{Role: roleBasic, Required: true, Reason: "处理签到命令并保存用户签到状态"},
		},
	}

	data, _ := json.Marshal(resp)
	pdk.Output(data)
	return 0
}

// lanmei_init 必需导出：接收安装实例身份、配置与宿主最终授予的角色/动作，执行一次性初始化
// （WIT plugin interface 的 init）；初始化失败则该插件不被加载。
//
// 参数：
//   - 宿主输入 JSON（InitRequest）：安装实例、配置、granted_roles 与 effective_actions
//
// 返回：通过 Extism 输出返回 InitResponse 的 JSON；返回码 0 且 ok=true 才算成功，
// ok=false 时宿主放弃加载并关闭实例，失败原因写在 error 字段。
//
// 注意：本示例不校验配置，直接返回成功；需要校验配置时应在此返回 ok=false。
//
//export lanmei_init
func lanmeiInit() int32 {
	input := pdk.Input()
	var req InitRequest
	_ = json.Unmarshal(input, &req)

	// 当前无需额外初始化，直接返回成功
	resp := InitResponse{OK: true}
	data, _ := json.Marshal(resp)
	pdk.Output(data)
	return 0
}

// lanmei_handle 必需导出：处理宿主路由到本插件的命令事件（WIT plugin interface 的 handle）。
//
// 参数：
//   - 宿主输入 JSON（HandleRequest）：事件信息、消息上下文与宿主已解析的命令
//
// 返回：通过 Extism 输出返回 HandleResponse 的 JSON；返回码 0 表示成功，
// 非 0 返回码或 trap 会让宿主丢弃本次响应并记录失败。
//
// 注意：只能回复触发本事件的消息，不能指定目标用户或群；handled=false 时 outputs 必须为空；
// 输出仅支持 text，单次最多 8 条、单条不超过 4096 字节、响应 JSON 不超过 64 KiB；
// 单次调用超时 3 秒，宿主对同一实例串行调用，不要假设并发。
//
//export lanmei_handle
func lanmeiHandle() int32 {
	input := pdk.Input()
	var req HandleRequest
	_ = json.Unmarshal(input, &req)

	today := time.Now().UTC().Format(dateFormat)

	// 查询 state_get 判断今日是否已签到
	guestKey := stateKeySign + ":user=" + req.Message.User.ID
	stateGetReq := StateGetRequest{Key: guestKey}
	reqData, _ := json.Marshal(stateGetReq)

	result := callStateGet(reqData)
	var stateResp StateGetResponse
	_ = json.Unmarshal(result, &stateResp)

	if stateResp.Data.Found && stateResp.Data.Value == today {
		resp := HandleResponse{
			Handled: true,
			Outputs: []OutputItem{
				{Type: "text", Content: "今日已签到，明天再来吧！"},
			},
		}
		data, _ := json.Marshal(resp)
		pdk.Output(data)
		return 0
	}

	stateSetReq := StateSetRequest{
		Key:   guestKey,
		Value: today,
		TTLMs: 0,
	}
	setData, _ := json.Marshal(stateSetReq)
	_ = callStateSet(setData)

	resp := HandleResponse{
		Handled: true,
		Outputs: []OutputItem{
			{Type: "text", Content: "签到成功！\n本次积分: +10"},
		},
	}
	data, _ := json.Marshal(resp)
	pdk.Output(data)
	return 0
}

// lanmei_start 可选导出：初始化成功、路由就绪后由宿主调用一次（WIT plugin interface 的 start），
// 用于启动后台任务；导出不存在时宿主直接跳过。
//
// 参数：
//   - started_at：启动时间，RFC 3339 UTC 字符串（Extism 输入 JSON）
//
// 返回：通过 Extism 输出返回 {"ok":true} 或 {"ok":false,"error":"…"}；
// 返回 ok=false 或调用失败会导致宿主启动该插件失败。
//
// 注意：本示例无需后台任务，直接返回成功；WASM 插件不得自行启动常驻任务，
// 定时能力须等待宿主提供独立 ABI。
//
//export lanmei_start
func lanmeiStart() int32 {
	pdk.Output([]byte(`{"ok":true}`))
	return 0
}

// lanmei_stop 可选导出：卸载或退出前由宿主调用（WIT plugin interface 的 stop），用于释放资源；
// 导出不存在时宿主直接跳过。
//
// 参数：
//   - reason：停止原因，取值为 shutdown、unload、upgrade 或 init_rollback（Extism 输入 JSON）
//
// 返回：通过 Extism 输出返回 {"ok":true} 或 {"ok":false,"error":"…"}；
// 返回 ok=false 或调用失败只记录日志，不阻止卸载。
//
// 注意：本示例无资源需要清理，直接返回成功；实现须保证可重复调用（幂等）。
//
//export lanmei_stop
func lanmeiStop() int32 {
	pdk.Output([]byte(`{"ok":true}`))
	return 0
}

func main() {}
