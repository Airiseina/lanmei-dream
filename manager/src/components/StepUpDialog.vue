<script setup lang="ts">
// Step-up 二次身份验证弹窗：高危操作（管理员管理 / LLM Provider / Conduit 编辑）前置校验。
// 支持 TOTP 验证码或密码复核（后端 StepUpVerify 自动选择）。
import { reactive, ref } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import { ShieldCheck } from 'lucide-vue-next'
import { useAuthStore } from '@/stores/auth'

const auth = useAuthStore()

const visible = defineModel<boolean>({ default: false })

const emit = defineEmits<{
  success: [token: string]
}>()

const form = reactive({ password: '', totpCode: '' })
const loading = ref(false)

async function submit() {
  if (!form.password && !form.totpCode) {
    MessagePlugin.warning('请输入 TOTP 验证码或登录密码')
    return
  }
  loading.value = true
  try {
    await auth.stepUp(form.password, form.totpCode)
    MessagePlugin.success('身份验证通过')
    visible.value = false
    emit('success', auth.stepUpToken)
  } catch (err) {
    MessagePlugin.error(err instanceof Error ? err.message : '身份验证失败')
  } finally {
    loading.value = false
  }
}

function handleClose() {
  form.password = ''
  form.totpCode = ''
}
</script>

<template>
  <t-dialog
    v-model:visible="visible"
    header="二次身份验证"
    :confirm-btn="{ content: '验证', loading: loading, disabled: loading }"
    cancel-btn="取消"
    @confirm="submit"
    @close="handleClose"
    @cancel="handleClose"
  >
    <div class="stepup-body">
      <ShieldCheck :size="36" />
      <p>该操作属于敏感操作，请完成二次身份验证（推荐使用 TOTP 验证码）。</p>
    </div>
    <t-form label-align="top">
      <t-form-item label="TOTP 验证码">
        <t-input v-model="form.totpCode" placeholder="6 位动态验证码" maxlength="6" @enter="submit" />
      </t-form-item>
      <t-form-item label="或登录密码（无 TOTP 时）">
        <t-input v-model="form.password" type="password" placeholder="登录密码" autocomplete="current-password" @enter="submit" />
      </t-form-item>
    </t-form>
  </t-dialog>
</template>

<style scoped>
.stepup-body {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 16px;
  color: var(--mgr-text-secondary);
}
</style>
