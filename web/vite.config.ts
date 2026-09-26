import { fileURLToPath, URL } from 'node:url'
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// 开发期那条 vite-hmr WebSocket 默认关掉(临时,联调 WS 重连用)。
// 为什么非关不可:手机切后台/断网回来时,Vite 客户端也会掉线,它会在轮询到
// dev server 复活后 location.reload() —— 整页一刷,正在看的重连标识和时间线
// 全没了,重连这个功能就没法测。关掉的只是 vite 自己那条:代理到后端的
// /api WebSocket(下面 proxy 里的 ws: true)走的是另一条 upgrade 监听,不受
// 影响,agent 的推送通道照常。
// 注意是 server.ws 不是 server.hmr:v5.4 里 hmr: false 只停掉"文件变更→推送更新"
// 那段逻辑,客户端照旧连、服务端照旧接那条 vite-hmr 连接(实测 hmr:false 下
// upgrade 仍回 101);只有 ws: false 才会真的不注册 upgrade 监听。
// 代价:改代码不再热更新,得手动刷新页面。浏览器控制台会有一条 [vite] failed to
// connect to websocket —— 就是被关掉的那条,不影响 /api(它不会 reload 页面)。
// 恢复热更新:VITE_DEV_WS=1 npm run dev。
const devWS = process.env.VITE_DEV_WS === '1'

// 构建产物输出到 cmd/server/dist,由 Go embed 打进二进制。
export default defineConfig({
  plugins: [
    vue(),
    {
      // 启动时说一声:免得下次有人(包括我自己)纳闷"改了代码怎么不刷新"。
      name: 'lr:dev-ws-off',
      configureServer() {
        if (!devWS) {
          console.log('\n[lightremote] 开发 WebSocket 已禁用(HMR 关闭,改代码需手动刷新页面)')
          console.log('[lightremote] 恢复热更新:VITE_DEV_WS=1 npm run dev\n')
        }
      },
    },
  ],
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
    // 不启动 vite 自己的 WebSocket(类型上只收 false,开着就写 undefined)。
    ws: devWS ? undefined : false,
    // 本地开发代理到 Go 后端。字符串简写不转发 WebSocket upgrade,/api/term 必须显式 ws: true。
    proxy: {
      '/api': { target: 'http://127.0.0.1:18080', ws: true },
    },
    allowedHosts: true
  },
})
