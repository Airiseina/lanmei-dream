# 数据流说明

## 消息处理流

```
用户消息 → NapCat(OneBot11) / Onebots(OneBot12) → WebSocket → gateway.Server → bot.handleMessage
  │
  ├─ gateway 协议适配：OneBot 11 / OneBot 12 → 统一 NormalizedMessage
  └─ engine.Process(input) → 同步拿到结果
       │
       ├─ 行为树决策
       │    ├─ /admin → pipeline.admin
       │    ├─ /xxx   → pipeline.command
       │    └─ 其余   → pipeline.intent（意图分析）
       │
       ├─ 执行管线 Pass
       │    ├─ CommandRouterPass → 解析命令名/参数 → ExecuteCommandPass → command.System.Process → Handler
       │    ├─ IntentPass → IntentAnalyzer.Analyze
       │    │    ├─ intent=command → CommandRouterPass → ExecuteCommandPass（复用命令处理）
       │    │    ├─ intent=tool    → RoleplayPass → ChatService.Chat（AI 自主工具调用）
       │    │    ├─ intent=chat    → RoleplayPass → ChatService.Chat → 回复
       │    │    └─ intent=ignore  → 静默忽略
       │    └─ FallbackPass → 兜底回复
       │
       └─ OnResponse 回调（输出消息/记录错误）
```

## 网关协议流

```
IM 平台消息
  │
  ├─ NapCat (NTQQ) ──── 反向 WebSocket ──── OneBot 11 协议 ──┐
  │                                                          ├─→ gateway.Server
  └─ Onebots ──────────── 反向 WebSocket ──── OneBot 12 协议 ─┘
                                                                  │
                                                            协议适配层
                                                                  │
                                                          NormalizedMessage
                                                                  │
                                                            bot.handleMessage
```

## 意图分析流

```
自然语言消息 → IntentPass.Execute
  │
  ├─ IntentAnalyzer.Analyze(ctx, msg)
  │    ├─ LLM 可用 → 构建 prompt（可用命令列表 + AI 工具列表 + 用户消息）→ LLM 分类 → JSON 解析
  │    └─ LLM 不可用 → 降级返回 {intent:"chat", confidence:1.0}
  │
  ├─ 结果路由（4 种意图）
  │    ├─ intent="command", command="签到" → ctx.RawMsg = "/签到" → CommandRouterPass → ExecuteCommandPass
  │    ├─ intent="tool"                    → RoleplayPass → ChatService（AI 通过 ToolCalling 自主调用工具）
  │    ├─ intent="chat"                    → RoleplayPass → ChatService.Chat
  │    └─ intent="ignore"                  → return nil（静默）
  │
  └─ 错误降级：分析失败 → 走 RoleplayPass（与之前行为一致）
```

## AI 工具调用流

```
ChatService.Chat（含工具调用循环）
  │
  ├─ 1. 组装 system prompt + LOD 上下文 + RAG 检索
  ├─ 2. 检查 tool.Registry 是否有工具 → 如有，通过 ChatWithTools 注入工具定义
  ├─ 3. 调用 LLM.Generate()
  │
  ├─ 4. processToolCalls 循环（最多 5 轮）
  │    ├─ 响应含 ToolCalls → 执行工具 → 追加 Tool 角色消息 → 再次调用 LLM
  │    │    ├─ 工具属于内部插件 → 直接调用 Handler(ctx, argsJSON)
  │    │    └─ 工具属于 WASM 插件 → 构建 HandleRequest{EventType:EventTypeToolCall}
  │    │                          → 调用 lanmei_handle → 返回 HandleResponse
  │    └─ 无 ToolCalls → 返回最终回复
  │
  └─ 5. 存储对话 + 触发压缩
```

## 命令管线流

