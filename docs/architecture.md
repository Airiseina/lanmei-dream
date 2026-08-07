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
        Infra[infra.Setup<br/>PG+Redis+pgvector+Logger]
        Gateway[gateway.Server<br/>反向WS网关<br/>OneBot 11/12]
        Bot[bot.Bot]
        Engine[Conduit Engine]
        BT[行为树]
        CmdSys[command.System]
    end

    subgraph 管线 Pass
        CRPass[CommandRouterPass<br/>命令路由]
        ECPass[ExecuteCommandPass<br/>命令执行]
        IntentPass[IntentPass<br/>意图分析]
        RPPass[RoleplayPass]
        FBPass[FallbackPass]
    end

    subgraph AI 层
        Intent[ai.IntentAnalyzer<br/>LLM 意图判断]
        ChatSvc[ai.ChatService<br/>含工具调用循环]
        Comp[ai.Compressor<br/>LOD 压缩]
        LLM[ai.LLMClient<br/>Eino + OpenAI 兼容]
        Emb[ai.Embedder<br/>Eino + OpenAI Embedding]
        ToolReg[ai.tool.Registry<br/>AI 工具注册表]
    end

    subgraph 插件系统
        BizPlugin[bizplugin<br/>内置业务插件]
        WasmMgmt[WasmManager<br/>WASM 插件管理]
        WasmRT[WasmRuntime<br/>Extism 运行时]
        WasmPlugin[WasmPlugin<br/>Plugin 接口适配器]
    end

    subgraph 插件设施
        StateFac[StateStore<br/>状态存储+原子操作]
        DBFac[DB Access<br/>IndexedDB 隔离模型]
        HTTPFac[HTTP Access<br/>fetch-like 受限客户端]
    end

    subgraph 安全层
        Casbin[Casbin RBAC]
        Cap[Capability<br/>Permission+Scope]
        ScopeChk[ScopeChecker<br/>运行时范围检查]
        Quota[ResourceQuota<br/>资源配额]
        Audit[AuditLogger<br/>zap 审计日志]
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
    BT -->|/admin| CRPass
    BT -->|/命令| CRPass
    CRPass --> ECPass
    BT -->|自然语言| IntentPass
    IntentPass -->|intent=command| CRPass
    IntentPass -->|intent=tool| RPPass
    IntentPass -->|intent=chat| RPPass
    IntentPass -->|intent=ignore| 忽略
    IntentPass --> Intent
    Intent --> LLM
    ECPass --> CmdSys
    RPPass --> ChatSvc
    ChatSvc --> LLM
    ChatSvc --> Emb
    ChatSvc --> PG
    ChatSvc --> Comp
    ChatSvc --> ToolReg
    ToolReg --> BizPlugin
    ToolReg --> WasmPlugin
    Comp --> LLM
    Comp --> Emb
    Comp --> PG
    RPPass --> PG
    ECPass --> PG
    ChatSvc -.->|pgvector| PG
    Engine -->|StateStore| RD
    Main --> Infra
    Infra --> PG
    Infra --> RD
    Infra -->|InitLogger| Audit
    WasmMgmt --> WasmRT
    WasmRT --> WasmPlugin
    BizPlugin --> StateFac
    BizPlugin --> DBFac
    WasmPlugin --> StateFac
    WasmPlugin --> DBFac
    WasmPlugin --> HTTPFac
    StateFac --> ScopeChk
    DBFac --> ScopeChk
    HTTPFac --> ScopeChk
    ScopeChk --> Cap
    Cap --> Casbin
    WasmPlugin --> Quota
    StateFac --> Audit
    DBFac --> Audit
    HTTPFac --> Audit
```

## 消息处理流程

```mermaid
sequenceDiagram
    participant U as 用户
    participant GW as Gateway网关
    participant B as bot.Bot
    participant E as Conduit Engine
    participant BT as 行为树
    participant CR as CommandRouterPass
    participant EC as ExecuteCommandPass
    participant IP as IntentPass
    participant RP as RoleplayPass
    participant FB as FallbackPass
    participant IA as IntentAnalyzer
    participant A as ai.ChatService
    participant TR as tool.Registry
    participant P as PostgreSQL+pgvector
    participant R as Redis

    U->>GW: 发消息
    GW->>B: NormalizedMessage
    B->>E: Process(InputMessage)

    E->>BT: Tick(MessageContext)

    alt /admin 开头
        BT-->>E: pipeline.admin
        E->>CR: Execute（路由命令）
        CR->>EC: Execute（执行命令）
        EC->>P: 操作数据库
        EC-->>U: 命令结果
    else / 开头（普通命令）
        BT-->>E: pipeline.command
        E->>CR: Execute（路由命令）
        CR->>EC: Execute（执行命令）
        EC-->>U: 命令结果
    else 自然语言
        BT-->>E: pipeline.intent
        E->>IP: Execute
        IP->>IA: Analyze(用户消息)
        IA->>IA: LLM 意图分类

        alt intent=command
            IA-->>IP: {intent:"command", command:"签到"}
            IP->>CR: routeToCommand
            CR->>EC: Execute
            EC-->>U: 命令结果
        else intent=tool
            IA-->>IP: {intent:"tool"}
            IP->>RP: routeToChat（AI 自主调用工具）
            RP->>A: Chat(当前消息, userID)
            A->>A: 工具调用循环（max 5 轮）
            A->>TR: processToolCalls
            TR-->>A: 工具结果
            A-->>RP: response
            RP-->>U: 回复内容
        else intent=chat
            IA-->>IP: {intent:"chat"}
            IP->>RP: routeToChat
            RP->>P: GetOrCreateUser
            RP->>A: Chat(当前消息, userID)
            A->>A: LOD组装 + RAG检索 + 工具调用循环
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
    IP -->|intent=command| CR[CommandRouterPass]
    CR --> EC[ExecuteCommandPass]
    IP -->|intent=tool| RP[RoleplayPass<br/>AI 工具调用]
    IP -->|intent=chat| RP2[RoleplayPass<br/>角色扮演]
    IP -->|intent=ignore| SKIP[静默忽略]
