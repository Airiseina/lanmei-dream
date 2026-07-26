# command 包 —— 斜杠命令系统

## 职责

只负责精确匹配的斜杠命令（/签到、/帮助、/抽卡），不参与角色扮演的消息处理。

```
用户消息
    │
    ├─ 以 / 开头 → command → 匹配注册的命令，执行 Handler
    │
    └─ 普通文本 → plugin/roleplay → ai.Chat()
```

## 核心设计

### Command 定义

```go
type Command struct {
    Name    string                    // "签到"
    Handler func(ctx *Context) error  // 处理函数
}
```

### System

```go
type System struct {
    commands map[string]Command
}

func (s *System) Register(cmd Command)  // 注册命令
func (s *System) Process(input string, ctx *Context) error  // 分发
```

### Context

```go
type Context struct {
    UserID  int64
    Message string
    Reply   func(string)  // 回复消息，不直接依赖 ZeroBot
}
```

## 注册方式

各插件在 `main.go` 组装阶段注册：

```go
cmdSys := command.New()
cmdSys.Register(command.Command{
    Name: "签到",
    Handler: signin.HandleSignin,
})
cmdSys.Register(command.Command{
    Name: "帮助",
    Handler: cmdSys.HelpHandler,  // 内置
})
```

## 远程安装 Wasm 插件

仅 `bot_owner` 可执行：

```text
/插件 安装 https://github.com/<owner>/<repo>/releases/download/<tag>/plugin.wasm
```

用户必须先将编译后的 Wasm 二进制上传到 GitHub Raw、GitHub Release 等提供公网 HTTPS 直链的平台。安装器拒绝 HTTP、本机或内网地址、非 `200 OK` 响应、HTML 页面以及超过运行时大小限制的内容；下载完成后插件保持未加载状态。
