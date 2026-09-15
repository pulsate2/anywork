// 思考直播卡:流式 reasoning_delta 期间显示"思考中 + 实时秒表",完整消息
// 落库(任意持久化事件)即收尾换正式卡(思考过程 · thinkMs)。
// 秒表是本地 setInterval:用假时间驱动,验证起表/跳秒/停表三件事。
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { NMessageProvider } from 'naive-ui'
import AgentView from '@/views/AgentView.vue'
import { AgentWS } from '@/api/agent'

vi.mock('@/api/client', () => {
  const sess = {
    id: 's-think', workspace: '/root/gittest2', title: 'think', app: 'claude',
    status: 'idle', createdAt: '2026-09-15T12:00:00Z', updatedAt: '2026-09-15T12:00:00Z',
  }
  return {
    api: {
      agentSessions: async () => [sess],
      agentMessages: async () => [],
      agentSend: async () => null,
      agentQueue: async () => [],
      agentEnqueue: async () => ({ queued: true, item: null, event: null }),
      agentQueueCancel: async () => ({ ok: true }),
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

// AgentWS 换成可注入的桩:把 AgentView 注册的 handler 存下来,测试里直接
// 推 WS 消息(reasoning_delta / 持久化事件)。类要有本地名:工厂里引用
// import 的 AgentWS 会被 vue-tsc 按真实类型检查(handlers 不存在)。
vi.mock('@/api/agent', () => {
  class MockAgentWS {
    static handlers: Array<(e: any) => void> = []
    connected = false
    constructor(handler: (e: any) => void) { MockAgentWS.handlers.push(handler) }
    connect() { this.connected = true }
    close() { this.connected = false }
    subscribe() {}
    unsubscribe() {}
    watch() {}
  }
  return { AgentWS: MockAgentWS }
})

// 桩类(带静态 handlers):vi.mock 的类型仍是真实 AgentWS,这里 any 桥一下。
const MockWS = AgentWS as unknown as { handlers: Array<(e: any) => void> }

const SID = 's-think'

async function fire(e: any) {
  for (const h of MockWS.handlers) h(e)
  await flushPromises()
}

describe('思考直播卡:思考中 + 实时计时器', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    MockWS.handlers = []
  })
  afterEach(() => vi.useRealTimers())

  it('delta 到达即起表显示秒数;随时间跳秒;收尾停表', async () => {
    const w = mount({
      components: { NMessageProvider, AgentView },
      template: '<n-message-provider><agent-view /></n-message-provider>',
    }, { global: { plugins: [createPinia()] } })
    await flushPromises()
    const vm = w.findComponent(AgentView).vm as any
    await vm.openSession(SID)
    await flushPromises()

    // 第一个思考增量:直播卡出现,标签是"思考中"(不再是"思考过程中…"),
    // 秒表从 1 秒起跳(fmtThink 对 0 取 max(1,…))
    await fire({ type: 'message', event: { sessionId: SID, kind: 'reasoning_delta', payload: '先理一下思路' } })
    const summary = () => w.find('.chat-row.reasoning summary').text()
    expect(w.find('.chat-row.reasoning').exists()).toBe(true)
    expect(summary()).toBe('思考中 · 1 秒')
    expect(w.find('.reasoning-body').text()).toContain('先理一下思路')

    // 5 秒后:读数跟着跳(假时间驱动 setInterval 与 Date.now)
    await vi.advanceTimersByTimeAsync(5000)
    expect(summary()).toBe('思考中 · 5 秒')

    // 任意持久化事件到达 = 思考段收尾:直播卡清掉,秒表停
    await fire({ type: 'message', event: { sessionId: SID, kind: 'status', payload: { state: 'running' }, seq: 1 } })
    expect(w.find('.chat-row.reasoning').exists()).toBe(false)
    // 停表后时间再走,也不会有遗留的 setInterval(没有可断言的 DOM,只要
    // 不报错且卡片保持消失)
    await vi.advanceTimersByTimeAsync(3000)
    expect(w.find('.chat-row.reasoning').exists()).toBe(false)
    w.unmount()
  })

  it('退出重进:起点回退到上一条持久化事件的 createdAt,计时续上不归零', async () => {
    const w = mount({
      components: { NMessageProvider, AgentView },
      template: '<n-message-provider><agent-view /></n-message-provider>',
    }, { global: { plugins: [createPinia()] } })
    await flushPromises()
    const vm = w.findComponent(AgentView).vm as any
    await vm.openSession(SID)
    await flushPromises()

    // 重进前的现场:思考 65 秒前就开始了,但增量是瞬态的,回放里没有 ——
    // 时间线最后一条持久化事件(用户消息)的 createdAt 就是思考的起点。
    const t0 = new Date(Date.now() - 65_000).toISOString()
    await fire({ type: 'message', event: { sessionId: SID, kind: 'user', payload: '跑吧', seq: 1, createdAt: t0 } })
    // 重进后第一条增量:直播卡出现,读数直接接上(1 分 5 秒,不是 1 秒)
    await fire({ type: 'message', event: { sessionId: SID, kind: 'reasoning_delta', payload: '继续想' } })
    const summary = () => w.find('.chat-row.reasoning summary').text()
    expect(summary()).toBe('思考中 · 1 分 5 秒')
    // 时间再走 5 秒:在已有基础上跳
    await vi.advanceTimersByTimeAsync(5000)
    expect(summary()).toBe('思考中 · 1 分 10 秒')
    w.unmount()
  })
})
