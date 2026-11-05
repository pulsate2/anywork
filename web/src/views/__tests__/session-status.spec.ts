import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { NMessageProvider } from 'naive-ui'
import AgentView from '@/views/AgentView.vue'
import Composer from '@/components/agent/Composer.vue'
import StatusBar from '@/components/agent/StatusBar.vue'
import type { AgentEvent, AgentSession, AgentStatusPayload } from '@/api/client'

const mocks = vi.hoisted(() => ({
  sessions: vi.fn(), messages: vi.fn(), enqueue: vi.fn(),
  handlers: [] as Array<(event: any) => void>,
  snapshot: null as AgentStatusPayload | null,
}))

vi.mock('@/api/client', () => ({ api: {
  agentSessions: mocks.sessions,
  agentMessages: mocks.messages,
  agentQueue: async () => [],
  agentEnqueue: mocks.enqueue,
  workspaces: async () => [],
  me: async () => ({ root: '/' }),
} }))
vi.mock('@/api/agent', () => ({ AgentWS: class {
  constructor(handler: (event: any) => void) { mocks.handlers.push(handler) }
  connect() {}
  close() {}
  subscribe(sessionId: string) {
    if (mocks.snapshot) mocks.handlers.forEach((handler) => handler({ type: 'message',
      event: { sessionId, kind: 'status', payload: mocks.snapshot },
    }))
  }
  unsubscribe() {}
  watch() {}
} }))

const SID = 's-long-task'
const CREATED = '2026-11-04T00:00:00Z'
function session(status: AgentSession['status'] = 'running'): AgentSession {
  return { id: SID, workspace: '/w', title: '长任务', app: 'claude', permissionMode: 'ask',
    status, createdAt: CREATED, updatedAt: CREATED }
}
function statusEvent(seq: number, state: string): AgentEvent {
  return { sessionId: SID, seq, kind: 'status', payload: { state }, createdAt: CREATED }
}

const live: Array<{ unmount: () => void }> = []
async function mountView() {
  const wrapper = mount({ components: { NMessageProvider, AgentView },
    template: '<n-message-provider><agent-view /></n-message-provider>',
  }, { global: { plugins: [createPinia()] } })
  live.push(wrapper)
  await flushPromises()
  const vm = wrapper.findComponent(AgentView).vm as any
  await vm.openSession(SID)
  await flushPromises()
  return { wrapper, vm }
}
async function fire(event: any) {
  mocks.handlers.forEach((handler) => handler(event))
  await flushPromises()
}

