// 各模块 API 函数（路径与后端 internal/manager/server.go 路由一一对应）
import { request, openSSE } from './client'
import type {
  Admin,
  AuthSession,
  CommandView,
  ConfigRevision,
  ConduitSnapshot,
  ConduitTrace,
  DashboardStats,
  GroupView,
  KnowledgeBaseView,
  KnowledgeChunk,
  LLMProvider,
  MeInfo,
  MemoryView,
  NodeTraffic,
  Page,
  Passkey,
  PendingTOTP,
  PluginView,
  PromptFragmentView,
  SessionResult,
  SkillView,
  StickerView,
  UsagePoint,
  UsageSummaryRow,
  UserView,
} from '@/types/api'

// ── 认证 ──
export interface LoginResult {
  pending_totp?: PendingTOTP
  admin_id?: number
  access_token?: string
  refresh_token?: string
}

export const authApi = {
  passwordLogin: (username: string, password: string) =>
    request<LoginResult>('/api/auth/password-login', {
      method: 'POST',
      body: { username, password },
      noRefresh: true,
    }),
  verifyTOTP: (pendingToken: string, code: string) =>
    request<SessionResult>('/api/auth/totp-verify', {
      method: 'POST',
      body: { pending_token: pendingToken, code },
      noRefresh: true,
    }),
  webauthnLoginBegin: (username: string) =>
    request<{ session_token: string; assertion: unknown }>('/api/auth/webauthn/begin-login', {
      method: 'POST',
      body: { username },
      noRefresh: true,
    }),
  webauthnLoginFinish: (sessionToken: string, username: string, body: unknown) =>
    request<SessionResult>(
      `/api/auth/webauthn/finish-login?session_token=${encodeURIComponent(sessionToken)}&username=${encodeURIComponent(username)}`,
      { method: 'POST', body, noRefresh: true },
    ),
  logout: (refreshToken: string) =>
    request<{ ok: boolean }>('/api/auth/logout', { method: 'POST', body: { refresh_token: refreshToken }, noRefresh: true }),
  me: () => request<MeInfo>('/api/auth/me'),
  stepUp: (password: string, totpCode: string) =>
    request<{ step_up_token: string; expires_in: number }>('/api/auth/step-up', {
      method: 'POST',
      body: { password, totp_code: totpCode },
    }),
  sessions: (adminId?: number) =>
    request<{ sessions: AuthSession[] }>(`/api/auth/sessions${adminId ? `?admin_id=${adminId}` : ''}`),
  revokeSession: (id: number) => request<{ ok: boolean }>(`/api/auth/sessions/${id}`, { method: 'DELETE' }),
  revokeAllSessions: (adminId?: number) =>
    request<{ ok: boolean }>(`/api/auth/sessions${adminId ? `?admin_id=${adminId}` : ''}`, { method: 'DELETE' }),
  changePassword: (oldPassword: string, newPassword: string, stepUpToken: string) =>
    request<{ ok: boolean }>('/api/auth/password', {
      method: 'POST',
      body: { old_password: oldPassword, new_password: newPassword },
      stepUpToken,
    }),
  totpSetupBegin: () => request<{ secret: string; otpauth_url: string }>('/api/auth/totp/setup-begin', { method: 'POST' }),
  totpSetupConfirm: (code: string) => request<{ ok: boolean }>('/api/auth/totp/setup-confirm', { method: 'POST', body: { code } }),
  totpRemove: (stepUpToken: string) => request<{ ok: boolean }>('/api/auth/totp', { method: 'DELETE', stepUpToken }),
  passkeys: () => request<{ passkeys: Passkey[] }>('/api/auth/passkeys'),
  passkeyRegisterBegin: () =>
    request<{ session_token: string; creation: unknown }>('/api/auth/webauthn/begin-register', { method: 'POST' }),
  passkeyRegisterFinish: (sessionToken: string, body: unknown) =>
    request<{ ok: boolean }>(`/api/auth/webauthn/finish-register?session_token=${encodeURIComponent(sessionToken)}`, {
      method: 'POST',
      body,
    }),
  passkeyRemove: (credentialId: string, stepUpToken: string) =>
    request<{ ok: boolean }>(`/api/auth/passkeys/${encodeURIComponent(credentialId)}`, { method: 'DELETE', stepUpToken }),
}

