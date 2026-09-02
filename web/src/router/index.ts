import { createRouter, createWebHistory } from 'vue-router'
import LoginView from '@/views/LoginView.vue'
import HomeView from '@/views/HomeView.vue'

const router = createRouter({
  history: createWebHistory(),
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
