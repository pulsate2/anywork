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
    await fire({ type: 'message', event: { sessionId: SID, kind: 'reasoning_delta', payload: '先理一下思路 **重点是并发**' } })
    const summary = () => w.find('.chat-row.reasoning summary').text()
    expect(w.find('.chat-row.reasoning').exists()).toBe(true)
    expect(summary()).toBe('思考中 · 1 秒')
    expect(w.find('.reasoning-body').text()).toContain('先理一下思路')
    // 思考正文也要走 markdown 渲染(此前是纯文本插值,星号照字面显示)
    expect(w.find('.reasoning-body').html()).toContain('<strong>')
    expect(w.find('.reasoning-body').text()).not.toContain('**')

    // 5 秒后:读数跟着跳(假时间驱动 setInterval 与 Date.now)
    await vi.advanceTimersByTimeAsync(5000)
    expect(summary()).toBe('思考中 · 5 秒')

    // 生成块收尾事件(思考段结束,正文开始)到达:直播卡清掉,秒表停。
    // 注意不是"任意持久化事件"都收尾 —— 子 agent 通知/running 状态见下一个用例。
    await fire({ type: 'message', event: { sessionId: SID, kind: 'assistant_text', payload: '结论是……', seq: 1 } })
    expect(w.find('.chat-row.reasoning').exists()).toBe(false)
    // 停表后时间再走,也不会有遗留的 setInterval(没有可断言的 DOM,只要
    // 不报错且卡片保持消失)
    await vi.advanceTimersByTimeAsync(3000)
    expect(w.find('.chat-row.reasoning').exists()).toBe(false)
    w.unmount()
  })

  it('退出重进:起点是首个增量,不再拿上一条持久化消息的时间当起点', async () => {
    const w = mount({
      components: { NMessageProvider, AgentView },
      template: '<n-message-provider><agent-view /></n-message-provider>',
    }, { global: { plugins: [createPinia()] } })
    await flushPromises()
    const vm = w.findComponent(AgentView).vm as any
    await vm.openSession(SID)
    await flushPromises()

    // 用户消息是 65 秒前落的,但那 65 秒里 agent 在跑工具 —— 思考是刚刚才开始
    // 的。拿上一条消息的时间当起点会一上来就从 1 分 5 秒跳起(用户:实际思考
    // 3 秒,你却从 8 秒开始),现在从首个增量这一刻起表。
    const t0 = new Date(Date.now() - 65_000).toISOString()
    await fire({ type: 'message', event: { sessionId: SID, kind: 'user', payload: '跑吧', seq: 1, createdAt: t0 } })
    await fire({ type: 'message', event: { sessionId: SID, kind: 'reasoning_delta', payload: '继续想' } })
    const summary = () => w.find('.chat-row.reasoning summary').text()
    expect(summary()).toBe('思考中 · 1 秒')
    await vi.advanceTimersByTimeAsync(3000)
    expect(summary()).toBe('思考中 · 3 秒')
    w.unmount()
  })

  // 退出立刻重进:增量是瞬态的、回放里没有,订阅时服务端补一份直播快照
  // (stream_snapshot)—— 正文要从头显示,秒表按已进行时长续上,不是归零重来。
  it('订阅快照:正文补全,秒表按已进行时长续上', async () => {
    const w = mount({
      components: { NMessageProvider, AgentView },
      template: '<n-message-provider><agent-view /></n-message-provider>',
    }, { global: { plugins: [createPinia()] } })
    await flushPromises()
    const vm = w.findComponent(AgentView).vm as any
    await vm.openSession(SID)
    await flushPromises()

    await fire({
      type: 'message',
      event: {
        sessionId: SID, kind: 'stream_snapshot', seq: 0, createdAt: '2026-09-15T12:00:00Z',
        payload: { text: '结论是……', think: '先理一下思路 **重点是并发**', thinkMs: 65_000 },
      },
    })
    const summary = () => w.find('.chat-row.reasoning summary').text()
    // 正文是按快照覆盖进来的(不是从半截开始,也不是追加)。
    expect(w.find('.reasoning-body').html()).toContain('<strong>')
    expect(summary()).toBe('思考中 · 1 分 5 秒')
    // 时间再走 5 秒:在已有基础上跳,不是从 1 秒重来。
    await vi.advanceTimersByTimeAsync(5000)
    expect(summary()).toBe('思考中 · 1 分 10 秒')
    w.unmount()
  })

  // 子 agent 通知(task_notification)/API 重试进度常在一段思考进行中穿插到达。
  // 它们不结束当前生成块:清了缓冲区,下一个增量会以"刚刚"为起点重新起表,
  // 读数就永远停在 1 秒 —— 子 agent 跑着时通知每 1-2 秒一条,秒表等于死了。
  it('子 agent 通知穿插到达不打断秒表,更不会把读数重置回 1 秒', async () => {
    const w = mount({
      components: { NMessageProvider, AgentView },
      template: '<n-message-provider><agent-view /></n-message-provider>',
    }, { global: { plugins: [createPinia()] } })
    await flushPromises()
    const vm = w.findComponent(AgentView).vm as any
    await vm.openSession(SID)
    await flushPromises()

    const summary = () => w.find('.chat-row.reasoning summary').text()
    await fire({ type: 'message', event: { sessionId: SID, kind: 'reasoning_delta', payload: '先理思路' } })
    expect(summary()).toBe('思考中 · 1 秒')
    await vi.advanceTimersByTimeAsync(4000)
    expect(summary()).toBe('思考中 · 4 秒')

    // 子 agent 通知(不结束生成块):直播卡不消失、读数不清零
    await fire({
      type: 'message',
      event: { sessionId: SID, kind: 'system_info', payload: { type: 'task_notification', text: '子任务完成' }, seq: 1 },
    })
    expect(w.find('.chat-row.reasoning').exists()).toBe(true)
    expect(summary()).toBe('思考中 · 4 秒')

    // running 状态同理:它落在回合开始,不是生成块的边界
    await fire({ type: 'message', event: { sessionId: SID, kind: 'status', payload: { state: 'running' }, seq: 2 } })
    expect(summary()).toBe('思考中 · 4 秒')

    // 通知之后的增量:接着原来的起点走,不是重新从 1 秒起
    await fire({ type: 'message', event: { sessionId: SID, kind: 'reasoning_delta', payload: '继续想' } })
    await vi.advanceTimersByTimeAsync(2000)
    expect(summary()).toBe('思考中 · 6 秒')

    // 真正的收尾事件(工具调用)照旧结束它
    await fire({ type: 'message', event: { sessionId: SID, kind: 'tool_call', payload: { tool: 'Bash', toolUseId: 't1' }, seq: 3 } })
    expect(w.find('.chat-row.reasoning').exists()).toBe(false)
    w.unmount()
  })

  // 服务端在思考段收尾时把实测跨度记进 payload.durationMs(见 internal/agent
  // 的 pump):前端直接用,不再按相邻事件的 created_at 推算 —— 那条推算链在
  // 秒精度和穿插的子 agent 通知面前都不可靠。
  it('落库的 reasoning 带 durationMs:直接用实测耗时,不走时间戳推算', async () => {
    const w = mount({
      components: { NMessageProvider, AgentView },
      template: '<n-message-provider><agent-view /></n-message-provider>',
    }, { global: { plugins: [createPinia()] } })
    await flushPromises()
    const vm = w.findComponent(AgentView).vm as any
    await vm.openSession(SID)
    await flushPromises()

    // 与上一条事件同秒落库:按推算只能得到 0(兜底 1 秒),实测值是 7 秒。
    const t = '2026-09-15T12:00:00Z'
    await fire({ type: 'message', event: { sessionId: SID, kind: 'user', payload: '想想', seq: 1, createdAt: t } })
    await fire({
      type: 'message',
      event: {
        sessionId: SID, kind: 'reasoning', seq: 2, createdAt: t,
        payload: { text: '想了一会儿', durationMs: 7000 },
      },
    })
    expect(w.find('.chat-row.reasoning summary').text()).toBe('思考过程 · 7 秒')
    // 正文照旧从 payload.text 取,不能把对象名当正文渲染。
    expect(w.find('.reasoning-body').text()).toContain('想了一会儿')
    w.unmount()
  })

  // created_at 是秒精度(RFC3339),短思考常与上一条事件同秒落库 → 差为 0。
  // 旧实现把 0 当真值判空,卡片只剩"思考过程"四个字(实测 103 条里踩中 13 条)。
  it('思考与上一条事件同秒:仍要报出耗时(兜底 1 秒),不能整段不显示', async () => {
    const w = mount({
      components: { NMessageProvider, AgentView },
      template: '<n-message-provider><agent-view /></n-message-provider>',
    }, { global: { plugins: [createPinia()] } })
    await flushPromises()
    const vm = w.findComponent(AgentView).vm as any
    await vm.openSession(SID)
    await flushPromises()

    const t = '2026-09-15T12:00:00Z' // 同一秒:用户消息与思考块一起落库
    await fire({ type: 'message', event: { sessionId: SID, kind: 'user', payload: '看看这个', seq: 1, createdAt: t } })
    await fire({ type: 'message', event: { sessionId: SID, kind: 'reasoning', payload: '很短的一段思考', seq: 2, createdAt: t } })
    expect(w.find('.chat-row.reasoning summary').text()).toBe('思考过程 · 1 秒')

    // 有真实间隔时照常报真实耗时
    await fire({
      type: 'message',
      event: { sessionId: SID, kind: 'reasoning', payload: '想了一会儿', seq: 3, createdAt: '2026-09-15T12:00:42Z' },
    })
    const summaries = w.findAll('.chat-row.reasoning summary').map((x) => x.text())
    expect(summaries[summaries.length - 1]).toBe('思考过程 · 42 秒')
    w.unmount()
  })
})