describe('会话列表和聊天详情共用当前状态', () => {
  beforeEach(() => {
    mocks.handlers.length = 0
    mocks.snapshot = null
    mocks.sessions.mockResolvedValue([session()])
    mocks.messages.mockResolvedValue([])
    mocks.enqueue.mockResolvedValue({ queued: true, item: { id: 1, text: '继续', createdAt: CREATED } })
  })
  afterEach(() => {
    live.splice(0).forEach((wrapper) => wrapper.unmount())
    vi.useRealTimers()
    vi.clearAllMocks()
  })

  it('长回合的开始事件超出最新 200 条时,仍显示运行、保留停止按钮并将消息入队', async () => {
    const tail = Array.from({ length: 200 }, (_, i): AgentEvent => ({
      sessionId: SID, seq: i + 3, kind: 'assistant_text', payload: `进度 ${i}`, createdAt: CREATED,
    }))
    mocks.messages.mockImplementation(async (_id, options) => options?.beforeSeq
      ? [statusEvent(1, 'idle'), statusEvent(2, 'running')] : tail)
    const { wrapper, vm } = await mountView()
    expect(wrapper.find('.sess-dot.running').exists()).toBe(true)
    expect(wrapper.findComponent(StatusBar).props('state')).toBe('running')
    expect(wrapper.findComponent(Composer).props('running')).toBe(true)
    expect(wrapper.find('.sb-verb').exists()).toBe(true)
    await vm.send('继续')
    await flushPromises()
    expect(mocks.enqueue).toHaveBeenCalledWith(SID, '继续')
    await vm.loadOlder()
    expect(wrapper.findComponent(StatusBar).props('state')).toBe('running')
  })

  it('历史里的旧 running 事件不覆盖当前空闲状态', async () => {
    mocks.sessions.mockResolvedValue([session('idle')])
    mocks.messages.mockResolvedValue([statusEvent(1, 'running')])
    const { wrapper } = await mountView()
    expect(wrapper.findComponent(StatusBar).props('state')).toBe('idle')
    expect(wrapper.findComponent(Composer).props('running')).toBe(false)
  })

  it('列表推送和新的回合状态消息同步更新两处状态,重复历史消息不回滚状态', async () => {
    mocks.messages.mockResolvedValue([statusEvent(1, 'running')])
    const { wrapper } = await mountView()
    await fire({ type: 'session', sessionId: SID, status: 'idle' })
    expect(wrapper.findComponent(StatusBar).props('state')).toBe('idle')
    await fire({ type: 'message', event: statusEvent(1, 'running') })
    expect(wrapper.findComponent(StatusBar).props('state')).toBe('idle')
    await fire({ type: 'message', event: statusEvent(2, 'running') })
    expect(wrapper.find('.sess-dot.running').exists()).toBe(true)
    expect(wrapper.findComponent(StatusBar).props('state')).toBe('running')
    await fire({ type: 'message', event: statusEvent(3, 'idle') })
    expect(wrapper.find('.sess-dot.idle').exists()).toBe(true)
    expect(wrapper.findComponent(StatusBar).props('state')).toBe('idle')
  })

  it('断线期间任务完成且没有补差消息时,重连仍恢复当前状态', async () => {
    const { wrapper } = await mountView()
    mocks.sessions.mockResolvedValue([session('idle')])
    await fire({ type: 'open' })
    expect(wrapper.find('.sess-dot.idle').exists()).toBe(true)
    expect(wrapper.findComponent(StatusBar).props('state')).toBe('idle')
  })

  it('订阅即恢复长回合计时,翻历史不会替换快照起点', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date(CREATED))
    const started = Date.now() - 65_000
    mocks.sessions.mockResolvedValue([session('idle')])
    mocks.snapshot = { state: 'running', turnStartedAt: started }
    const tail = Array.from({ length: 200 }, (_, i): AgentEvent => ({
      sessionId: SID, seq: i + 3, kind: 'assistant_text', payload: `进度 ${i}`, createdAt: CREATED,
    }))
    mocks.messages.mockImplementation(async (_id, options) => options?.beforeSeq
      ? [statusEvent(1, 'idle'), statusEvent(2, 'running')] : tail)
    const { wrapper, vm } = await mountView()
    expect(wrapper.find('.sess-dot.running').exists()).toBe(true)
    expect(wrapper.findComponent(Composer).props('running')).toBe(true)
    expect(wrapper.findComponent(StatusBar).props('turnStartedAt')).toBe(started)
    expect(wrapper.find('.sb-verb').text()).toContain('1m05s')
    expect(vm.messages.length).toBe(200)
    await vm.loadOlder()
    expect(wrapper.findComponent(StatusBar).props('turnStartedAt')).toBe(started)
    await vi.advanceTimersByTimeAsync(5000)
    expect(wrapper.find('.sb-verb').text()).toContain('1m10s')
  })

  it('退出重进与重连后沿用同一回合的起点,补差的旧状态不重置计时', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date(CREATED))
    const started = Date.now() - 65_000
    mocks.snapshot = { state: 'running', turnStartedAt: started }
    const first = await mountView()
    expect(first.wrapper.find('.sb-verb').text()).toContain('1m05s')
    first.wrapper.unmount()
    live.splice(live.indexOf(first.wrapper), 1)
    mocks.handlers.length = 0
    await vi.advanceTimersByTimeAsync(15_000)
    const { wrapper } = await mountView()
    expect(wrapper.find('.sb-verb').text()).toContain('1m20s')
    mocks.messages.mockImplementation(async (_id, options) => options?.afterSeq
      ? [] : [statusEvent(1, 'idle'), statusEvent(2, 'running')])
    await fire({ type: 'open' })
    await fire({ type: 'message', event: {
      sessionId: SID, kind: 'status', payload: { state: 'running', turnStartedAt: started },
    } })
    expect(wrapper.findComponent(StatusBar).props('turnStartedAt')).toBe(started)
    expect(wrapper.find('.sb-verb').text()).toContain('1m20s')
    await vi.advanceTimersByTimeAsync(5000)
    expect(wrapper.find('.sb-verb').text()).toContain('1m25s')
  })

  it('无序号的空闲快照收尾直播并停表,下一回合使用新起点', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date(CREATED))
    mocks.snapshot = { state: 'running', turnStartedAt: Date.now() - 65_000 }
    const { wrapper, vm } = await mountView()
    await fire({ type: 'message', event: {
      sessionId: SID, kind: 'reasoning_delta', payload: '还在思考',
    } })
    await fire({ type: 'message', event: statusEvent(1, 'running') })
    await fire({ type: 'message', event: {
      sessionId: SID, kind: 'status', seq: 0, payload: { state: 'idle' },
    } })
    expect(wrapper.findComponent(StatusBar).props('state')).toBe('idle')
    expect(wrapper.findComponent(StatusBar).props('turnStartedAt')).toBeUndefined()
    expect(wrapper.findComponent(Composer).props('running')).toBe(false)
    expect(vm.streamThink).toBe('')
    expect(vm.messages.length).toBe(1)
    const started = Date.now()
    await fire({ type: 'message', event: {
      sessionId: SID, kind: 'status', payload: { state: 'running', turnStartedAt: started },
    } })
    expect(wrapper.find('.sb-verb').text()).toContain('0s')
    await vi.advanceTimersByTimeAsync(3000)
    expect(wrapper.find('.sb-verb').text()).toContain('3s')
    await fire({ type: 'exit', sessionId: SID })
    expect(wrapper.findComponent(StatusBar).props('state')).toBe('dead')
    expect(wrapper.findComponent(StatusBar).props('turnStartedAt')).toBeUndefined()
  })

  it('新消息浮标位于标题栏内,点击后跳到底部并清空计数', async () => {
    const { wrapper, vm } = await mountView()
    vm.newMsgCount = 4
    await flushPromises()
    expect(wrapper.find('.chat-timeline .new-msg-pill').exists()).toBe(false)
    const pill = wrapper.find('.chat-head .new-msg-pill')
    expect(pill.text()).toContain('4 条新消息')
    await pill.trigger('click')
    expect(wrapper.find('.new-msg-pill').exists()).toBe(false)
  })
})
