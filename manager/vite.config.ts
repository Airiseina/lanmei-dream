import { fileURLToPath, URL } from 'node:url'
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// https://vite.dev/config/
export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    port: 5173,
    // 开发代理：/api 转发到内嵌管理服务（Fiber，独立端口 8090）。
    // 保留 Host（changeOrigin: false），使后端 CSRF 同源校验通过（Origin 与 Host 一致）。
    proxy: {
      '/api': {
        target: process.env.MANAGER_PROXY_TARGET ?? 'http://127.0.0.1:8090',
        changeOrigin: false,
      },
    },
  },
  build: {
    outDir: 'dist',
    sourcemap: false,
    chunkSizeWarningLimit: 1500,
  },
})
