# bot 包 —— 消息处理层

## 职责

负责接收网关标准化消息并分发，使用 Conduit 引擎（行为树 + 管线）路由消息，通过网关回复。

## 核心设计

### 消息路由（行为树）

所有用户消息通过 Conduit 引擎处理，行为树按优先级路由：

```
Gateway 收到 NormalizedMessage → engine.Process(InputMessage)
        │
        ▼
   行为树决策
        │
        ├─ /admin 开头 → pipeline.admin → CommandPass
        ├─ /xxx 开头   → pipeline.command → CommandPass
        └─ 自然语言    → pipeline.intent → IntentPass
                              │
                              ├─ intent=command → CommandPass
                              ├─ intent=chat    → RoleplayPass
                              └─ intent=ignore  → 静默
```

### Pass 列表

| Pass | 文件 | 职责 |
|------|------|------|
| CommandPass | passes.go | 执行斜杠命令 |
| RoleplayPass | passes.go | AI 角色扮演对话 |
| IntentPass | intent_pass.go | LLM 意图分析 → 路由到 Command/Roleplay/忽略 |
| FallbackPass | passes.go | 超时/异常兜底回复 |

### 条件判断

| 函数 | 逻辑 |
|------|------|
| IsAdminCommand | 消息以 `/admin` 开头 |
| IsCommand | 消息以 `/` 开头 |

### 关键依赖

- `github.com/zrurf/conduit` — 引擎、行为树、管线
- `internal/gateway` — 网关（反向 WS 服务端 + OneBot 协议适配）
- `internal/ai` — ChatService、IntentAnalyzer
- `internal/command` — 命令系统
- `internal/database` — 数据库访问
