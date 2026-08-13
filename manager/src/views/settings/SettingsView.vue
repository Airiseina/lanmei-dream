<script setup lang="ts">
// 账号与安全：基本信息 / 修改密码 / TOTP / Passkey / 会话管理
import { computed, onMounted, ref } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import { Fingerprint, Monitor, Smartphone, Trash2 } from 'lucide-vue-next'
import { authApi } from '@/api'
import { useAuthStore } from '@/stores/auth'
import StepUpDialog from '@/components/StepUpDialog.vue'
import type { AuthSession } from '@/types/api'
import type { PrimaryTableCol } from 'tdesign-vue-next'

const auth = useAuthStore()

// Passkey 列表列
const passkeyColumns: PrimaryTableCol[] = [
  { colKey: 'name', title: '名称' },
  { colKey: 'last_used_at', title: '最后使用' },
  { colKey: 'ops', title: '操作', width: 80 },
]

// 会话列表列
const sessionColumns: PrimaryTableCol[] = [
  { colKey: 'device', title: '设备' },
  { colKey: 'ip', title: 'IP' },
  { colKey: 'issued_at', title: '登录时间' },
  { colKey: 'expires_at', title: '过期时间' },
  { colKey: 'last_seen_at', title: '最后活跃' },
  { colKey: 'ops', title: '操作', width: 90, align: 'center' },
]

// ── 修改密码 ──
const pwdForm = ref({ oldPassword: '', newPassword: '', confirm: '' })
const pwdLoading = ref(false)

async function changePassword() {
  const { oldPassword, newPassword, confirm } = pwdForm.value
  if (!oldPassword || newPassword.length < 8 || newPassword !== confirm) {
    MessagePlugin.warning('请填写原密码，且新密码至少 8 位并两次一致')
    return
  }
  stepUpVisible.value = true
  pendingAction.value = async (token) => {
    await authApi.changePassword(oldPassword, newPassword, token)
    MessagePlugin.success('密码已修改')
    pwdForm.value = { oldPassword: '', newPassword: '', confirm: '' }
  }
}

// ── TOTP ──
const totpSetup = ref<{ secret: string; otpauthUrl: string } | null>(null)
const totpCode = ref('')

async function beginTOTPSetup() {
  const res = await authApi.totpSetupBegin()
  totpSetup.value = { secret: res.secret, otpauthUrl: res.otpauth_url }
}

async function confirmTOTPSetup() {
  if (totpCode.value.length !== 6) {
    MessagePlugin.warning('请输入 6 位验证码')
    return
  }
  await authApi.totpSetupConfirm(totpCode.value)
  MessagePlugin.success('TOTP 已绑定')
  totpSetup.value = null
  totpCode.value = ''
  await auth.init()
}

async function removeTOTP() {
  stepUpVisible.value = true
  pendingAction.value = async (token) => {
    await authApi.totpRemove(token)
    MessagePlugin.success('TOTP 已解绑')
    await auth.init()
  }
}

// ── Passkey ──
async function registerPasskey() {
  if (!window.isSecureContext || !window.PublicKeyCredential) {
    MessagePlugin.warning('当前环境不支持 Passkey（需要 HTTPS 域名）')
    return
  }
  try {
    const { session_token, creation } = await authApi.passkeyRegisterBegin()
    const cred = (await navigator.credentials.create({
      publicKey: creation as PublicKeyCredentialCreationOptions,
    })) as PublicKeyCredential | null
    if (!cred) return
    await authApi.passkeyRegisterFinish(session_token, cred)
    MessagePlugin.success('Passkey 已注册')
    await auth.init()
  } catch (err) {
    MessagePlugin.error(err instanceof Error ? err.message : 'Passkey 注册失败')
  }
}

async function removePasskey(credentialId: string) {
  stepUpVisible.value = true
  pendingAction.value = async (token) => {
    await authApi.passkeyRemove(credentialId, token)
    MessagePlugin.success('已删除')
    await auth.init()
  }
}