```

优先级从上到下：管理员命令 > 普通命令 > 意图分析（自然语言路由）

## 插件系统架构

```mermaid
graph TB
    subgraph 内置插件
        BizSignin[bizplugin/signin<br/>Go 原生]
    end

    subgraph WASM 插件
        WasmInstall[WasmManager.Install<br/>远程下载/本地加载]
        WasmLoad[WasmManager.Load<br/>Manifest 解析]
        WasmStart[WasmManager.Start<br/>Extism 运行时启动]
        WasmStop[WasmManager.Stop<br/>Extism 插件关闭]
        WasmUninstall[WasmManager.Uninstall<br/>资源清理+删除]
    end

    subgraph 插件注册
        Reg[plugin.Registry<br/>生命周期管理]
        ToolReg2[ai.tool.Registry<br/>AI 工具注册]
        CmdSys2[command.System<br/>斜杠命令注册]
    end

    subgraph 插件生命周期
        direction LR
        L1[Register] --> L2[Init] --> L3[Start] --> L4[Stop]
    end

    BizSignin --> Reg
    WasmStart --> Reg
    Reg -->|注册命令| CmdSys2
    Reg -->|注册工具| ToolReg2

    WasmInstall --> WasmLoad --> WasmStart --> WasmStop --> WasmUninstall
```

## 插件设施与安全模型

```mermaid
graph LR
    subgraph 插件设施
        StateFac2[StateStore<br/>get/set/delete<br/>CAS/IncrBy/SetNX]
        DBFac2[DB Access<br/>IndexedDB 隔离模型<br/>表名校验防注入]
        HTTPFac2[HTTP Access<br/>GET/POST<br/>受 allow_hosts 约束]
    end

    subgraph 安全检查链
        Perm[Permission<br/>state:read/write<br/>db:read/write<br/>http:get/post]
        Scope[Scope<br/>key_prefix<br/>tables<br/>allow_hosts]
        Cap2[Capability<br/>授权声明]
        RBAC[Casbin RBAC<br/>角色→权限映射]
        Quota2[ResourceQuota<br/>内存/CPU/速率/并发]
    end

    subgraph 审计
        Audit2[AuditLogger<br/>zap 结构化<br/>allow/deny 记录]
    end

    StateFac2 --> Perm
    DBFac2 --> Perm
    HTTPFac2 --> Perm
    Perm --> Scope
    Scope --> Cap2
    Cap2 --> RBAC
    Perm --> Quota2
    Perm --> Audit2
    Scope --> Audit2
```

## AI 工具调用流程

```mermaid
sequenceDiagram
    participant CS as ChatService
    participant LLM as EinoClient
    participant TR as tool.Registry
    participant Plugin as 插件 Handler

    CS->>LLM: ChatWithTools(toolInfos)
    LLM-->>CS: 带工具的模型实例
    CS->>LLM: Generate(messages)
    LLM-->>CS: 响应（含 ToolCalls?）

    loop 最多 5 轮工具调用
        alt 响应含 ToolCalls
            CS->>TR: Call(toolName, argsJSON)
            TR->>Plugin: Handler(ctx, argsJSON)
            Plugin-->>TR: 结果
            TR-->>CS: 工具结果字符串
            CS->>CS: 追加 Tool 消息
            CS->>LLM: Generate(messages + 工具结果)
            LLM-->>CS: 新响应
        else 无 ToolCalls
            CS-->>CS: 返回最终回复
        end
    end
```

## 基础设施层

```mermaid
graph LR
    Main[main.go] --> Infra[infra.Setup]
    Infra --> PG[PostgreSQL 17<br/>pgvector/pgvector:pg17]
    Infra --> RD[Redis 7<br/>redis:7-alpine<br/>64MB + allkeys-lru]
    Infra --> PV[PGVectorStore<br/>实现 MemoryStore 接口]
    Infra --> RS[RedisStore<br/>实现 StateStore 接口]
    Infra --> Logger[zap Logger<br/>lumberjack 轮转]
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

## 已实现 / 规划中

- [x] LLMClient 实现（Eino 框架 + eino-ext OpenAI 兼容层，支持 ToolCalling）
- [x] Embedder 实现（Eino 框架 + eino-ext OpenAI Embedding）
- [x] Function Calling 自然语言命令路由（IntentPass，4 种意图：command/tool/chat/ignore）
- [x] AI 工具注册表（tool.Registry，基于 Eino schema.ToolInfo + ToolCallingChatModel）
- [x] 插件系统（内置 bizplugin + WASM Extism 运行时）
- [x] 插件设施（StateStore 原子操作、DB Access IndexedDB 隔离、HTTP Access 受限客户端）
- [x] 安全模型（Casbin RBAC + Permission + Scope + Capability + ResourceQuota）
- [x] 审计日志（zap 结构化 AuditLogger）
- [x] 统一日志（zap + lumberjack 轮转，替换 log.Printf/slog）
- [x] Command 管线优化（CommandRouterPass + ExecuteCommandPass 分离）
- [x] WIT 接口定义（schema/plugin/lanmei-plugin.wit，含 db/http Host Functions）
- [x] LOD 记忆压缩（L0 原文 → L1 摘要 → L2 主题）
- [x] 多路召回（向量 + 关键词 + 时间，memory.MultiRetriever 加权合并）
- [ ] 签到记录表
- [ ] 状态面板前端
