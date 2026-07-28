# CLAUDE.md — 项目规则（AI 助手必读）

## 项目概况

蓝妹（lanmei-dream）是一个跨平台聊天机器人，通过反向 WebSocket 网关接入 OneBot 协议（v12/v11）的 IM 平台。支持通过 Onebots 网关连接微信、钉钉、Telegram 等，也支持 NapCat 直连 NTQQ。消息路由使用 Conduit（行为树 + 管线架构）。

## 技术栈

- **语言**: Go
- **网关**: internal/gateway（基于 lxzan/gws 的反向 WS 服务端，OneBot 12/11 协议适配）
- **消息引擎**: github.com/zrurf/conduit（行为树 + 管线）
- **数据库**: PostgreSQL 17 + pgvector（GORM ORM + pgx 驱动，参数化查询防注入）
- **向量搜索**: pgvector 扩展（HNSW 索引，替代 Milvus）
- **状态缓存**: Redis 7（Conduit StateStore 持久化 + TTL 自动回收）
- **通信**: 反向 WebSocket（Onebots/NapCat → 蓝妹网关）
- **LLM / Embedding**: 待定（接口已定义在 internal/ai/llm/ 和 internal/ai/embedding/，具体实现等用户指定）

## 目录结构

```
cmd/lanmei/          → 入口 main.go
internal/
  ai/                → ChatService、Compressor、提示词
    intent/          → IntentAnalyzer 意图分析（LLM 判断消息路由）
    llm/             → LLMClient 接口 + ChatRequest/ChatResponse/Role/Message
    embedding/       → Embedder 接口
    memory/          → MemoryStore 接口（仅接口，实现移至 infra/）
  bot/               → Conduit 引擎 + 行为树 + Pass 实现 + EventHandler
  command/           → 斜杠命令系统（注册/解析/分发）
  database/          → GORM 连接池、迁移、CRUD（user/conversation/lod），所有查询参数化
  gateway/           → 反向 WS 服务端 + OneBot 12/11 协议适配 + 连接管理
  infra/             → 基础设施统一管理（PostgreSQL + Redis + pgvector），生命周期 Setup/Close
  model/             → 数据模型（User/Conversation/Memory/EpisodeSummary/TopicCluster/MemoryVector/ConduitState）
  plugin/            → Wasm 插件系统（注册表、ABI、宿主函数、权限控制）
  bizplugin/         → 内置业务插件（签到）
docs/                → 架构图（drawio + markdown）
```

## 约定

- **语言**: 所有代码注释、对话、commit message 用中文
- **数据库表名/字段**: 英文蛇形（snake_case）
- **Go 导出符号**: 大驼峰
- **错误处理**: fmt.Errorf 带 %w 包装，log.Printf 记录，不吞错
- **SQL 安全**: GORM 参数化查询，绝不拼接用户输入到 SQL 字符串
- **依赖注入**: 构造函数注入（NewXxx），不用全局变量
- **消息路由**: 行为树决策 → 管线执行，不写 if-else 面条
- **新增功能**: 加 Pass 实现 conduit.Pass 接口，注册到管线
- **基础设施**: 新增基础设施（Redis、pgvector 等）统一放入 internal/infra/，通过 infra.Setup() 初始化
- **向量存储**: 使用 pgvector（PostgreSQL 扩展），不使用独立向量数据库
- **状态存储**: Conduit StateStore 使用 Redis（TTL 自动过期 + LRU 淘汰）
- **平台标识**: 用户以 (platform, platform_user_id) 唯一标识，platform 为 qq/wechat/telegram/napcat 等
- **权限主体**: user::<platform>::<platformUserID>，如 user::qq::123456

## 关键设计决策

