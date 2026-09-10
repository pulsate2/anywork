// 复现 #16:排队消息服务端化后的前端行为 —— 入队显示、撤回调 API、
// 打开会话时从 GET /queue 补齐。放行逻辑在服务端 pump(后端
// TestReleaseQueued 覆盖),这里只测 UI。
import { describe, it, expect, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { NMessageProvider } from 'naive-ui'
import AgentView from '@/views/AgentView.vue'
import type { AgentEvent } from '@/api/client'

// vi.mock 工厂被提升,引用不到外层变量 —— vi.hoisted 把 spy 提到工厂之前。
const mocks = vi.hoisted(() => ({
  enqueue: vi.fn(),
  cancel: vi.fn(),
  queue: vi.fn(),
}))

vi.mock('@/api/client', () => {
  const sess = {
    id: 's-queue', workspace: '/w', title: 'queue', app: 'claude',
    status: 'running', createdAt: '2026-09-10T06:00:00Z', updatedAt: '2026-09-10T06:00:00Z',
  }
  // 一条 running 状态事件:turnState 由消息流推导,不是会话列表的 status。
  const msgs = [{
    seq: 1, kind: 'status', payload: { state: 'running' },
    sessionId: 's-queue', createdAt: '2026-09-10T06:00:00Z',
  }] as AgentEvent[]
  return {
    api: {
      agentSessions: async () => [sess],
      agentMessages: async () => msgs,
      agentSend: async () => null,
      agentQueue: mocks.queue,
      agentEnqueue: mocks.enqueue,
      agentQueueCancel: mocks.cancel,
      agentInterrupt: async () => ({ ok: true }),
      agentApprove: async () => null,
      agentAnswer: async () => null,
      agentKill: async () => null,
      agentDelete: async () => null,
      agentSessionCreate: async () => sess,
      agentSettings: async () => ({}),
      agentCleanupStatus: async () => ({}),
      agentCleanupRun: async () => ({}),
      agentCleanupSave: async () => ({}),
      agentFileUrl: (id: string, n: string) => `/api/agent/sessions/${id}/files/${n}`,
      workspaces: async () => [],
      me: async () => ({ root: '/' }),
    },
  }
})

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

describe('排队消息(服务端队列)', () => {
  const SID = 's-queue'
  async function mountView() {
    const w = mount({
      components: { NMessageProvider, AgentView },
      template: '<n-message-provider><agent-view /></n-message-provider>',
    }, { global: { plugins: [createPinia()] } })
    await flushPromises()
    const vm = w.findComponent(AgentView).vm as any
    await vm.openSession(SID)
    await flushPromises()
    return { w, vm }
  }

  it('running 时发送 → 走入队 API,气泡出现且可撤回', async () => {
    mocks.queue.mockResolvedValue([])
    mocks.enqueue.mockResolvedValue({
      queued: true,
      item: { id: 7, text: '排队消息', createdAt: '2026-09-10T06:01:00Z' },
    })
    mocks.cancel.mockResolvedValue({ ok: true })
    const { w, vm } = await mountView()

    // 回合进行中:send 应入队而不是直发。
    await vm.send('排队消息')
    await flushPromises()
    expect(mocks.enqueue).toHaveBeenCalledWith(SID, '排队消息')
    expect(w.findAll('.queued-bubble').length).toBe(1)
    expect(w.find('.queued-text').text()).toBe('排队消息')

    // 撤回:调 DELETE,成功后气泡消失。
    await w.find('.queued-cancel').trigger('click')
    await flushPromises()
    expect(mocks.cancel).toHaveBeenCalledWith(SID, 7)
    expect(w.findAll('.queued-bubble').length).toBe(0)
  })

  it('打开会话时从服务端补齐队列(GET /queue)', async () => {
    // 模拟:页面退出前有两条在队,重新打开会话时补齐显示。
    mocks.queue.mockResolvedValue([
      { id: 1, text: '页面退出前排的', createdAt: '2026-09-10T06:01:00Z' },
      { id: 2, text: '第二条', createdAt: '2026-09-10T06:02:00Z' },
    ])
    const { w } = await mountView()
    expect(mocks.queue).toHaveBeenCalledWith(SID)
    const texts = w.findAll('.queued-text').map((x) => x.text())
    expect(texts).toEqual(['页面退出前排的', '第二条'])
  })
})
