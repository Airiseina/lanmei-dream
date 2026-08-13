<script setup lang="ts">
// 登录页：密码 + TOTP 二次验证 + Passkey（自动检测可用性，不可用时纯密码回退）
import { computed, reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { MessagePlugin } from 'tdesign-vue-next'
import { Bot, Fingerprint, KeyRound } from 'lucide-vue-next'
import { authApi } from '@/api'
import { useAuthStore } from '@/stores/auth'

const auth = useAuthStore()
const router = useRouter()
const route = useRoute()

const form = reactive({ username: '', password: '', totpCode: '' })
const loading = ref(false)
const totpPendingToken = ref('')
const passkeyLoading = ref(false)

// Passkey 可用性：安全上下文 + WebAuthn API + 域名（IP 部署自动回退密码）
const passkeySupported = computed(() => {
  if (!window.isSecureContext) return false
  if (typeof window.PublicKeyCredential === 'undefined') return false
  const host = window.location.hostname
  if (/^\d{1,3}(\.\d{1,3}){3}$/.test(host)) return false
  return true
})

async function handlePasswordLogin() {
  if (!form.username || !form.password) {
    MessagePlugin.warning('请输入用户名和密码')
    return
  }
  loading.value = true
  try {
    const res = await authApi.passwordLogin(form.username, form.password)
    if (res.pending_totp) {
      totpPendingToken.value = res.pending_totp.token
      MessagePlugin.info('请输入 TOTP 验证码完成登录')
      return
    }
    if (res.access_token && res.refresh_token) {
      await finishLogin(res.access_token, res.refresh_token)
    }
  } catch (err) {
    handleLoginError(err)
  } finally {
    loading.value = false
  }
}

async function handleTOTPVerify() {
  if (!form.totpCode || !totpPendingToken.value) return
  loading.value = true
  try {
    const session = await authApi.verifyTOTP(totpPendingToken.value, form.totpCode)
    await finishLogin(session.access_token, session.refresh_token)
  } catch (err) {
    handleLoginError(err)
  } finally {
    loading.value = false
  }
}

async function handlePasskeyLogin() {
  if (!passkeySupported.value || !form.username) {
    MessagePlugin.warning('请先输入用户名，且当前环境支持 Passkey')
    return
  }
  passkeyLoading.value = true
  try {
    const { session_token, assertion } = await authApi.webauthnLoginBegin(form.username)
    const cred = (await navigator.credentials.get({
      publicKey: assertion as PublicKeyCredentialRequestOptions,
    })) as PublicKeyCredential | null
    if (!cred) return // 用户取消
    const session = await authApi.webauthnLoginFinish(session_token, form.username, cred)
    await finishLogin(session.access_token, session.refresh_token)
  } catch (err) {
    handleLoginError(err)
  } finally {
    passkeyLoading.value = false
  }
}

async function finishLogin(access: string, refresh: string) {
  auth.applySession(access, refresh)
  const me = await authApi.me()
  auth.setMe(me)
  MessagePlugin.success('登录成功')
  const redirect = (route.query.redirect as string) || '/dashboard'
  router.replace(redirect)
}

function handleLoginError(err: unknown) {
  if (auth.isApiError(err)) {
    if (err.code === 'WEBAUTHN_UNAVAILABLE') {
      MessagePlugin.warning('当前环境不支持 Passkey，请使用密码登录')
      return
    }
    MessagePlugin.error(err.message)
  } else {
    MessagePlugin.error('登录失败，请稍后重试')
  }
}
</script>

<template>
  <div class="login-page">
    <div class="login-card">
      <div class="login-brand">
        <div class="brand-logo">
          <Bot :size="30" />
        </div>
        <h1>蓝妹管理面板</h1>
        <p>群聊 AI Bot 运维控制台</p>
      </div>

      <!-- 密码登录（t-form submit 为自定义事件，内部已阻止原生提交，勿加 .prevent） -->
      <t-form v-if="!totpPendingToken" label-align="top" @submit="handlePasswordLogin">
        <t-form-item label="用户名">
          <t-input v-model="form.username" placeholder="请输入用户名" autocomplete="username" size="large">
            <template #prefix-icon><KeyRound :size="16" /></template>
          </t-input>
        </t-form-item>
        <t-form-item label="密码">
          <t-input
            v-model="form.password"
            type="password"
            placeholder="请输入密码"
            autocomplete="current-password"
            size="large"
            @enter="handlePasswordLogin"
          >
            <template #prefix-icon><KeyRound :size="16" /></template>
          </t-input>
        </t-form-item>
        <t-button block theme="primary" size="large" :loading="loading" type="submit">登 录</t-button>
        <t-button
          v-if="passkeySupported"
          block
          variant="outline"
          size="large"
          class="passkey-btn"
          :loading="passkeyLoading"
          @click="handlePasskeyLogin"
        >
          <template #icon><Fingerprint :size="18" /></template>
          使用 Passkey 登录
        </t-button>
      </t-form>

      <!-- TOTP 二次验证 -->
      <t-form v-else label-align="top" @submit="handleTOTPVerify">
        <t-form-item label="TOTP 验证码">
          <t-input v-model="form.totpCode" placeholder="6 位验证码" maxlength="6" size="large" @enter="handleTOTPVerify" />
        </t-form-item>
        <t-button block theme="primary" size="large" :loading="loading" type="submit">验 证</t-button>
      </t-form>
    </div>
  </div>
</template>

<style scoped>
.login-page {
  height: 100%;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 16px;
  background: var(--mgr-bg);
  background-image: radial-gradient(1200px 600px at 15% -10%, var(--mgr-primary-soft), transparent 60%),
    radial-gradient(1000px 500px at 110% 110%, var(--mgr-primary-soft), transparent 55%);
}

.login-card {
  width: 100%;
  max-width: 392px;
  background: var(--mgr-bg-card);
  border: 1px solid var(--mgr-border);
  border-radius: var(--mgr-radius-lg);
  padding: 36px 32px 24px;
  box-shadow: var(--mgr-shadow-lg);
}

.login-brand {
  text-align: center;
  margin-bottom: 28px;
}

.brand-logo {
  width: 60px;
  height: 60px;
  margin: 0 auto 12px;
  display: flex;
  align-items: center;
  justify-content: center;
  border-radius: 16px;
  color: #fff;
  background: linear-gradient(135deg, var(--mgr-primary) 0%, var(--mgr-primary-hover) 100%);
  box-shadow: 0 8px 24px var(--mgr-primary-soft);
}

.login-brand h1 {
  margin: 0 0 4px;
  font-size: 22px;
  font-weight: 700;
  color: var(--mgr-text);
  letter-spacing: 0.5px;
}

.login-brand p {
  margin: 0;
  color: var(--mgr-text-secondary);
  font-size: 13px;
}

.passkey-btn {
  margin-top: 12px;
}

.login-foot {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  margin-top: 24px;
  padding-top: 16px;
  border-top: 1px solid var(--mgr-border);
  color: var(--mgr-text-muted);
  font-size: 12px;
}
</style>
