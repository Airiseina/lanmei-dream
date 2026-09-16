# bot 包 —— 消息处理层

## 职责

接收网关标准化消息，交给 Conduit 引擎（行为树 + 管线）路由，并通过网关回复。

## 消息入口

`OnMessage`（实现 `gateway.EventHandler`）完成空文本过滤、`message_id` 去重（`Deduper`，Redis，故障时放行）与事件上下文注入（`Extra`，含多模态段与 notice 信息），随后 `engine.Submit` 异步入队，结果由 `ResponseCallback` 处理：

- 正常结束 → `flushOutput` 按出站段或纯文本回复
- `conduit.IsYielded` → 走 `streamSegments` 逐段投递（模拟打字节奏）
- 返回错误 → 统一兜底话术

异步提交是为了不阻塞网关读循环。notice / request 事件无文本内容，事件信息写入黑板供插件消费，本身不产生回复。

## 行为树

```
Selector [
  插件子树（优先级最高）
  流式段落重入   IsSegment
  互动事件       IsNotice
  管理员命令     IsAdminCommand
  斜杠命令       IsCommand
  多媒体         IsMedia
  意图分析（兜底）
]
```

流式段落优先于命令与意图：段落是已生成的回复内容，直接交付、不再走意图分析。

## 管线

| 管线 | Pass 链 | 说明 |
|---|---|---|
| pipeline.admin | AdminGuardPass → CommandPass | 先校验超管身份，非超管拦截不执行 |
| pipeline.command | CommandRouterPass → ExecuteCommandPass | 斜杠命令解析与执行 |
| pipeline.intent_analysis | IntentAnalysisPass | LLM 意图分类，RouterPass 按结果动态路由 |
| pipeline.roleplay | RoleplayStreamPass | 角色扮演流式对话（LLM 未配置时不注册该管线） |
| pipeline.roleplay_segment | RoleplaySegmentPass | 流式段落作为子消息重入后的交付 |
| pipeline.intent_command_exec | IntentCommandExecPass | 自然语言命中命令后执行 |
| pipeline.intent_ignore | IntentIgnorePass | 意图为 ignore：只保存对话历史，不回复 |
| pipeline.topic_gate | TopicGatePass | 群聊「是否该接话」决策，RouterPass |
| pipeline.topic_ignore | TopicIgnorePass | 话题门控判定不接话 |
| pipeline.media | MediaPass → MediaRouterPass | 多媒体段理解与缓存，再路由到意图分析 |
| pipeline.notice | NoticeGatePass | 互动事件（notice / request）分发 |
| pipeline.fallback | FallbackPass | 超时或未匹配时的兜底回复 |

核心管线以「动态管线」（PassID 引用）注册：管理面板可编辑 Pass 顺序，替换 Pass 时引擎自动失效对应管线缓存。

## 条件函数

| 函数 | 逻辑 |
|---|---|
| IsAdminCommand | 消息以 `/admin` 开头 |
| IsCommand | 消息以 `/` 开头 |
| IsSegment | 流式段落重入消息（读 `ctx.Extra`，不能用 `conduit.Get`） |
| IsNotice | notice / request 事件 |
| IsMedia | 消息含 image / audio / video / file / record 段 |
| IsSuperUserFromCtx | 发送者在超管集合中（静态配置与动态管理员合并） |

## 关键依赖

- `github.com/zrurf/conduit` — 引擎、行为树、管线
- `internal/gateway` — 反向 WS 服务端 + OneBot 协议适配
- `internal/ai` — ChatService、IntentAnalyzer、VisionService
- `internal/command` — 斜杠命令系统
- `internal/plugin` — 插件注册表（插件子树挂载到行为树）
- `internal/topic` — 群聊话题系统
- `internal/database` — 对话、记忆与管理员持久化
