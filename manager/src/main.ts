import { createApp } from 'vue'
import { createPinia } from 'pinia'
import TDesign from 'tdesign-vue-next'
import 'tdesign-vue-next/es/style/index.css'

import App from './App.vue'
import { router } from './router'
import { useAppStore } from './stores/app'
import './styles/main.css'

const app = createApp(App)
app.use(createPinia())
app.use(TDesign)
app.use(router)

// 应用初始主题（light/dark）+ 跟随系统主题变化
const appStore = useAppStore()
appStore.applyTheme()
appStore.watchSystemTheme()

app.mount('#app')
