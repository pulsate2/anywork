import { createRouter, createWebHistory } from 'vue-router'
import LoginView from '@/views/LoginView.vue'
import HomeView from '@/views/HomeView.vue'

// vue-router 会把浏览器的原生滚动恢复关掉,不给 scrollBehavior 就等于返回时一律回到顶部。
// 各页数据都是挂载后异步拉的,恢复那一刻文档往往还没那么高,直接滚会被截断到底,
// 所以先等文档长够(最多 1.5 秒)再把位置交回去。
function waitForHeight(top: number) {
  return new Promise<void>((resolve) => {
    const deadline = performance.now() + 1500
    const tick = () => {
      const max = document.documentElement.scrollHeight - window.innerHeight
      if (max >= top || performance.now() >= deadline) resolve()
      else requestAnimationFrame(tick)
    }
    tick()
  })
}

const router = createRouter({
  history: createWebHistory(),
  async scrollBehavior(to, from, saved) {
    if (saved) {
      if (saved.top > 0) await waitForHeight(saved.top)
      return saved
    }
    // 同一个页面只改 query(例如抹掉一次性的返回标记)不算换页,别把用户拽回顶部。
    if (to.path === from.path) return false
    return { left: 0, top: 0 }
  },
  routes: [
    { path: '/', name: 'home', component: HomeView },
    { path: '/login', name: 'login', component: LoginView },
    { path: '/terminal', name: 'terminal', component: () => import('@/views/TerminalView.vue') },
    { path: '/files', name: 'files', component: () => import('@/views/FilesView.vue') },
    { path: '/files/file', name: 'files-file', component: () => import('@/views/FilesFileView.vue') },
    { path: '/git', name: 'git', component: () => import('@/views/GitView.vue') },
    { path: '/git/file', name: 'git-file', component: () => import('@/views/GitFileView.vue') },
    { path: '/ai', name: 'ai', component: () => import('@/views/AIView.vue') },
    { path: '/settings', name: 'settings', component: () => import('@/views/SettingsView.vue') },
  ],
})

// 登录守卫:未认证(401)则跳登录页。
// fetch 只在网络失败时 reject,HTTP 错误码需显式检查。
router.beforeEach(async (to) => {
  if (to.name === 'login') return true
  try {
    const res = await fetch('/api/me', { credentials: 'same-origin' })
    if (res.status === 401) return { name: 'login' }
    return true
  } catch {
    return { name: 'login' }
  }
})

export default router
