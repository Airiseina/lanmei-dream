# command 包 —— 斜杠命令系统

## 职责

本包只负责斜杠命令的注册、查找与分发（如 `/签到`、`/帮助`、`/随机美图`），不参与普通消息的 AI 角色扮演；包内不依赖 bot / gateway / plugin，`System`、`Command`、`Context` 均为纯数据结构，由上层组装（见 `internal/command/module.go`）。

```
用户消息
    │
    ├─ 以 / 开头 → bot 行为树命令分支 → System.Process → Handler
    │
    └─ 普通文本 → 行为树其余分支（插件子树 / 意图分析 / AI 聊天）
```

## 核心类型

### Command

```go
type Command struct {
    Name        string                    // 命令名，不含 / 前缀，全局唯一
    Description string                    // 展示在 /帮助 与 /help 列表，可为空
    Handler     func(ctx *Context) error  // 必填；返回错误会中断消息处理管线
    Order       int                       // 帮助列表排序权重，小的在前；0 排末尾
}
```

### System

```go
cmdSys := command.New()                          // 创建
err := cmdSys.Register(command.Command{...})     // 注册；空名/重复/nil handler 返回错误，不覆盖已有命令
cmdSys.Unregister("命令名")                       // 注销，幂等（插件卸载时由 Registry 调用）
cmd, ok := cmdSys.Lookup("命令名")                // 只读查找
cmds := cmdSys.List()                            // 按名称排序的命令快照
err := cmdSys.Process("/命令名 参数", ctx)        // 分发
err := cmdSys.HelpHandler(ctx)                   // 内置帮助命令
```

`Process` 的语义：

- 归一化空白后把 `"/命令名 参数"` 写回 `ctx.Message`；未知命令先经 `ctx.Reply` 提示再返回错误
- 不填充 `ctx.CommandName` / `ctx.CommandArgs`，也不设置 `ReplySegments` / `SuppressRequesterAt`（这些由 bot 层 Pass 填充）；插件的斜杠命令经 bot 层 Pass 分发，不走 `Process`
- 帮助列表排序：`Order` 升序（0 视为最大值排末尾），同权重按命令名升序

### Context

`Context` 字段由 bot 层在调用 Handler 前注入，插件只读使用：

| 字段 | 说明 |
|---|---|
| `Platform` / `PlatformUserID` | 平台标识与发送者平台用户 ID |
| `GroupID` / `IsGroup` | 群 ID（私聊为空）与会话类型 |
| `CommandName` / `CommandArgs` | 命中的命令名与参数列表（由 bot 层命令分发 Pass 填充） |
| `Message` | 命令原文（含 `/` 前缀与参数） |
| `Reply` | 发送文本回复；群聊默认自动 @ 请求者 |
| `ReplySegments` | 回传 OneBot 原生段（at/text/image 等），非空时优先于 `Reply` 文本 |
| `SuppressRequesterAt` | 关闭本次文本回复的自动 @请求者 |
| `SelfID` / `AtTargets` / `Nickname` / `MessageID` / `ConnID` | 消息上下文（平台 ID、at 目标、昵称、消息 ID、来源连接） |
| `IsSuperUser` | 发送者是否超管（由静态与动态管理员合并得出） |
| `CommandReentry` | 命令来自插件命令重入时为 true，占位 Handler 据此退出，避免无限递归 |

## 注册位置

命令由三方注册，全部经同一个 `command.System`：

| 位置 | 命令 |
|---|---|
| `cmd/lanmei/main.go` | `/帮助`（Order 51）、`/help`（50）、`/插件`（安装 Wasm 插件，150） |
| `internal/bot/bot.go`（`registerAdminCommands`） | `/admin`（140）、`/添加管理员`（160） |
| 插件（`internal/plugin/registry.go`） | 按 `PluginInfo.Commands` 在插件 OnInit 阶段注册，Order 取 `CommandDef.Order`；插件卸载时自动注销 |

```go
cmdSys := command.New()
if err := cmdSys.Register(command.Command{
    Name:        "帮助",
    Description: "显示可用命令",
    Handler:     cmdSys.HelpHandler,
    Order:       51,
}); err != nil {
    // 处理错误
}
```

## 分发链路

- 斜杠命令：bot 行为树 `IsCommand` 分支进入 `pipeline.command`，`CommandRouterPass` 解析命令名/参数并查表，`ExecuteCommandPass` 执行 Handler，Handler 的 `Reply` 文本与 `ReplySegments` 段写回消息上下文
- 管理命令：`/admin` 前缀由 `IsAdminCommand` 条件进入 `pipeline.admin`，由 `CommandPass` 执行
- 自然语言触发：意图分析识别出命令后，`IntentCommandExecPass` 直接执行对应 Handler
- 插件命令：注册的 Handler 是占位实现（`internal/plugin/registry.go` 的 `makeCommandHandler`），把 `/命令名 参数` 重入引擎交给插件子树处理；重入消息带 `CommandReentry` 标记防止死循环

## 远程安装 Wasm 插件

仅 `bot_owner` 可执行（命令定义见 `internal/plugin/wasm_command.go`，需恰好三个字段）：

```text
/插件 安装 https://github.com/<owner>/<repo>/releases/download/<tag>/plugin.wasm
```

用户必须先将编译后的 Wasm 二进制上传到 GitHub Raw、GitHub Release 等提供公网 HTTPS 直链的平台。安装器拒绝 HTTP、本机或内网地址、非 `200 OK` 响应、HTML 页面以及超过运行时大小限制的内容；下载完成后插件保持未加载状态（安装记录 `Enabled=false`，加载/启用由管理流程执行）。
