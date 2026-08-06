# 插件拓展规范

本规范定义蓝妹（lanmei-dream）插件系统的开发约定，面向在仓库内新增插件的开发者。

## 1. 插件类型选择

| 类型 | 位置 | 适用场景 | 特性 |
|---|---|---|---|
| **Go 内置插件** | `internal/bizplugin/<id>/` | 核心业务、需直连 DB/内存、可靠性要求高 | 编译期注册、代码级访问、无沙箱 |
| **WASM 插件** | `examples/plugin/<id>/` | 第三方交付、动态安装、热插拔 | Extism 沙箱、ABI 隔离、权限/配额管控 |

选择原则：
- 插件需要访问 `database.DB` 之外的基础设施（Redis、pgvector、LLM 等）且不暴露给第三方 → Go 内置插件
- 插件需要动态安装/卸载、多实例、跨用户分发 → WASM 插件
- 不确定时优先 **Go 内置插件**（开发成本低），后续需要可再迁移到 WASM

## 2. 目录约定

```
internal/bizplugin/            # Go 内置插件，一个插件一个包
  signin/
    signin.go                  # 主实现（命令/Pass/Pipeline/Subtree/工具）
examples/plugin/<id>/          # WASM 插件，独立 Go module（tinygo 构建）
  go.mod
  main.go                      # 实现 lanmei.plugin/v1 ABI
data/plugins/                  # 运行时 wasm 仓库（不入库，gitignore）
  inbox/                       # 待安装 .wasm 的投放目录
  installed/<installationID>/  # Manager 自动管理，禁止手动修改
schema/plugin/                 # WASM ABI 的 WIT 定义（改 ABI 时同步更新）
docs/                          # 插件说明文档（插件较多时建议 <id>.md）
```

## 3. Go 内置插件规范

### 3.1 骨架（以 `internal/bizplugin/signin` 为参考实现）

```go
type XxxPlugin struct { /* 依赖在 OnInit 中从 PluginContext 获取 */ }

// Info 返回元信息（ID 唯一、命令/工具声明）
func (p *XxxPlugin) Info() pluginpkg.PluginInfo {
    return pluginpkg.PluginInfo{
        ID:          "xxx",
        Name:        "名称",
        Description: "描述",
        Version:     "1.0.0",
        Commands:    []pluginpkg.CommandDef{{Name: "命令", Description: "描述"}},
        SubtreeID:   pluginpkg.SubtreeID("xxx"),
    }
}

// OnInit 注册 Pass → Pipeline → Subtree，全部必须 Track（卸载自动清理）
func (p *XxxPlugin) OnInit(ctx *pluginpkg.PluginContext) error { ... }

func (p *XxxPlugin) OnStart(*pluginpkg.PluginContext) error { return nil }
func (p *XxxPlugin) OnStop(*pluginpkg.PluginContext) error  { return nil }
```

### 3.2 注册资源三步（顺序固定）

```go
// 1. 注册 Pass（业务逻辑），依赖通过结构体注入
passID := pluginpkg.PassID("xxx", "execute")
ctx.Engine.RegisterPass(passID, &xxxExecutePass{...})
ctx.Registry.TrackPass("xxx", passID)

// 2. 注册 Pipeline（通过 Pass ID 引用，支持热替换）
pipelineID := pluginpkg.PipelineID("xxx", "main")
ctx.Engine.RegisterPipeline(conduit.NewPipelineFromIDs(pipelineID, passID, replyPassID))
ctx.Registry.TrackPipeline("xxx", pipelineID)

// 3. 注册行为树 Subtree（路由条件 + 管线）
subtree := conduit.NewSequence(
    conduit.NewCondition(isXxxCommand),
    conduit.NewAction(pipelineID),
)
ctx.Engine.RegisterSubtree(pluginpkg.SubtreeID("xxx"), subtree)
```

### 3.3 硬性约定

- **Pass 内禁止调用 `ctx.Send()`**，回复统一写入 `conduit.AppendOutput(ctx, &conduit.Message{...})`，由引擎统一发送
- 跨 Pass 传数据用 `conduit.Set(ctx, key, value)` / `conduit.Get[T](ctx, key)`，**key 必须带插件前缀**（如 `plugin.xxx.result`），避免与其他插件冲突
- 状态持久化使用 `StoreKey(pluginID, key)` 生成隔离 key（`plugin:<pluginID>:<key>`）
- 注册的任何资源都必须调用对应的 `TrackPass/TrackPipeline/TrackTool`，否则卸载时泄漏
- 在 `cmd/lanmei/main.go` 的 `pluginReg.Register(...)` 处注册，与 `signin` 并列
- 插件 ID 规则：小写字母+数字+下划线，以字母开头，长度 ≤64

