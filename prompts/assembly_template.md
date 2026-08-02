{{ fragment "system_static" }}

{{ fragment "system_persona" }}

{{ fragment "system_safety" }}

{{ fragment "system_semi" }}

{{ .Skills }}

## 当前上下文
- 当前时间：{{ .CurrentTime }}
- 用户昵称：{{ .UserName }}
- 当前群组：{{ .GroupName }}

---

{{ .Conversation }}