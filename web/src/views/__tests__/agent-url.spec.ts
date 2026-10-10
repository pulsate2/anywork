// 会话 id 记进 URL(/agent?session=<id>):刷新浏览器回到同一会话,浏览器前进/
// 后退也在会话之间走。这里用一份内存路由,断言"打开写 URL / 带参回会话 / 返回
// 清参"三条线的接线。浏览器里的真实刷新行为(历史栈、滚动位置)自测。
import { describe, it, expect, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { createRouter, createMemoryHistory, type Router } from 'vue-router'
import { NMessageProvider } from 'naive-ui'
import { config } from '@vue/test-utils'
import AgentView from '@/views/AgentView.vue'
import type { AgentEvent } from '@/api/client'

// setup 里给全局挂了默认路由桩;本文件要断言导航,自己装一份内存路由,先撤掉默认桩。
config.global.plugins = []

const SID = 's-restore'

vi.mock('@/api/client', () => ({
  api: {
    agentSessions: async () => [{
      id: 's-restore', workspace: '/w', title: 'T', app: 'claude',
      status: 'idle', createdAt: '', updatedAt: '',
    }],
    agentMessages: async () => [{ seq: 1, kind: 'user', payload: 'hello', createdAt: '' }] as unknown as AgentEvent[],
    agentSend: async () => null,
    agentQueue: async () => [],
    agentEnqueue: async () => ({ queued: true, item: null, event: null }),
    agentQueueCancel: async () => ({ ok: true }),
    agentInterrupt: async () => ({ ok: true }),
    agentApprove: async () => null,
    agentAnswer: async () => null,
    agentKill: async () => null,
    agentDelete: async () => null,
    agentSessionCreate: async () => null,
    agentSettings: async () => ({}),
    agentCleanupStatus: async () => ({}),
    agentCleanupRun: async () => ({}),
    agentCleanupSave: async () => ({}),
    agentFileUrl: (id: string, n: string) => `/f/${id}/${n}`,
    workspaces: async () => [],
    me: async () => ({ root: '/' }),
  },
}))

vi.mock('@/api/agent', () => ({
  AgentWS: class {
    connected = false
    connect() { this.connected = true }
    close() { this.connected = false }
    subscribe() {}
    unsubscribe() {}
    watch() {}
  },
}))

function makeRouter(): Router {
  return createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: { template: '<div />' } },
      { path: '/agent', component: { template: '<div />' } },
    ],
  })
}

async function mountAt(router: Router, url: string) {
  await router.push(url)
  await router.isReady()
  const w = mount({
    components: { NMessageProvider, AgentView },
    template: '<n-message-provider><agent-view /></n-message-provider>',
  }, { global: { plugins: [createPinia(), router] } })
  await flushPromises()
  return w.findComponent(AgentView).vm as any
}

describe('Agent 会话 URL 绑定', () => {
  it('打开会话把 id 写进 URL', async () => {
    const router = makeRouter()
    const vm = await mountAt(router, '/agent')
    expect(router.currentRoute.value.query.session).toBeUndefined()

    await vm.openSession(SID)
    await flushPromises()
    expect(router.currentRoute.value.query.session).toBe(SID)
  })

  it('带 ?session= 进入(刷新)回到同一会话', async () => {
    const router = makeRouter()
    const vm = await mountAt(router, `/agent?session=${SID}`)
    expect(vm.selectedId).toBe(SID)
    expect(vm.messages.length).toBe(1) // 历史已拉,不是空列表
    expect(router.currentRoute.value.query.session).toBe(SID) // 未产生多余导航
  })

  it('返回列表清掉 URL 里的 session', async () => {
    const router = makeRouter()
    const vm = await mountAt(router, `/agent?session=${SID}`)
    await vm.backToList()
    await flushPromises()
    expect(vm.selectedId).toBeNull()
    expect(router.currentRoute.value.query.session).toBeUndefined()
  })
})