// ── 管理员 ──
export interface AdminForm {
  username: string
  password?: string
  role?: string
  display_name?: string
  status?: string
}

export const adminApi = {
  list: (page = 1, pageSize = 20) =>
    request<Page<Admin>>(`/api/admins?page=${page}&page_size=${pageSize}`),
  create: (form: AdminForm, stepUpToken: string) =>
    request<{ ok: boolean }>('/api/admins', { method: 'POST', body: form, stepUpToken }),
  update: (id: number, form: Partial<AdminForm>, stepUpToken: string) =>
    request<{ ok: boolean }>(`/api/admins/${id}`, { method: 'PUT', body: form, stepUpToken }),
  delete: (id: number, stepUpToken: string) =>
    request<{ ok: boolean }>(`/api/admins/${id}`, { method: 'DELETE', stepUpToken }),
  setStatus: (id: number, status: string, stepUpToken: string) =>
    request<{ ok: boolean }>(`/api/admins/${id}/status`, { method: 'PUT', body: { status }, stepUpToken }),
  resetPassword: (id: number, password: string, stepUpToken: string) =>
    request<{ ok: boolean }>(`/api/admins/${id}/password`, { method: 'PUT', body: { password }, stepUpToken }),
}

// ── LLM Provider 与用量 ──
export interface ProviderForm {
  name: string
  base_url: string
  api_key?: string
  model: string
  max_tokens?: number
  temperature?: number
  in_price_per_m?: number
  out_price_per_m?: number
  enabled?: boolean
  priority?: number
}

export const llmApi = {
  providers: () => request<{ items: LLMProvider[]; active: string }>('/api/llm/providers'),
  create: (form: ProviderForm, stepUpToken: string) =>
    request<{ ok: boolean }>('/api/llm/providers', { method: 'POST', body: form, stepUpToken }),
  update: (id: number, form: Partial<ProviderForm>, stepUpToken: string) =>
    request<{ ok: boolean }>(`/api/llm/providers/${id}`, { method: 'PUT', body: form, stepUpToken }),
  remove: (id: number, stepUpToken: string) =>
    request<{ ok: boolean }>(`/api/llm/providers/${id}`, { method: 'DELETE', stepUpToken }),
  activate: (id: number, stepUpToken: string) =>
    request<{ ok: boolean; active: string; model: string }>(`/api/llm/providers/${id}/activate`, {
      method: 'POST',
      stepUpToken,
    }),
  usageSummary: (by = 'model', since?: string, until?: string) =>
    request<{ items: UsageSummaryRow[] }>(`/api/llm/usage/summary?by=${by}${queryRange(since, until)}`),
  usageSeries: (step = 'hour', since?: string, until?: string) =>
    request<{ items: UsagePoint[] }>(`/api/llm/usage/series?step=${step}${queryRange(since, until)}`),
}

