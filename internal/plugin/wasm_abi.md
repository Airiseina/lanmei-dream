# Wasm 插件 ABI 开发规范

面向 `lanmei.plugin/v1`。参考实现：[`examples/plugin/signin`](../../examples/plugin/signin)。

## 基础约定

- 编译为 WASI Wasm；Extism 使用 bytes-in / bytes-out，所有载荷均为 UTF-8 JSON。
- ABI 版本固定为 `lanmei.plugin/v1`；自定义 Host Function namespace 固定为 `lanmei:host/v1`。
- 时间使用 RFC 3339 UTC，所有 ID 使用 JSON string。未知 JSON 字段目前会被忽略；不得改变既有字段的语义，破坏性修改必须使用新的 ABI 版本。
- 调用失败（trap、非零 exit code、超时）时，宿主丢弃本次输出。导出调用串行执行，单次超时 3 秒；禁止在 Guest 中假设并发或调用次数。

## 必需 Guest Export

三个导出均必须存在，并以 Extism PDK 的输入/输出机制收发 JSON：

| 导出 | 输入 | 输出 | 约束 |
|---|---|---|---|
| `lanmei_plugin_info` | `{"host_abi_version":"lanmei.plugin/v1"}` | 插件元数据 | 无副作用；安装检查可能多次调用 |
| `lanmei_init` | 安装实例、配置和宿主最终授予的权限 | `{"ok":true}` 或 `{"ok":false,"error":"…"}` | 仅校验和初始化自身状态；失败则不加载 |
| `lanmei_handle` | 一个 `command` 事件 | `HandleResponse` | 只处理宿主路由到本插件的命令 |

`lanmei_plugin_info` 返回：

```json
{
  "abi_version": "lanmei.plugin/v1",
  "id": "signin",
  "name": "签到",
  "description": "每日签到",
  "version": "1.0.0",
  "commands": [{"name": "签到", "description": "每日签到"}],
  "requested_roles": [{"role": "role::plugin_command_basic", "required": true, "reason": "处理命令并保存状态"}]
}
```

- `id` 必须匹配 `^[a-z][a-z0-9_]{0,63}$`；`name` 和 `version` 非空；至少声明一个命令。
- 命令名不得包含 `/`、空白或控制字符，且不得重复或与现有命令冲突。
- `requested_roles` 只表达申请；实际权限由宿主决定。不要把权限判断仅建立在 `effective_actions` 上，Host Function 会再次鉴权。

`lanmei_init` 的请求结构：

```json
{
  "abi_version": "lanmei.plugin/v1",
  "plugin_id": "signin",
  "installation_id": "…",
  "config": {"key": "value"},
  "granted_roles": ["role::plugin_command_basic"],
  "effective_actions": ["command.handle", "message.reply", "state.read", "state.write", "state.delete"]
}
```

`lanmei_handle` 的输入包含 `event_id`、`timestamp`、`message` 和宿主解析完成的 `command`：

```json
{
  "abi_version": "lanmei.plugin/v1",
  "event_id": "…",
  "event_type": "command",
  "timestamp": "2026-07-27T00:00:00Z",
  "message": {"text": "/签到", "raw": "/签到", "user": {"id": "10001"}, "group": null, "is_group": false},
  "command": {"name": "签到", "args": [], "raw_args": "", "raw_message": "/签到"}
}
```

返回格式：

```json
{"handled":true,"outputs":[{"type":"text","content":"签到成功"}]}
```

- 仅支持 `event_type="command"` 和 `outputs[].type="text"`。
- `handled=false` 时 `outputs` 必须为空。输出不得指定目标用户或群；宿主始终回复原事件。
- `message_id`、`nickname`、`group` 可缺省；私聊 `group` 为 `null`。身份和群组信息均由宿主提供，不可信任 Guest 自行构造的副本。

## 可选生命周期 Export

| 导出 | 输入 | 时机 |
|---|---|---|
| `lanmei_start` | `{"started_at":"RFC3339"}` | 初始化成功、路由就绪后调用一次 |
| `lanmei_stop` | `{"reason":"shutdown|unload|upgrade|init_rollback"}` | 卸载或退出前调用 |

两者输出均为 `{"ok":true}` 或 `{"ok":false,"error":"…"}`。`lanmei_stop` 失败只记录，不阻止卸载；不得启动后台任务，定时任务须等待宿主提供独立 ABI。

## Host Imports：实例私有状态

导入签名均为 `(PTR i64) -> (PTR i64)`，使用 `lanmei:host/v1`。传入和返回的 PTR 都指向 Extism 管理的 JSON 内存；使用 PDK 分配、读取并释放，禁止手写裸内存长度计算。

所有响应采用同一信封：

```json
{"ok":true,"data":{}}
// 或
{"ok":false,"error":{"code":"permission_denied","message":"动作未被授权"}}
```

| Host Function | 所需动作 | 请求 | 成功 `data` |
|---|---|---|---|
| `state_get` | `state.read` | `{"key":"user:10001"}` | `{"found":true,"value":"…"}`；不存在为 `{"found":false,"value":""}` |
| `state_set` | `state.write` | `{"key":"…","value":"…","ttl_ms":0}` | `{}` |
| `state_delete` | `state.delete` | `{"key":"…"}` | `{}` |

- `ttl_ms=0` 表示永不过期；正数最大 30 天，负数无效。
- Guest key 是逻辑 key；宿主按 `plugin:<installation_id>:<key>` 隔离。不得在 key 中放 `plugin_id`、安装 ID、`/`、`..` 或控制字符。
- Host 返回错误应按 `error.code` 处理：`invalid_request`、`permission_denied`、`key_too_large`、`value_too_large`、`ttl_out_of_range`、`state_unavailable`、`internal_error`。权限和安装身份由宿主闭包绑定，Guest 不能伪造或提升。

## 限制与交付检查

| 项目 | 上限 |
|---|---:|
| Wasm 文件 / 内存 | 16 MiB / 256 pages（16 MiB） |
| 单次 Guest 输入 / 输出 JSON | 256 KiB / 64 KiB |
| 单次文本输出 / 单条文本 | 8 条 / 4096 UTF-8 bytes |
| state key / value | 256 bytes / 64 KiB |

交付前至少验证：可导出三个必需函数；`plugin_info` 可重复调用且无副作用；无权限状态调用能处理错误；`handle` 对 `handled=false` 不返回输出；使用与目标语言匹配的 PDK 构建并运行一次真实命令路径。Go/TinyGo 参考构建：`tinygo build -o plugin.wasm -target wasi main.go`。
