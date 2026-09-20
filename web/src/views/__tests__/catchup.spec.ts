// 手机切后台一段时间再回来:后台期间丢掉的推送远超一页,补差必须循环拉到追平;
// 而且半开连接(readyState 还报 OPEN)不重连的话,时间线会永远停在切走那一刻。
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { NMessageProvider } from 'naive-ui'
import AgentView from '@/views/AgentView.vue'
import type { AgentEvent } from '@/api/client'

const SID = 's-catchup'
// 时间线本来看到 seq 1..500(切走那一刻),后台期间又跑了 1000 条(501..1500)。
const SEEN = 500
const MISSED = 1000
const TOTAL = SEEN + MISSED
const ALL: AgentEvent[] = Array.from({ length: TOTAL }, (_, i) => ({
  seq: i + 1, kind: 'user', payload: `m${i + 1}`, sessionId: SID, createdAt: '2026-09-19T00:00:00Z',
}) as AgentEvent)

const calls: Array<{ afterSeq?: number; beforeSeq?: number; limit?: number }> = []
let connectCount = 0

vi.mock('@/api/client', () => {
  const sess = {
    id: 's-catchup', workspace: '/w', title: 'catchup', app: 'claude',
    status: 'running', createdAt: '2026-09-19T00:00:00Z', updatedAt: '2026-09-19T00:00:00Z',
  }
  return {
    api: {
      agentSessions: async () => [sess],
      // 仿真服务端语义:afterSeq 增量补差;limit 缺省/越界一律回落 200。
      agentMessages: async (_id: string, opts: any = {}) => {
        calls.push(opts)
        const limit = opts.limit > 0 && opts.limit <= 1000 ? opts.limit : 200
        if (opts.afterSeq) return ALL.filter((e) => e.seq > opts.afterSeq).slice(0, limit)
        if (opts.beforeSeq) return ALL.filter((e) => e.seq < opts.beforeSeq).slice(-limit)
        return ALL.slice(-limit) // 最新一页
      },
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

vi.mock('@/api/agent', () => ({
  AgentWS: class {
    connected = false
    private onEvent: (e: any) => void
    constructor(onEvent: (e: any) => void) { this.onEvent = onEvent }
    connect() { this.connected = true; connectCount++; this.onEvent({ type: 'open' }) }
    close() { this.connected = false }
    subscribe() { this.connected = true }
    unsubscribe() {}
    watch() {}
  },
}))

// 挂载的视图要在用例之间卸掉:每个 AgentView 都会在 document 上挂
// visibilitychange 监听,留着的话一次事件会把前面所有用例的实例都叫起来。
const live: Array<{ unmount: () => void }> = []

function mountView() {
  const w = mount({
    components: { NMessageProvider, AgentView },
    template: '<n-message-provider><agent-view /></n-message-provider>',
  }, { global: { plugins: [createPinia()] } })
  live.push(w)
  return w.findComponent(AgentView).vm as any
}

// happy-dom 里 document.hidden 是只读的,重定义一份来模拟切后台/回前台。
function setHidden(v: boolean) {
  Object.defineProperty(document, 'hidden', { value: v, configurable: true })
}

describe('切后台回来的补差', () => {
  beforeEach(() => { calls.length = 0; connectCount = 0; setHidden(false) })
  afterEach(() => { for (const w of live.splice(0)) w.unmount() })

  it('打开会话只看到最新一页,切走时停在第 500 条', async () => {
    const vm = mountView()
    await flushPromises()
    await vm.openSession(SID)
    await flushPromises()
    // 打开时只拉最新 200 条,时间线的头就是这个上限(更早的靠「加载更早」翻)。
    expect(vm.messages.length).toBe(200)
  })

  it('补差要一页一页拉到底,不能只补一页就停', async () => {
    const vm = mountView()
    await flushPromises()
    await vm.openSession(SID)
    await flushPromises()

    // 造出「切走时只看到第 500 条」的现场:截断到切走那一刻。
    vm.messages = ALL.slice(0, SEEN)
    calls.length = 0

    await vm.catchUp()
    await flushPromises()

    // 1000 条丢的推送:一次补满一页(1000),第二次空页收尾。
    expect(calls[0].afterSeq).toBe(SEEN)
    expect(calls[0].limit).toBe(1000)
    expect(calls.length).toBe(2)
    // 断线期间的消息一条不落,且时间线追平到最新。
    expect(vm.messages.length).toBe(TOTAL)
    expect(vm.messages[vm.messages.length - 1].seq).toBe(TOTAL)
  })

  // 只有"真的被切到后台过"才拆连接重连:半开连接(readyState 仍报 OPEN)只有
  // 换掉它才能靠 seq 重放追平。模拟真实的 hidden → visible 序列。
  it('切后台再回来要强制换新连接,并发起补差', async () => {
    const vm = mountView()
    await flushPromises()
    await vm.openSession(SID)
    await flushPromises()

    const before = connectCount
    setHidden(true)
    document.dispatchEvent(new Event('visibilitychange'))
    setHidden(false)
    document.dispatchEvent(new Event('visibilitychange'))
    await flushPromises()
    expect(connectCount).toBe(before + 1)
    expect(calls.some((c) => c.afterSeq !== undefined)).toBe(true)

    // 1 秒内重复触发(visibilitychange + focus 一起来)只当一次。
    window.dispatchEvent(new Event('focus'))
    await flushPromises()
    expect(connectCount).toBe(before + 1)
  })

  // 桌面 Alt-Tab 只是窗口失焦,页面从没隐藏过 —— 连接好着,不该拆了重连。
  it('只是窗口失焦又回来时不重连,只补差', async () => {
    const vm = mountView()
    await flushPromises()
    await vm.openSession(SID)
    await flushPromises()

    const before = connectCount
    window.dispatchEvent(new Event('focus'))
    await flushPromises()
    expect(connectCount).toBe(before)
    expect(calls.some((c) => c.afterSeq !== undefined)).toBe(true)
  })
})
