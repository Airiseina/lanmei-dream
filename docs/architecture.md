# 蓝妹架构图

## 系统总览

```mermaid
graph TB
    subgraph 外部
        IM[IM 平台<br/>微信/钉钉/TG/...]
        Onebots[Onebots 网关]
        NapCat[NapCat<br/>NTQQ Provider]
    end

    subgraph 蓝妹服务
        Main[main.go 入口]
        Infra[infra.Setup<br/>PG+Redis+pgvector]
        Gateway[gateway.Server<br/>反向WS网关]
        Bot[bot.Bot]
        Engine[Conduit Engine]
        BT[行为树]
        CmdSys[command.System]
    end

    subgraph 管线 Pass
        CmdPass[CommandPass]
        IntentPass[IntentPass<br/>意图分析]
        RPPass[RoleplayPass]
        FBPass[FallbackPass]
    end

    subgraph AI 层
        Intent[ai.IntentAnalyzer<br/>LLM 意图判断]
        ChatSvc[ai.ChatService]
        Comp[ai.Compressor<br/>LOD 压缩]
        LLM[ai.LLMClient<br/>⚠ 待实现]
        Emb[ai.Embedder<br/>⚠ 待实现]
    end

    subgraph 存储层
        PG[(PostgreSQL 17<br/>+ pgvector)]
        RD[(Redis 7)]
    end

    IM -->|消息| Onebots
    Onebots -->|反向WS OneBot12| Gateway
    NapCat -->|反向WS OneBot11| Gateway
    Gateway -->|NormalizedMessage| Bot
    Bot -->|Process| Engine
    Engine -->|决策| BT
    BT -->|/admin| CmdPass
    BT -->|/命令| CmdPass
    BT -->|自然语言| IntentPass
    IntentPass -->|intent=command| CmdPass
    IntentPass -->|intent=chat| RPPass
    IntentPass -->|intent=ignore| 忽略
    IntentPass --> Intent
    Intent --> LLM
    CmdPass --> CmdSys
    RPPass --> ChatSvc
    ChatSvc --> LLM
    ChatSvc --> Emb
    ChatSvc --> PG
    ChatSvc --> Comp
    Comp --> LLM
    Comp --> Emb
    Comp --> PG
    RPPass --> PG
    CmdPass --> PG
    ChatSvc -.->|pgvector| PG
    Engine -->|StateStore| RD
    Main --> Infra
    Infra --> PG
    Infra --> RD
```

## 消息处理流程

```mermaid
sequenceDiagram
    participant U as 用户
    participant GW as Gateway网关
    participant B as bot.Bot
    participant E as Conduit Engine
    participant BT as 行为树
    participant CP as CommandPass
    participant IP as IntentPass
    participant RP as RoleplayPass
    participant FB as FallbackPass
    participant IA as IntentAnalyzer
    participant A as ai.ChatService
    participant P as PostgreSQL+pgvector
    participant R as Redis

    U->>GW: 发消息
    GW->>B: NormalizedMessage
    B->>E: Process(InputMessage)

    E->>BT: Tick(MessageContext)

    alt /admin 开头
        BT-->>E: pipeline.admin
        E->>CP: Execute
        CP->>P: 操作数据库
        CP-->>U: 命令结果
    else / 开头（普通命令）
        BT-->>E: pipeline.command
        E->>CP: Execute
        CP-->>U: 命令结果
    else 自然语言
        BT-->>E: pipeline.intent
        E->>IP: Execute
        IP->>IA: Analyze(用户消息)
        IA->>IA: LLM 意图分类

        alt intent=command
            IA-->>IP: {intent:"command", command:"签到"}
            IP->>CP: routeToCommand
            CP-->>U: 命令结果
        else intent=chat
            IA-->>IP: {intent:"chat"}
            IP->>RP: routeToChat
            RP->>P: GetOrCreateUser
            RP->>A: Chat(当前消息, userID)
            A->>A: LOD组装(L2→L1→L0) + RAG检索(pgvector)
            A->>A: Embed → pgvector检索 → 拼提示 → LLM
            A-->>RP: response
            RP->>P: SaveConversation (L0)
            A-.->Comp: MaybeCompress (异步)
            Comp-.->LLM: L0→L1 / L1→L2 压缩
            RP-->>U: 回复内容
        else intent=ignore
            IA-->>IP: {intent:"ignore"}
            IP-->>U: 静默
        end

    else 超时/异常
        BT-->>E: pipeline.fallback
        E->>FB: Execute
        FB-->>U: 兜底回复
    end
```