// ── 会话管理 ──
const sessions = ref<AuthSession[]>([])
const sessionLoading = ref(false)

async function loadSessions() {
  sessionLoading.value = true
  try {
    const res = await authApi.sessions()
    sessions.value = res.sessions
  } catch (err) {
    MessagePlugin.error(err instanceof Error ? err.message : '会话加载失败')
  } finally {
    sessionLoading.value = false
  }
}

async function revokeSession(id: number) {
  await authApi.revokeSession(id)
  MessagePlugin.success('会话已吊销')
  await loadSessions()
}

// ── step-up 联动 ──
const stepUpVisible = ref(false)
const pendingAction = ref<((token: string) => Promise<void>) | null>(null)

function onStepUpSuccess(token: string) {
  void pendingAction.value?.(token)
  pendingAction.value = null
}

const passkeySupported = computed(() => window.isSecureContext && typeof window.PublicKeyCredential !== 'undefined')

function copySecret() {
  if (totpSetup.value) {
    void navigator.clipboard?.writeText(totpSetup.value.secret)
    MessagePlugin.success('密钥已复制')
  }
}

function fmtTime(s?: string | null): string {
  return s ? new Date(s).toLocaleString() : '—'
}

onMounted(async () => {
  await auth.init()
  await loadSessions()
})
</script>

