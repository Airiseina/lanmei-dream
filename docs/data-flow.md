# 数据流说明

## 消息处理流

```
用户消息 → QQ/LLOneBot → WebSocket → bot.handleMessage
  │
  ├─ 转换为 conduit.InputMessage
  └─ engine.Process(input) → 同步拿到结果
       │
       ├─ 行为树决策
       │    ├─ /admin → pipeline.admin
       │    ├─ /xxx   → pipeline.command
       │    └─ 其余   → pipeline.intent（意图分析）
       │
       ├─ 执行管线 Pass
       │    ├─ CommandPass → command.System.Process → Handler
       │    ├─ IntentPass → IntentAnalyzer.Analyze
       │    │    ├─ intent=command → CommandPass（复用命令处理）
       │    │    ├─ intent=chat    → RoleplayPass → ChatService.Chat → 回复
       │    │    └─ intent=ignore  → 静默忽略
       │    └─ FallbackPass → 兜底回复
       │
       └─ OnResponse 回调（输出消息/记录错误）
```

## 意图分析流

```
自然语言消息 → IntentPass.Execute
  │
  ├─ IntentAnalyzer.Analyze(ctx, msg)
  │    ├─ LLM 可用 → 构建 prompt（可用命令列表 + 用户消息）→ LLM 分类 → JSON 解析
  │    └─ LLM 不可用 → 降级返回 {intent:"chat", confidence:1.0}
  │
  ├─ 结果路由
  │    ├─ intent="command", command="签到" → ctx.RawMsg = "/签到" → CommandPass.Execute
  │    ├─ intent="chat"  → RoleplayPass.Execute
  │    └─ intent="ignore" → return nil（静默）
  │
  └─ 错误降级：分析失败 → 走 RoleplayPass（与之前行为一致）
```

## Conduit 引擎配置

```
Engine
  ├─ Workers: 4
  ├─ Timeout: 10s
  ├─ Fallback: pipeline.fallback
  ├─ StateStore: RedisStore（Redis，TTL 自动过期 + LRU 淘汰）
  └─ BehaviorTree: Selector[admin → command → intent]
```

## 基础设施流

```
main.go
  │
  └─ infra.Setup(ctx, cfg)
       ├─ PostgreSQL 连接 + GORM 迁移 + pgvector 扩展启用 + HNSW 索引
       ├─ Redis 连接（64MB 上限 + allkeys-lru）
       ├─ PGVectorStore（实现 MemoryStore 接口）→ inf.MemStore
       └─ RedisStore（实现 StateStore 接口）→ inf.StateStore
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
                                    ├── 3. 拼提示 → LLM → 回复
                                    │
                                    ├── 4. 异步存记忆（pgvector）
                                    │
                                    └── 5. 异步压缩（Compressor.MaybeCompress）
                                           ├── L0≥40 → LLM压缩 → L1 EpisodeSummary → 删原文
                                           └── L1≥10 → LLM聚合 → L2 TopicCluster → 向量化 → 删旧摘要
```

## 管线一览

| 管线 ID | Pass 链 | 触发条件 |
|---------|---------|----------|
| pipeline.admin | CommandPass | `/admin` 开头 |
| pipeline.command | CommandPass | `/` 开头 |
| pipeline.intent | IntentPass → CommandPass/RoleplayPass/忽略 | 自然语言（非 `/` 开头） |
| pipeline.fallback | FallbackPass | 超时降级 |