### 3.4 可提供的资源

| 资源 | 说明 |
|---|---|
| 斜杠命令 | `Info().Commands` 声明，命令名不含 `/` |
| AI 工具 | `Info().Tools` 声明 `ToolDef{Name, Description, Parameters, Handler}`，注册后 LLM 可调用 |

## 4. WASM 插件规范

完整 ABI 见 [internal/plugin/wasm_abi.md](internal/plugin/wasm_abi.md)，WIT 定义见 [schema/plugin/lanmei-plugin.wit](schema/plugin/lanmei-plugin.wit)。

### 4.1 开发与构建

```bash
# 新建独立 module（参照 examples/plugin/signin）
tinygo build -o xxx.wasm -target wasi main.go
# 投放安装
cp xxx.wasm data/plugins/inbox/        # 或走远程 URL 安装
```

### 4.2 必需 Guest Export

| 导出 | 作用 |
|---|---|
| `lanmei_plugin_info` | 声明 id/name/version/commands/tools/权限申请，无副作用 |
| `lanmei_init` | 校验配置、初始化自身状态，失败则不加载 |
| `lanmei_handle` | 处理 `command` / `tool_call` 事件 |

可选：`lanmei_start` / `lanmei_stop`（后台任务、资源清理）。

### 4.3 宿主能力（Host Function）

| 能力 | 权限 | 约束方式 |
|---|---|---|
| `state_get/set/delete` 等 | `state:read/write/delete` | key 按安装实例隔离 |
| `http_get/http_post` | `http:get/post` | `allow_hosts` 白名单 |
| `db_query/db_exec` | `db:read/write` | 表名隔离（`plugin_<id>_` 前缀） |

**新增宿主能力的四步**（先例：无现成模板，需修改宿主代码）：
1. `internal/plugin/wasm_abi.go`：加请求/响应 DTO，注册 `HostFunctionActions` 映射
2. `internal/plugin/wasm_host.go`：用 `newLimitedHostFunction` 实现 + `NewDenyAllHostFunctions` 加拒绝版
3. `internal/plugin/capability.go`：加 `Permission` 常量
4. `internal/plugin/access_control.go`：加 `ActionXxx` 常量并同步 `allActions()`

## 5. 权限与安全

- 权限模型：`Permission`（能做什么）+ `Scope`（在什么范围内做）+ `Capability`（绑定到安装实例），参考 Tauri v2
- WASM 插件默认仅 `role::plugin_command_basic`（命令处理 + 消息回复 + state 读写），DB/HTTP 需在 `RequestedRoles` 显式申请
- **沙箱边界**：宿主能力是插件唯一的外部访问通道；WASM 侧无网络/文件系统直连
- **fail-closed 原则**：权限检查在宿主侧强制执行，不信任 Guest 自报的 `effective_actions`
- 插件不得获取宿主未授予的 API Key、数据库凭据、系统 Prompt 等敏感信息

## 6. 开发检查清单

- [ ] 插件 ID 合法且不与现有插件冲突（`pluginReg.List()` 可查）
- [ ] Go 插件：Pass/Pipeline/Subtree 全部 Track；回复用 `AppendOutput`
- [ ] WASM 插件：声明了所有实际使用的权限；不依赖 `effective_actions` 做鉴权
- [ ] 命令名不包含 `/`、空白、控制字符
- [ ] 状态 key 带插件前缀，避免跨插件冲突
- [ ] 生命周期方法可重复调用安全（幂等），OnStop 能清理所有资源
- [ ] `go build ./...` 通过；WASM 插件按 4.1 构建验证

## 7. 现有插件

| ID | 类型 | 说明 |
|---|---|---|
| `signin` | Go 内置（`internal/bizplugin/signin`）+ WASM 示例（`examples/plugin/signin`） | 每日签到 |

> 本文档随插件系统演进同步更新；ABI 变更须同时更新 `schema/plugin/lanmei-plugin.wit` 与 `internal/plugin/wasm_abi.md`。
