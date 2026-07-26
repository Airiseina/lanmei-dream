// Package main 是蓝妹 Wasm 插件的 Go PDK 参考实现。
//
// 功能：每日签到命令插件，演示 ABI 闭环：
//   - lanmei_plugin_info → 声明命令和角色
//   - lanmei_init → 验证配置
//   - lanmei_handle → 通过 state_get/state_set 读写签到状态，返回文本回复
//
// 构建（需要 TinyGo 0.30+ 和 extism/go-pdk）:
//
//	tinygo build -o signin.wasm -target wasi main.go
//
// 构建后将 signin.wasm 放入 data/plugins/inbox/，通过 Manager 安装。
package main

import (
	"encoding/json"
	"time"

	"github.com/extism/go-pdk"
)

// PluginInfoResponse 匹配 ABI lanmei.plugin/v1
type PluginInfoResponse struct {
	ABIVersion     string        `json:"abi_version"`
	ID             string        `json:"id"`
	Name           string        `json:"name"`
	Description    string        `json:"description"`
	Version        string        `json:"version"`
	Commands       []CommandDecl `json:"commands"`
	RequestedRoles []RoleRequest `json:"requested_roles"`
}

type CommandDecl struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type RoleRequest struct {
	Role     string `json:"role"`
	Required bool   `json:"required"`
	Reason   string `json:"reason"`
}

type InitRequest struct {
	ABIVersion       string            `json:"abi_version"`
	PluginID         string            `json:"plugin_id"`
	InstallationID   string            `json:"installation_id"`
	Config           map[string]string `json:"config"`
	GrantedRoles     []string          `json:"granted_roles"`
	EffectiveActions []string          `json:"effective_actions"`
}

type InitResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

type HandleRequest struct {
	ABIVersion string      `json:"abi_version"`
	EventID    string      `json:"event_id"`
	EventType  string      `json:"event_type"`
	Timestamp  string      `json:"timestamp"`
	Message    MessageInfo `json:"message"`
	Command    CommandInfo `json:"command"`
}

type MessageInfo struct {
	Text    string   `json:"text"`
	Raw     string   `json:"raw"`
	User    UserInfo `json:"user"`
	IsGroup bool     `json:"is_group"`
}

type UserInfo struct {
	ID       string `json:"id"`
	Nickname string `json:"nickname,omitempty"`
}

type CommandInfo struct {
	Name       string   `json:"name"`
	Args       []string `json:"args"`
	RawArgs    string   `json:"raw_args"`
	RawMessage string   `json:"raw_message"`
}

type HandleResponse struct {
	Handled bool         `json:"handled"`
	Outputs []OutputItem `json:"outputs"`
}

type OutputItem struct {
	Type    string `json:"type"`
	Content string `json:"content"`
}

// Host Function: state_get
type StateGetRequest struct {
	Key string `json:"key"`
}

type StateGetResponse struct {
	OK   bool         `json:"ok"`
	Data StateGetData `json:"data"`
}

type StateGetData struct {
	Found bool   `json:"found"`
	Value string `json:"value"`
}

// Host Function: state_set
type StateSetRequest struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	TTLMs int64  `json:"ttl_ms"`
}

type StateSetResponse struct {
	OK bool `json:"ok"`
}

const (
	abiVersion   = "lanmei.plugin/v1"
	roleBasic    = "role::plugin_command_basic"
	stateKeySign = "user:last_sign_date:provider=signin"
	dateFormat   = "2006-01-02"
)

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
	pdk.OutputSet(data)
	return 0
}

//export lanmei_init
func lanmeiInit() int32 {
	// 读取输入
	input := pdk.Input()
	var req InitRequest
	_ = json.Unmarshal(input, &req)

	// 首版无额外初始化逻辑
	resp := InitResponse{OK: true}
	data, _ := json.Marshal(resp)
	pdk.OutputSet(data)
	return 0
}

//export lanmei_handle
func lanmeiHandle() int32 {
	input := pdk.Input()
	var req HandleRequest
	_ = json.Unmarshal(input, &req)

	today := time.Now().UTC().Format(dateFormat)

	// 调用 state_get 检查今日是否已签到
	guestKey := stateKeySign + ":user=" + req.Message.User.ID
	stateGetReq := StateGetRequest{Key: guestKey}
	reqData, _ := json.Marshal(stateGetReq)

	result := pdk.CallHostFunc("lanmei:host/v1", "state_get", reqData)
	var stateResp StateGetResponse
	_ = json.Unmarshal(result, &stateResp)

	if stateResp.Data.Found && stateResp.Data.Value == today {
		// 已签到
		resp := HandleResponse{
			Handled: true,
			Outputs: []OutputItem{
				{Type: "text", Content: "今日已签到，明天再来吧！"},
			},
		}
		data, _ := json.Marshal(resp)
		pdk.OutputSet(data)
		return 0
	}

	// 写入今日签到日期
	stateSetReq := StateSetRequest{
		Key:   guestKey,
		Value: today,
		TTLMs: 0,
	}
	setData, _ := json.Marshal(stateSetReq)
	_ = pdk.CallHostFunc("lanmei:host/v1", "state_set", setData)

	resp := HandleResponse{
		Handled: true,
		Outputs: []OutputItem{
			{Type: "text", Content: "签到成功！\n本次积分: +10"},
		},
	}
	data, _ := json.Marshal(resp)
	pdk.OutputSet(data)
	return 0
}

//export lanmei_start
func lanmeiStart() int32 {
	pdk.OutputSet([]byte(`{"ok":true}`))
	return 0
}

//export lanmei_stop
func lanmeiStop() int32 {
	pdk.OutputSet([]byte(`{"ok":true}`))
	return 0
}

func main() {}
