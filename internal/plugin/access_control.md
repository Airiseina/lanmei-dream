# Wasm 插件权限开发指南

权限控制位于 `internal/plugin/access_control.go`。它使用 Casbin RBAC：主体绑定角色，角色拥有动作；无匹配策略时拒绝。

```text
Require(principal, action) -> allow | ErrPermissionDenied
```

Casbin 只负责动作授权。插件身份、状态隔离、Host Function 参数校验和 Extism 沙箱由宿主负责。

## 主体、角色与动作

| 类型 | 格式 | 示例 |
|---|---|---|
| 用户主体 | `user::<qq_id>` | `user::123456789` |
| 插件主体 | `plugin::<plugin_id>::<installation_id>` | `plugin::signin::01J4F7ABCD` |
| 系统主体 | `system::<name>` | `system::startup` |
| 角色 | `role::<name>` | `role::plugin_command_basic` |
| 动作 | `<resource>.<verb>` | `state.write` |

插件主体必须精确到安装实例，使用 `PluginPrincipal(pluginID, installationID)` 构造。Host Function 闭包捕获这个主体和 `installationID`；绝不能相信 Wasm 请求里的插件或安装 ID。

## 内置角色

| 角色 | 动作 |
|---|---|
| `role::plugin_command_basic` | `command.handle`、`message.reply`、`state.read`、`state.write`、`state.delete` |
| `role::plugin_runtime` | `plugin.load`、`plugin.start` |
| `role::bot_owner` | 所有 `plugin.*` 管理动作、`role.read`、`role.manage`、`audit.read` |

全部动作常量定义在 `access_control.go`。新动作必须先加入 `allActions()`，并按需加入 `builtinRoleActions()`；未知角色或动作不能写入策略。

## 初始化与管理

`NewService(db)` 复用 GORM PostgreSQL 连接，并将 Casbin 策略保存在 `plugin_casbin_rule`。启动时调用：

```go
authorizer, err := plugin.NewService(db)
if err != nil {
    return err
}
if err := authorizer.InitBuiltinPolicies(superUsers); err != nil {
    return err
}
```

初始化幂等地写入内置策略，给 `system::startup` 绑定 `role::plugin_runtime`。仅当数据库不存在任何 `role::bot_owner` 绑定时，才将 `SUPER_USERS` 引导为所有者；后续配置不会覆盖数据库策略。

业务代码只能通过 `Authorizer` 修改策略：

```go
pluginPrincipal := plugin.PluginPrincipal(pluginID, installationID)

if err := authorizer.BindRole(actor, pluginPrincipal, plugin.RolePluginCommandBasic); err != nil {
    return err
}
if err := authorizer.Require(pluginPrincipal, plugin.ActionStateWrite); err != nil {
    return err
}
```

`BindRole`、`UnbindRole` 分别要求调用者拥有 `plugin.role.bind`、`plugin.role.unbind`；`GrantAction`、`RevokeAction` 要求 `role.manage`。策略变更使用 Casbin 增量 API 和 `AutoSave`，不要关闭 `AutoSave` 后调用会重写全表的 `SavePolicy()`。

## Wasm 接入规则

1. `WasmManager.Load` 校验插件声明的必需角色；缺失时关闭刚创建的实例并拒绝加载。
2. `WasmCommandPass` 调用 `lanmei_handle` 前检查 `command.handle`，消费非空输出前检查 `message.reply`。
3. `NewStateHostFunctions` 在 `state_get`、`state_set`、`state_delete` 前分别检查对应的 `state.*` 动作。
4. 输出不能指定目标用户或群组；宿主始终从触发事件回填回复目标。
5. Guest key 只能是逻辑 key。宿主使用 `conduit.MakeStoreKey("plugin", installationID, guestKey)` 生成物理 key，因此不同安装实例互相隔离。

拒绝权限时，Host Function 返回 `permission_denied` 错误信封；不要向插件暴露策略表、数据库错误、物理状态 key 或调用栈。

## 插件申请角色

插件在 `lanmei_plugin_info` 的 `requested_roles` 中声明所需角色：

```json
{
  "role": "role::plugin_command_basic",
  "required": true,
  "reason": "处理命令并保存状态"
}
```

声明不是授权。管理主体必须显式为具体安装实例绑定角色；缺少 `required: true` 的角色时，加载失败并返回 `required_role_not_granted`。

## 扩展检查表

- 动作是否是宿主预定义的最小能力，而非通配符？
- Host Function 是否在访问受保护资源前调用 `Require`？
- 插件身份和资源范围是否只来自宿主可信上下文？
- 新状态输入是否有长度、控制字符、TTL 和 value 限制？
- 是否补充了允许、拒绝、解绑后立即拒绝和跨安装实例隔离测试？

需要按用户、群组、域名等资源授权时，再新增独立的 `(sub, obj, act)` Casbin 模型；不要改变现有无资源的 `(sub, act)` 模型。