- 消息路由由 Conduit 引擎驱动：行为树决策走哪条管线，管线内 Pass 链式执行
- 行为树优先级：管理员命令 > 普通命令 > 意图分析（自然语言路由）
- 五条管线：pipeline.admin / pipeline.command / pipeline.intent / pipeline.chat / pipeline.fallback
- 意图分析（IntentPass）：非 / 消息先由 LLM 判断意图 → command / chat / ignore
- 显式 / 命令保留为快捷入口，IntentPass 只处理自然语言消息
- LLM 未配置时 IntentPass 降级为全部走 chat，与之前行为一致
- 角色扮演流程：LOD 多级上下文组装 → RAG 检索记忆 → 拼提示 → LLM → 存对话+记忆 → 异步压缩
- LOD 记忆压缩：L0（原始对话）→ L1（EpisodeSummary: brief+detailed+facts）→ L2（TopicCluster: brief+detailed+facts+pgvector向量）
- 压缩由 LLM 驱动：读旧对话/摘要 → 生成 brief+detailed+结构化事实 → 替换原文
- 压缩阈值：L0≥40条触发L0→L1，L1≥10条触发L1→L2
- 上下文组装按 token 预算分配：L2→L1→L0 优先级填充
- LLMClient / Embedder 是接口，无具体实现，角色扮演因此暂不可用
- 基础设施层（infra）统一管理所有外部连接（PG + Redis + pgvector），提供 Setup/Close 生命周期
- 向量存储从 Milvus 切换到 pgvector，减少 3 个容器（etcd + minio + milvus），统一到 PostgreSQL
- 网关层（gateway）提供反向 WS 服务端，Onebots/NapCat 作为 WS 客户端主动连接
- OneBot 12 端点：/onebot/v12，OneBot 11 端点：/onebot/v11（NapCat 兼容），通用端点：/onebot
- NapCat 连接到 /onebot/v11 或通过 ?platform=napcat 查询参数标识

## 环境变量

| 变量 | 默认值 | 说明 |
|------|--------|------|
| DATABASE_URL | postgres://lanmei:lanmei@localhost:5432/lanmei?sslmode=disable | PostgreSQL 连接串（含 pgvector 扩展） |
| REDIS_ADDR | localhost:6379 | Redis 地址（Conduit StateStore） |
| EMBEDDING_DIM | 1024 | 向量维度（需与 Embedding 模型匹配） |
| BOT_GATEWAY_LISTEN_ADDR | 0.0.0.0:8080 | 网关监听地址（反向 WS） |
| BOT_GATEWAY_ACCESS_TOKEN | （空） | 网关鉴权 token |
| BOT_NICKNAME | 蓝妹 | 机器人昵称 |
| BOT_SUPER_USERS | （空） | 超级用户，格式：platform:userID,逗号分隔，如 qq:123456,wechat:wxid_xxx |

> **本文档状态：当前有效** — 架构变更时须同步更新此文件。

## 变更记录

- 2026-07-28：移除 ZeroBot/QQ 强依赖，实现跨平台 OneBot 12/11 网关层（internal/gateway/），基于 lxzan/gws 反向 WS 服务端；用户模型从 QQID 迁移到 (Platform, PlatformUserID)；SuperUsers 格式改为 platform:userID；docker-compose 新增 onebots/napcat 服务
- 2026-07-25：新增 IntentPass 意图分析管线，自然语言消息由 LLM 判断路由（command/chat/ignore），LLM 未配置时降级为 chat；行为树优先级3从 pipeline.chat 改为 pipeline.intent
- 2026-07-25：向量存储从 Milvus 切换到 pgvector（PG 扩展 + HNSW 索引），删除 Milvus/etcd/minio 三容器；新增 model.MemoryVector 和 infra/pgvector.go
- 2026-07-25：新增 internal/infra/ 包统一管理基础设施（PG + Redis + pgvector），Setup/Close 生命周期；Conduit StateStore 从 MemoryStore 切换到 Redis（RedisStore），TTL 自动过期 + LRU 淘汰
- 2026-07-22：数据库层从 pgxpool 迁移到 GORM，参数化查询防注入，AutoMigrate 替代原始 SQL
- 2026-07-22：拆包重构：ai/ → ai/ + ai/llm/ + ai/embedding/ + ai/memory/；database/ 拆分为 user/conversation/lod；plugin/signin → signin/；删除空 plugin/ 目录
- 2026-07-22：引入 LOD 记忆压缩系统（L0→L1→L2），LLM 驱动 brief/detailed/facts 双粒度压缩，token 预算上下文组装
- 2026-07-22：引入 Conduit 消息引擎（行为树 + 管线），替代 if-else 路由；roleplay 逻辑合并进 RoleplayPass
- 2026-07-22：初始版本，PostgreSQL + Milvus + ZeroBot，LLM/Embedding 待定
