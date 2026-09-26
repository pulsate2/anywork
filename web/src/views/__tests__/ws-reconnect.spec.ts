// WS 重连标识:断开(弱网 / 服务重启 / 切后台被断网)就地挂上顶部一条
// "连接已断开,正在重连…",接回来转"已重新连接"留 2 秒再收。
// AgentWS 换成可注入的桩:测试直接推 open/close 事件驱动状态机,不碰真连接。
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { NMessageProvider } from 'naive-ui'
import AgentView from '@/views/AgentView.vue'
import { AgentWS } from '@/api/agent'

vi.mock('@/api/client', () => {
  const sess = {
    id: 's-ws', workspace: '/w', title: 'ws', app: 'claude',
    status: 'idle', createdAt: '2026-09-26T12:00:00Z', updatedAt: '2026-09-26T12:00:00Z',
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

async function fire(e: any) {
  for (const h of MockWS.handlers) h(e)
  await flushPromises()
}

function mountView() {
  return mount({
    components: { NMessageProvider, AgentView },
    template: '<n-message-provider><agent-view /></n-message-provider>',
  }, { global: { plugins: [createPinia()] } })
}

// happy-dom 里 document.hidden 是只读的,重定义一份来模拟切后台/回前台。
function setHidden(v: boolean) {
  Object.defineProperty(document, 'hidden', { value: v, configurable: true })
}

describe('WS 重连标识', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    MockWS.handlers = []
    setHidden(false)
  })
  afterEach(() => vi.useRealTimers())

  it('断开显示"正在重连",接回来转"已重新连接"并在 2 秒后收掉', async () => {
    const w = mountView()
    await flushPromises()
    const banner = () => w.find('.ws-banner')

    // 首连(进页面)不挂标识:那会儿页面本来就空,catchUp 也会补上,
    // 只会闪一下没用的色条。
    await fire({ type: 'open' })
    expect(banner().exists()).toBe(false)

    // 断线:ws 自己按退避重连(最长 15 秒),这期间画面是彻底不动的,
    // 标识必须亮着,否则用户分不清是网断了还是 agent 卡了。
    await fire({ type: 'close' })
    expect(banner().classes()).toContain('down')
    expect(banner().text()).toContain('正在重连')

    // 接回来:立刻转"已重新连接",但别急着收 —— catchUp 刚把断档补上,
    // 这两秒是给用户看"时间线已经追平"的。
    await fire({ type: 'open' })
    expect(banner().classes()).toContain('back')
    expect(banner().text()).toContain('已重新连接')

    await vi.advanceTimersByTimeAsync(2000)
    expect(banner().exists()).toBe(false)
    w.unmount()
  })

  it('2 秒没走完又断一次:标识回到"正在重连",不会被先前的计时器收掉', async () => {
    const w = mountView()
    await flushPromises()
    const banner = () => w.find('.ws-banner')

    await fire({ type: 'close' })
    await fire({ type: 'open' })
    expect(banner().classes()).toContain('back')

    // 收尾计时器还没到点就又断了。
    await vi.advanceTimersByTimeAsync(1000)
    await fire({ type: 'close' })
    expect(banner().classes()).toContain('down')

    // 旧计时器到点(它被清过,不该把标识收掉),随后新连接上来再走完整流程。
    await vi.advanceTimersByTimeAsync(1000)
    expect(banner().classes()).toContain('down')
    await vi.advanceTimersByTimeAsync(1000)
    expect(banner().classes()).toContain('down')
    await fire({ type: 'open' })
    await vi.advanceTimersByTimeAsync(2000)
    expect(banner().exists()).toBe(false)
    w.unmount()
  })

  // 半开连接:切后台期间网断了,但 readyState 还报 OPEN、onclose 根本不触发,
  // 回来时是前端主动换连接。这条路径上没有 close 事件,标识得自己补 —— 后台
  // 那几十分钟的断档是实打实的。
  it('切后台回来主动换连接:没有 close 事件也要挂上标识', async () => {
    const w = mountView()
    await flushPromises()
    const banner = () => w.find('.ws-banner')

    setHidden(true)
    document.dispatchEvent(new Event('visibilitychange'))
    setHidden(false)
    document.dispatchEvent(new Event('visibilitychange'))
    await flushPromises()
    expect(banner().classes()).toContain('down')

    // 新连接建立(桩的 connect 不会自己推 open,这里手动推)
    await fire({ type: 'open' })
    expect(banner().classes()).toContain('back')
    await vi.advanceTimersByTimeAsync(2000)
    expect(banner().exists()).toBe(false)
    w.unmount()
  })
})