```
消息（/命令 开头）
  │
  ├─ 行为树匹配 → pipeline.command
  │
  ├─ CommandRouterPass.Execute
  │    ├─ 解析 /命令名 参数列表
  │    ├─ command.System.Lookup(命令名) → 查找已注册命令
  │    ├─ 将命令信息写入 MessageContext（CommandName + CommandArgs）
  │    └─ 返回
  │
  └─ ExecuteCommandPass.Execute
       ├─ 从 MessageContext 读取命令信息
       ├─ 执行 Handler
       ├─ 输出写入 Conduit 输出
       └─ 插件命令不再需要二次 engine.Process
```

## Conduit 引擎配置

```
Engine
  ├─ Workers: 4
  ├─ Timeout: 10s
  ├─ Fallback: pipeline.fallback
  ├─ StateStore: RedisStore（Redis，TTL 自动过期 + LRU 淘汰 + 原子操作）
  └─ BehaviorTree: Selector[admin → command → intent]
```

## 插件系统流

### 插件注册与生命周期

```
plugin.Registry
  │
  ├─ 内置插件注册（bizplugin）
  │    └─ signin → Registry.RegisterPlugin(signinPlugin)
  │
  ├─ WASM 插件注册（WasmManager）
  │    ├─ Install(url)      → 远程下载/本地加载 Wasm 文件
  │    ├─ Load(manifest)    → 解析 Manifest、提取 PluginInfo
  │    ├─ Start(pluginID)   → Extism 运行时启动 → WasmPlugin 适配器 → Registry.RegisterPlugin
  │    ├─ Stop(pluginID)    → Extism 插件关闭 → 资源释放
  │    └─ Uninstall(pluginID) → 清理注册表 + 删除文件
  │
  └─ 生命周期：Register → Init → Start → Stop
       ├─ Register: 注册命令(command.System)、工具(tool.Registry)
       ├─ Init:    初始化插件状态，加载 Capability
       ├─ Start:   启动运行时（WASM 启动 Extism 插件实例）
       └─ Stop:    停止运行时，清理注册的工具和命令
```

### 插件设施流

```
插件调用设施
  │
  ├─ StateStore（状态存储）
  │    ├─ state_get / state_set / state_delete（基础操作）
  │    ├─ compare_and_swap（CAS 乐观锁，Lua 脚本保证原子性）
  │    ├─ incr_by（原子增减，Redis INCRBY）
  │    └─ set_if_not_exists（SETNX）
  │    Scope 检查：key_prefix 校验
  │
  ├─ DB Access（数据库访问，IndexedDB 隔离模型）
  │    ├─ db_query(table, query_json) → 查询授权表
  │    ├─ db_exec(table, exec_json)   → 写入授权表
  │    ├─ 表名校验：仅允许 Scope 中 tables 列表的表名（防 SQL 注入）
  │    └─ 隔离模型：每个插件只能访问自己声明的表
  │    Scope 检查：tables 校验
  │
  └─ HTTP Access（HTTP 客户端，fetch-like）
       ├─ http_get(url, headers_json)   → GET 请求
       ├─ http_post(url, headers_json, body) → POST 请求
       └─ 域名校验：仅允许 Scope 中 allow_hosts 白名单的域名
       Scope 检查：allow_hosts 校验
```

## 安全检查流

```
插件操作请求
  │
  ├─ Layer 1: Permission 检查
  │    ├─ 插件是否声明了对应 Permission（如 state:read、db:write、http:get）
  │    └─ Capability 中是否授权了该 Permission
  │
  ├─ Layer 2: Scope 检查（ScopeChecker）
  │    ├─ state 操作 → 检查 key 是否匹配 key_prefix
  │    ├─ db 操作    → 检查表名是否在 tables 列表中
  │    └─ http 操作  → 检查 URL host 是否匹配 allow_hosts
  │
  ├─ Layer 3: RBAC 检查（Casbin）
  │    └─ 角色→权限映射，快速授权路径
  │
  ├─ Layer 4: ResourceQuota 检查
  │    ├─ 内存/CPU 限制
  │    ├─ 调用速率限制
  │    └─ 并发数限制
  │
  └─ Layer 5: 审计日志（AuditLogger）
       └─ zap 结构化记录：principal + permission + scope + decision(allow/deny) + reason
```