## 行为树结构

```mermaid
graph TD
    Root[Selector 根节点]
    Root --> S1[Sequence: 管理员命令]
    Root --> S2[Sequence: 普通命令]
    Root --> A1[Action: pipeline.intent]

    S1 --> C1[Condition: IsAdminCommand]
    S1 --> A2[Action: pipeline.admin]

    S2 --> C2[Condition: IsCommand]
    S2 --> A3[Action: pipeline.command]

    A1 --> IP[IntentPass]
    IP -->|intent=command| CMD[CommandPass]
    IP -->|intent=chat| RP[RoleplayPass]
    IP -->|intent=ignore| SKIP[静默忽略]
```

优先级从上到下：管理员命令 > 普通命令 > 意图分析（自然语言路由）

## 基础设施层

```mermaid
graph LR
    Main[main.go] --> Infra[infra.Setup]
    Infra --> PG[PostgreSQL 17<br/>pgvector/pgvector:pg17]
    Infra --> RD[Redis 7<br/>redis:7-alpine<br/>64MB + allkeys-lru]
    Infra --> PV[PGVectorStore<br/>实现 MemoryStore 接口]
    Infra --> RS[RedisStore<br/>实现 StateStore 接口]
    PG --> PV
    RD --> RS
```

## 存储层设计

```mermaid
erDiagram
    users {
        bigserial id PK
        varchar platform UK
        varchar platform_user_id UK
        varchar nickname
        timestamptz created_at
        timestamptz updated_at
    }
    conversations {
        bigserial id PK
        bigint user_id FK
        varchar role
        text content
        timestamptz created_at
    }
    episode_summaries {
        bigserial id PK
        bigint user_id FK
        text brief "一句话总结"
        text detailed "详细摘要"
        jsonb facts "结构化事实"
        int covered_count "压缩的原文条数"
        bigint first_convo_id
        bigint last_convo_id
        timestamptz created_at
    }
    topic_clusters {
        bigserial id PK
        bigint user_id FK
        varchar topic "主题名称"
        text brief "一句话主题总结"
        text detailed "详细主题描述"
        jsonb facts "聚合关键事实"
        int covered_count "聚合的episode条数"
        timestamptz created_at
        timestamptz updated_at
    }
    memories {
        bigserial id PK
        bigint user_id FK
        text content
        jsonb metadata
        timestamptz created_at
    }
    memory_vectors {
        bigserial id PK
        bigint user_id FK
        text content "记忆文本"
        vector_1024 embedding "pgvector 向量"
        timestamptz created_at
    }
    conduit_states {
        varchar key PK
        text value
        timestamptz expires_at
    }
    users ||--o{ conversations : "L0 原文"
    users ||--o{ episode_summaries : "L1 摘要"
    users ||--o{ topic_clusters : "L2 主题"
    users ||--o{ memories : "has"
    users ||--o{ memory_vectors : "向量记忆"
```

## 待实现 / 规划中

- [ ] LLMClient 具体实现（等用户指定 provider）
- [ ] Embedder 具体实现（等用户指定 provider）
- [x] Function Calling 自然语言命令路由（IntentPass）— 骨架已搭好，LLM 接入后自动生效
- [ ] 多路召回（向量 + 关键词 + 时间）
- [x] LOD 记忆压缩（L0 原文 → L1 摘要 → L2 主题）
- [ ] 签到记录表
- [ ] 状态面板前端