// ── Conduit 控制平面 ──
export const conduitApi = {
  snapshot: () => request<ConduitSnapshot>('/api/conduit/snapshot'),
  applyBehaviorTree: (node: unknown, comment: string, stepUpToken: string) =>
    request<{ snapshot: ConduitSnapshot }>('/api/conduit/behavior-tree', {
      method: 'PUT',
      body: { node, comment },
      stepUpToken,
    }),
  applySubtrees: (subtrees: { id: string; node: unknown }[], comment: string, stepUpToken: string) =>
    request<{ snapshot: ConduitSnapshot }>('/api/conduit/subtrees', {
      method: 'PUT',
      body: { subtrees, comment },
      stepUpToken,
    }),
  applyPipelines: (pipelines: unknown[], comment: string, stepUpToken: string) =>
    request<{ snapshot: ConduitSnapshot }>('/api/conduit/pipelines', {
      method: 'PUT',
      body: { pipelines, comment },
      stepUpToken,
    }),
  revisions: (page = 1, pageSize = 20) =>
    request<Page<ConfigRevision>>(`/api/conduit/revisions?page=${page}&page_size=${pageSize}`),
  rollback: (id: number, stepUpToken: string) =>
    request<{ snapshot: ConduitSnapshot }>(`/api/conduit/revisions/${id}/rollback`, { method: 'POST', stepUpToken }),
  traces: (params: { pipeline?: string; status?: string; groupId?: string; since?: string; page?: number; pageSize?: number }) => {
    const q = new URLSearchParams()
    if (params.pipeline) q.set('pipeline', params.pipeline)
    if (params.status) q.set('status', params.status)
    if (params.groupId) q.set('group_id', params.groupId)
    if (params.since) q.set('since', params.since)
    q.set('page', String(params.page ?? 1))
    q.set('page_size', String(params.pageSize ?? 20))
    return request<Page<ConduitTrace>>(`/api/conduit/traces?${q.toString()}`)
  },
  traffic: (pipeline?: string, node?: string, since?: string, until?: string) => {
    const q = new URLSearchParams()
    if (pipeline) q.set('pipeline', pipeline)
    if (node) q.set('node', node)
    if (since) q.set('since', since)
    if (until) q.set('until', until)
    return request<{ items: NodeTraffic[] }>(`/api/conduit/traffic?${q.toString()}`)
  },
  openTraceStream: (onEvent: (data: ConduitTrace) => void, onError?: (err: Error) => void) =>
    openSSE<ConduitTrace>('/api/conduit/traces/stream', (name, data) => {
      if (name === 'trace') onEvent(data)
    }, onError),
}

// ── 审计 ──
export const auditApi = {
  list: (params: { action?: string; username?: string; result?: string; since?: string; page?: number; pageSize?: number }) => {
    const q = new URLSearchParams()
    if (params.action) q.set('action', params.action)
    if (params.username) q.set('username', params.username)
    if (params.result) q.set('result', params.result)
    if (params.since) q.set('since', params.since)
    q.set('page', String(params.page ?? 1))
    q.set('page_size', String(params.pageSize ?? 20))
    return request<Page<AuditLogPageItem>>(`/api/audit-logs?${q.toString()}`)
  },
}

import type { AuditLog } from '@/types/api'
type AuditLogPageItem = AuditLog

// ── 仪表盘 ──
export const dashboardApi = {
  stats: () => request<DashboardStats>('/api/dashboard/stats'),
}