## 基础设施流

```
main.go
  │
  └─ infra.Setup(ctx, cfg)
       ├─ PostgreSQL 连接 + GORM 迁移 + pgvector 扩展启用 + HNSW 索引
       ├─ Redis 连接（64MB 上限 + allkeys-lru）
       ├─ PGVectorStore（实现 MemoryStore 接口）→ inf.MemStore
       ├─ RedisStore（实现 StateStore 接口 + CAS/IncrBy/SetNX）→ inf.StateStore
       └─ InitLogger(cfg.LogConfig) → inf.Logger（zap + lumberjack 轮转）
```

## 记忆层

```
LOD 三级压缩架构：

L0 原始对话 (PostgreSQL conversations)
  ├── 每条消息原文保存
  └── 超过 40 条 → 触发压缩到 L1

L1 Episode Summary (PostgreSQL episode_summaries)
  ├── brief: 一句话总结（≤50字）
  ├── detailed: 详细摘要（≤200字），保留关键事实/情感/决策
  ├── facts: 结构化事实列表（JSONB），如 "用户喜欢猫"
  └── 超过 10 条 → 触发聚合到 L2

L2 Topic Cluster (PostgreSQL topic_clusters + pgvector memory_vectors)
  ├── topic: 主题名称（LLM 生成，如"宠物话题"）
  ├── brief: 一句话主题总结
  ├── detailed: 详细主题描述
  ├── facts: 聚合后的关键事实
  └── 向量化存入 pgvector（memory_vectors 表，HNSW 索引），支持语义检索
```

## 压缩流程

```
用户发消息 → RoleplayPass → ChatService.Chat
                                    │
                                    ├── 1. LOD 上下文组装（3000 token 预算）
                                    │     L2 主题概览 → L1 摘要 → L0 原文
                                    │     按 token 预算优先级填充
                                    │
                                    ├── 2. RAG 检索（pgvector 向量相似度）
                                    │
                                    ├── 3. 工具调用循环（processToolCalls，max 5 轮）
                                    │     检查 tool.Registry → ChatWithTools → Generate
                                    │     有 ToolCalls → 执行 → 追加消息 → 再次 Generate
                                    │
                                    ├── 4. 拼提示 → LLM → 回复
                                    │
                                    ├── 5. 异步存记忆（pgvector）
                                    │
                                    └── 6. 异步压缩（Compressor.MaybeCompress）
                                           ├── L0≥40 → LLM压缩 → L1 EpisodeSummary → 删原文
                                           └── L1≥10 → LLM聚合 → L2 TopicCluster → 向量化 → 删旧摘要
```

## 管线一览

| 管线 ID | Pass 链 | 触发条件 |
|---------|---------|----------|
| pipeline.admin | CommandRouterPass → ExecuteCommandPass | `/admin` 开头 |
| pipeline.command | CommandRouterPass → ExecuteCommandPass | `/` 开头 |
| pipeline.intent | IntentPass → CommandRouterPass+ExecuteCommandPass / RoleplayPass / 忽略 | 自然语言（非 `/` 开头） |
| pipeline.fallback | FallbackPass | 超时降级 |

## 意图路由一览

| 意图类型 | 路由目标 | 说明 |
|---------|---------|------|
| intent=command | CommandRouterPass → ExecuteCommandPass | 自然语言触发斜杠命令 |
| intent=tool | RoleplayPass → ChatService（工具调用循环） | AI 自主调用注册的工具 |
| intent=chat | RoleplayPass → ChatService | 普通角色扮演对话 |
| intent=ignore | 静默忽略 | 不需要回复的消息 |