<template>
  <div class="page">
    <div class="page-header">
      <h2 class="page-title">账号与安全</h2>
    </div>

    <t-row :gutter="[16, 16]">
      <!-- 账号信息 -->
      <t-col :xs="24" :lg="12">
        <t-card title="账号信息" class="h-full">
          <t-descriptions :column="1">
            <t-descriptions-item label="用户名">{{ auth.me?.username }}</t-descriptions-item>
            <t-descriptions-item label="显示名称">{{ auth.me?.display_name || '—' }}</t-descriptions-item>
            <t-descriptions-item label="角色">
              <t-tag :theme="auth.isSuper ? 'danger' : 'primary'" variant="light">
                {{ auth.isSuper ? '超级管理员' : '普通管理员' }}
              </t-tag>
            </t-descriptions-item>
            <t-descriptions-item label="TOTP">
              <t-tag :theme="auth.me?.has_totp ? 'success' : 'default'" variant="light">
                {{ auth.me?.has_totp ? '已绑定' : '未绑定' }}
              </t-tag>
            </t-descriptions-item>
            <t-descriptions-item label="Passkey">
              <t-tag :theme="auth.me?.has_passkey ? 'success' : 'default'" variant="light">
                {{ auth.me?.has_passkey ? '已注册' : '未注册' }}
              </t-tag>
            </t-descriptions-item>
          </t-descriptions>
        </t-card>
      </t-col>

      <!-- 修改密码 -->
      <t-col :xs="24" :lg="12">
        <t-card title="修改密码" class="h-full">
          <t-form label-align="top">
            <t-form-item label="原密码">
              <t-input v-model="pwdForm.oldPassword" type="password" autocomplete="current-password" />
            </t-form-item>
            <t-form-item label="新密码（至少 8 位）">
              <t-input v-model="pwdForm.newPassword" type="password" autocomplete="new-password" />
            </t-form-item>
            <t-form-item label="确认新密码">
              <t-input v-model="pwdForm.confirm" type="password" autocomplete="new-password" />
            </t-form-item>
            <t-button theme="primary" :loading="pwdLoading" @click="changePassword">修改密码</t-button>
          </t-form>
        </t-card>
      </t-col>
    </t-row>

    <t-row :gutter="[16, 16]" class="mt-16">
      <!-- TOTP -->
      <t-col :xs="24" :lg="12">
        <t-card title="两步验证（TOTP）" class="h-full">
          <template v-if="totpSetup">
            <t-alert theme="warning" message="请将密钥输入到 Authenticator 应用（如 Google Authenticator / Microsoft Authenticator），然后输入 6 位验证码完成绑定。" class="mb-16" />
            <t-form label-align="top">
              <t-form-item label="密钥">
                <t-input :model-value="totpSetup.secret" readonly>
                  <template #suffix>
                    <t-button variant="text" @click="copySecret">复制</t-button>
                  </template>
                </t-input>
              </t-form-item>
              <t-form-item label="验证码">
                <t-input v-model="totpCode" placeholder="6 位验证码" maxlength="6" />
              </t-form-item>
              <t-space>
                <t-button theme="primary" @click="confirmTOTPSetup">确认绑定</t-button>
                <t-button variant="outline" @click="totpSetup = null">取消</t-button>
              </t-space>
            </t-form>
          </template>
          <template v-else>
            <p class="desc">TOTP 为登录提供第二道验证（除密码外），强烈建议开启。</p>
            <t-button v-if="!auth.me?.has_totp" theme="primary" @click="beginTOTPSetup">
              <template #icon><Smartphone :size="16" /></template>绑定 TOTP
            </t-button>
            <t-button v-else theme="danger" variant="outline" @click="removeTOTP">
              <template #icon><Trash2 :size="16" /></template>解绑 TOTP
            </t-button>
          </template>
        </t-card>
      </t-col>

      <!-- Passkey -->
      <t-col :xs="24" :lg="12">
        <t-card title="Passkey（WebAuthn）" class="h-full">
          <p class="desc">
            Passkey 支持指纹 / 面容 / 安全密钥一键登录，无需密码。
            {{ passkeySupported ? '' : '当前环境（IP / 非 HTTPS）不支持，登录时将自动回退密码。' }}
          </p>
          <t-button theme="primary" :disabled="!passkeySupported" @click="registerPasskey">
            <template #icon><Fingerprint :size="16" /></template>注册新 Passkey
          </t-button>
          <t-table v-if="auth.me?.passkeys?.length" :data="auth.me.passkeys" row-key="credential_id" class="mt-16" size="small" :columns="passkeyColumns">
            <template #name="{ row }">{{ row.name || row.credential_id.slice(0, 12) }}</template>
            <template #last_used_at="{ row }">{{ fmtTime(row.last_used_at) }}</template>
            <template #ops="{ row }">
              <t-button size="small" variant="base" theme="danger" @click="removePasskey(row.credential_id)">删除</t-button>
            </template>
          </t-table>
        </t-card>
      </t-col>
    </t-row>

    <!-- 会话管理 -->
    <t-card title="活跃会话" class="mt-16">
      <t-table :data="sessions" :loading="sessionLoading" row-key="id" size="medium" :columns="sessionColumns">
        <template #device="{ row }">
          <div class="session-cell">
            <Monitor :size="16" />
            <span>{{ row.device || '未知设备' }}</span>
          </div>
        </template>
        <template #ip="{ row }">{{ row.ip || '—' }}</template>
        <template #issued_at="{ row }">{{ fmtTime(row.issued_at) }}</template>
        <template #expires_at="{ row }">{{ fmtTime(row.expires_at) }}</template>
        <template #last_seen_at="{ row }">{{ fmtTime(row.last_seen_at) }}</template>
        <template #ops="{ row }">
          <t-button size="small" variant="base" theme="danger" @click="revokeSession(row.id)">吊销</t-button>
        </template>
      </t-table>
    </t-card>

    <StepUpDialog v-model="stepUpVisible" @success="onStepUpSuccess" />
  </div>
</template>

<style scoped>
.h-full {
  height: 100%;
}
.mt-16 {
  margin-top: 16px;
}
.mb-16 {
  margin-bottom: 16px;
}
.desc {
  color: var(--mgr-text-secondary);
  font-size: 13px;
}
.session-cell {
  display: flex;
  align-items: center;
  gap: 6px;
}
</style>
