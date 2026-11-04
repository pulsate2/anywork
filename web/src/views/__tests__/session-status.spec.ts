import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { NMessageProvider } from 'naive-ui'
import AgentView from '@/views/AgentView.vue'
import Composer from '@/components/agent/Composer.vue'
import StatusBar from '@/components/agent/StatusBar.vue'
import type { AgentEvent, AgentSession } from '@/api/client'

const mocks = vi.hoisted(() => ({
  sessions: vi.fn(), messages: vi.fn(), enqueue: vi.fn(),
  handlers: [] as Array<(event: any) => void>,
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
  subscribe() {}
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
    mocks.sessions.mockResolvedValue([session()])
    mocks.messages.mockResolvedValue([])
    mocks.enqueue.mockResolvedValue({ queued: true, item: { id: 1, text: '继续', createdAt: CREATED } })
  })
  afterEach(() => { live.splice(0).forEach((wrapper) => wrapper.unmount()); vi.clearAllMocks() })

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