// ── 内容管理（M3） ──
export const contentApi = {
  // 群组
  groups: (keyword = '', page = 1, pageSize = 20) => {
    const q = new URLSearchParams({ page: String(page), page_size: String(pageSize) })
    if (keyword) q.set('keyword', keyword)
    return request<Page<GroupView>>(`/api/groups?${q.toString()}`)
  },
  groupConfig: (platform: string, groupId: string) =>
    request<GroupView>(`/api/groups/${encodeURIComponent(platform || 'all')}/${encodeURIComponent(groupId)}/config`),
  saveGroupConfig: (platform: string, groupId: string, form: Partial<GroupView>, stepUpToken: string) =>
    request<{ ok: boolean }>(`/api/groups/${encodeURIComponent(platform || 'all')}/${encodeURIComponent(groupId)}/config`, {
      method: 'PUT',
      body: form,
      stepUpToken,
    }),
  // 用户
  users: (keyword = '', page = 1, pageSize = 20) => {
    const q = new URLSearchParams({ page: String(page), page_size: String(pageSize) })
    if (keyword) q.set('keyword', keyword)
    return request<Page<UserView>>(`/api/users?${q.toString()}`)
  },
  setUserBan: (id: number, banned: boolean, reason: string, stepUpToken: string) =>
    request<{ ok: boolean }>(`/api/users/${id}/ban`, { method: 'POST', body: { banned, reason }, stepUpToken }),
  // 知识库
  knowledgeBases: () => request<Page<KnowledgeBaseView>>('/api/knowledge/bases'),
  knowledgeChunks: (params: { base?: string; keyword?: string; page?: number; pageSize?: number }) => {
    const q = new URLSearchParams()
    if (params.base) q.set('base', params.base)
    if (params.keyword) q.set('keyword', params.keyword)
    q.set('page', String(params.page ?? 1))
    q.set('page_size', String(params.pageSize ?? 20))
    return request<Page<KnowledgeChunk>>(`/api/knowledge/chunks?${q.toString()}`)
  },
  deleteKnowledgeChunk: (id: number, stepUpToken: string) =>
    request<{ ok: boolean }>(`/api/knowledge/chunks/${id}`, { method: 'DELETE', stepUpToken }),
  syncKnowledge: (base = '', stepUpToken: string) =>
    request<{ ok: boolean }>(`/api/knowledge/sync${base ? `?base=${encodeURIComponent(base)}` : ''}`, {
      method: 'POST',
      stepUpToken,
    }),
  // 记忆
  memories: (params: { userId?: string; groupId?: string; keyword?: string; page?: number; pageSize?: number }) => {
    const q = new URLSearchParams()
    if (params.userId) q.set('user_id', params.userId)
    if (params.groupId) q.set('group_id', params.groupId)
    if (params.keyword) q.set('keyword', params.keyword)
    q.set('page', String(params.page ?? 1))
    q.set('page_size', String(params.pageSize ?? 20))
    return request<Page<MemoryView>>(`/api/memories?${q.toString()}`)
  },
  deleteMemory: (id: number, stepUpToken: string) =>
    request<{ ok: boolean }>(`/api/memories/${id}`, { method: 'DELETE', stepUpToken }),
  // 插件
  plugins: () => request<Page<PluginView>>('/api/plugins'),
  enablePlugin: (id: string, stepUpToken: string) =>
    request<{ ok: boolean }>(`/api/plugins/${encodeURIComponent(id)}/enable`, { method: 'POST', stepUpToken }),
  disablePlugin: (id: string, stepUpToken: string) =>
    request<{ ok: boolean }>(`/api/plugins/${encodeURIComponent(id)}/disable`, { method: 'POST', stepUpToken }),
  deletePlugin: (id: string, stepUpToken: string) =>
    request<{ ok: boolean }>(`/api/plugins/${encodeURIComponent(id)}`, { method: 'DELETE', stepUpToken }),
  // Skills
  skills: () => request<Page<SkillView>>('/api/skills'),
  enableSkill: (id: string, stepUpToken: string) =>
    request<{ ok: boolean }>(`/api/skills/${encodeURIComponent(id)}/enable`, { method: 'POST', stepUpToken }),
  disableSkill: (id: string, stepUpToken: string) =>
    request<{ ok: boolean }>(`/api/skills/${encodeURIComponent(id)}/disable`, { method: 'POST', stepUpToken }),
  // Prompt 模板
  promptFragments: () => request<Page<PromptFragmentView>>('/api/prompts/fragments'),
  promptFragment: (id: string) => request<PromptFragmentView>(`/api/prompts/fragments/${encodeURIComponent(id)}`),
  updatePromptFragment: (id: string, content: string, stepUpToken: string) =>
    request<{ ok: boolean }>(`/api/prompts/fragments/${encodeURIComponent(id)}`, {
      method: 'PUT',
      body: { content },
      stepUpToken,
    }),
  // 表情包
  stickers: (keyword = '', page = 1, pageSize = 20) => {
    const q = new URLSearchParams({ page: String(page), page_size: String(pageSize) })
    if (keyword) q.set('keyword', keyword)
    return request<Page<StickerView>>(`/api/stickers?${q.toString()}`)
  },
  updateSticker: (id: number, tags: string[], stepUpToken: string) =>
    request<{ ok: boolean }>(`/api/stickers/${id}`, { method: 'PUT', body: { tags }, stepUpToken }),
  deleteSticker: (id: number, stepUpToken: string) =>
    request<{ ok: boolean }>(`/api/stickers/${id}`, { method: 'DELETE', stepUpToken }),
  // 命令
  commands: () => request<Page<CommandView>>('/api/commands'),
}

// 辅助：拼接 since/until 查询参数
function queryRange(since?: string, until?: string): string {
  let s = ''
  if (since) s += `&since=${encodeURIComponent(since)}`
  if (until) s += `&until=${encodeURIComponent(until)}`
  return s
}
