import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import router, { prefetchViews } from './router'
import './assets/main.css'

const app = createApp(App)
app.use(createPinia())
app.use(router)
app.mount('#app')

// 首屏空闲后预热底栏各页的分块,别跟首屏自己的请求抢带宽。
router.isReady().then(() => {
  if ('requestIdleCallback' in window) requestIdleCallback(() => prefetchViews(), { timeout: 3000 })
  else setTimeout(prefetchViews, 1500)
})

// 注册 Service Worker(Web Push 通知)。可选项:失败不影响应用。
if ('serviceWorker' in navigator) {
  navigator.serviceWorker.register('/sw.js').catch(() => { /* SW 可选 */ })
}
