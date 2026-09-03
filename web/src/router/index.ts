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

// 底栏各页都是懒加载分块。集中登记一份,既给路由用,也给 prefetchViews 预热用 ——
// 两处必须是同一个 import() 表达式,Vite 才会认成同一个分块。
const views = {
  terminal: () => import('@/views/TerminalView.vue'),
  files: () => import('@/views/FilesView.vue'),
  filesFile: () => import('@/views/FilesFileView.vue'),
  git: () => import('@/views/GitView.vue'),
  gitFile: () => import('@/views/GitFileView.vue'),
  ai: () => import('@/views/AIView.vue'),
  settings: () => import('@/views/SettingsView.vue'),
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
    { path: '/terminal', name: 'terminal', component: views.terminal },
    { path: '/files', name: 'files', component: views.files },
    { path: '/files/file', name: 'files-file', component: views.filesFile },
    { path: '/git', name: 'git', component: views.git },
    { path: '/git/file', name: 'git-file', component: views.gitFile },
    { path: '/ai', name: 'ai', component: views.ai },
    { path: '/settings', name: 'settings', component: views.settings },
  ],
})

// prefetchViews 空闲时把各页分块提前拉到本地,让底栏切换不再等网络。
// 顺序串行:预热是背景任务,不该跟页面自己的请求抢带宽。
export async function prefetchViews() {
  for (const load of Object.values(views)) {
    try {
      await load()
    } catch { /* 预热失败无所谓,真正切页时还会再要一次 */ }
  }
}

// 登录守卫:未认证(401)则跳登录页。
// fetch 只在网络失败时 reject,HTTP 错误码需显式检查。
// 只在本次加载的首次导航校验一次:每次切页都多一个往返,弱网下就是切页卡顿的主因,
// 而会话中途失效由 api/client.ts 统一拦 401 跳登录页兜底。
let authChecked = false

router.beforeEach(async (to) => {
  if (to.name === 'login' || authChecked) return true
  try {
    const res = await fetch('/api/me', { credentials: 'same-origin' })
    if (res.status === 401) return { name: 'login' }
    authChecked = true
    return true
  } catch {
    return { name: 'login' }
  }
})

export default router
