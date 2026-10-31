import { defineConfig } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'

// 统一写作工作台前端。开发时 vite 代理到本地 workbench API；
// 构建产物 dist/ 由 Go 侧按需静态托管（可选目录，存在即服务）。
export default defineConfig({
  plugins: [svelte()],
  server: {
    proxy: {
      '/api': 'http://127.0.0.1:8790',
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
})
