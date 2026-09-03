import { fileURLToPath, URL } from 'node:url'
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// 构建产物输出到 cmd/server/dist,由 Go embed 打进二进制。
export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  build: {
    outDir: '../cmd/server/dist',
    emptyOutDir: true,
    target: 'es2020',
    sourcemap: false,
    chunkSizeWarningLimit: 600, // vendor(壳)+ xterm 懒加载分块,首屏 <600KB 预算内
    rollupOptions: {
      output: {
        // 按需拆分:核心壳 / xterm(懒加载)。xterm 单独成块,改终端页的代码时
        // 用户只需重下几 KB 的视图块,300KB+ 的 xterm 仍命中强缓存。
        manualChunks: {
          vendor: ['vue', 'vue-router', 'naive-ui'],
          xterm: ['@xterm/xterm', '@xterm/addon-fit'],
        },
      },
    },
  },
  server: {
    // 本地开发代理到 Go 后端。字符串简写不转发 WebSocket upgrade,/api/term 必须显式 ws: true。
    proxy: {
      '/api': { target: 'http://127.0.0.1:18080', ws: true },
    },
    allowedHosts: true
  },
})
