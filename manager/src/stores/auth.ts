// 认证状态：登录、登出、会话恢复、step-up 令牌管理
import { defineStore } from 'pinia'
import { authApi } from '@/api'
import {
  ApiError,
  LS_ACCESS,
  LS_CSRF,
  clearTokens,
  getRefreshToken,
  readCsrfFromCookie,
  setSessionExpiredHandler,
  setTokens,
} from '@/api/client'
import type { MeInfo } from '@/types/api'

export const useAuthStore = defineStore('auth', {
  state: () => ({
    me: null as MeInfo | null,
    accessToken: localStorage.getItem(LS_ACCESS) ?? '',
    stepUpToken: '' as string,
    stepUpExpiresAt: 0 as number, // 毫秒时间戳
    ready: false,
  }),
  getters: {
    isLoggedIn: (s) => Boolean(s.accessToken),
    isSuper: (s) => s.me?.role === 'super_admin',
    isStepUpValid: (s) => Boolean(s.stepUpToken) && Date.now() < s.stepUpExpiresAt,
  },
  actions: {
    /** 应用启动时恢复会话 */
    async init() {
      setSessionExpiredHandler(() => this.forceLogout())
      // 同步 CSRF 令牌到 localStorage（登录后的新会话）
      if (!localStorage.getItem(LS_CSRF)) localStorage.setItem(LS_CSRF, readCsrfFromCookie())
      if (this.isLoggedIn) {
        try {
          this.me = await authApi.me()
        } catch {
          this.forceLogout()
        }
      }
      this.ready = true
    },
    /** 保存登录成功会话 */
    applySession(access: string, refresh: string) {
      setTokens(access, refresh)
      this.accessToken = access
      if (!localStorage.getItem(LS_CSRF)) localStorage.setItem(LS_CSRF, readCsrfFromCookie())
    },
    setMe(me: MeInfo) {
      this.me = me
    },
    /** step-up 二次验证 */
    async stepUp(password: string, totpCode: string) {
      const { step_up_token, expires_in } = await authApi.stepUp(password, totpCode)
      this.stepUpToken = step_up_token
      this.stepUpExpiresAt = Date.now() + expires_in * 1000
    },
    clearStepUp() {
      this.stepUpToken = ''
      this.stepUpExpiresAt = 0
    },
    /** 登出 */
    async logout() {
      const refresh = getRefreshToken()
      if (refresh) await authApi.logout(refresh).catch(() => undefined)
      this.forceLogout()
    },
    forceLogout() {
      clearTokens()
      this.accessToken = ''
      this.me = null
      this.clearStepUp()
    },
    /** 高危操作需 step-up；未验证时抛出提示 */
    requireStepUp(password: string, totpCode: string) {
      return this.stepUp(password, totpCode)
    },
    isApiError(err: unknown): err is ApiError {
      return err instanceof ApiError
    },
  },
})
