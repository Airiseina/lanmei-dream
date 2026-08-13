// 应用级状态：主题（light/dark）、侧栏折叠、语言
import { defineStore } from 'pinia'

export type ThemeMode = 'light' | 'dark'

const THEME_KEY = 'lanmei_theme'

function detectTheme(): ThemeMode {
  const saved = localStorage.getItem(THEME_KEY) as ThemeMode | null
  if (saved === 'light' || saved === 'dark') return saved
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}

export const useAppStore = defineStore('app', {
  state: () => ({
    theme: detectTheme() as ThemeMode,
    collapsed: false,
    isMobile: window.innerWidth < 768,
    // 系统主题变化监听器（跟随系统时自动切换）
    mediaQuery: null as MediaQueryList | null,
  }),
  actions: {
    toggleTheme() {
      this.theme = this.theme === 'dark' ? 'light' : 'dark'
      this.applyTheme()
    },
    applyTheme() {
      localStorage.setItem(THEME_KEY, this.theme)
      const el = document.documentElement
      // data-theme：本应用自定义 CSS 变量选择器
      el.setAttribute('data-theme', this.theme)
      // theme-mode：TDesign 官方暗色模式选择器（组件库配色依赖此属性）
      el.setAttribute('theme-mode', this.theme)
    },
    /** 监听系统主题变化（仅在用户未手动指定时跟随系统） */
    watchSystemTheme() {
      if (this.mediaQuery) return
      const mq = window.matchMedia('(prefers-color-scheme: dark)')
      this.mediaQuery = mq
      mq.addEventListener('change', (e) => {
        if (!localStorage.getItem(THEME_KEY)) {
          this.theme = e.matches ? 'dark' : 'light'
          this.applyTheme()
        }
      })
    },
    toggleSidebar() {
      this.collapsed = !this.collapsed
    },
    setMobile(v: boolean) {
      this.isMobile = v
      if (v) this.collapsed = true
    },
  },
})
