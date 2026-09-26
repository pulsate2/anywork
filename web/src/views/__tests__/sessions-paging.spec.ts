// 会话列表分页:每个工作区先只拉一页(后端默认 10 条),分组底部"加载更多"
// 再翻这个目录更早的一页 —— 会话攒久了不再一次性全拉下来。
import { describe, it, expect, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { NMessageProvider } from 'naive-ui'
import AgentView from '@/views/AgentView.vue'

// agentSessions 收到的参数与假数据:工厂里用不了外面声明的变量,vi.hoisted 先立好。
const h = vi.hoisted(() => {
  const calls: Array<Record<string, unknown>> = []
  const mk = (prefix: string, ws: string, n: number) =>
    Array.from({ length: n }, (_, i) => ({
      id: `${prefix}${i}`, app: 'claude', workspace: ws, title: `${prefix}${i}`,
      status: 'idle', createdAt: '2026-09-01T00:00:00Z',
      // 序号越小越新:服务端按 updated_at 倒序给页。
      updatedAt: `2026-09-01T00:00:${String(60 - i).padStart(2, '0')}Z`,
    }))
  const big = mk('b', '/big', 15)
  const small = mk('s', '/small', 2)
  return { calls, big, small }
})

vi.mock('@/api/client', () => ({
  api: {
    // 不带 workspace = 每个目录一页;带 workspace = 该目录的下一页。
    agentSessions: async (p: Record<string, unknown> = {}) => {
      h.calls.push(p)
      const limit = typeof p.limit === 'number' ? p.limit : 10
      if (p.workspace) return h.big.slice(Number(p.offset) || 0, (Number(p.offset) || 0) + limit)
      return [...h.big.slice(0, limit), ...h.small]
    },
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
    agentSessionCreate: async () => null,
    agentSettings: async () => ({}),
    agentCleanupStatus: async () => ({}),
    agentCleanupRun: async () => ({}),
    agentCleanupSave: async () => ({}),
    agentFileUrl: (id: string, n: string) => `/api/agent/sessions/${id}/files/${n}`,
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

describe('会话列表分页', () => {
  it('每个工作区先拉一页,"加载更多"再翻下一页', async () => {
    const w = mount({
      components: { NMessageProvider, AgentView },
      template: '<n-message-provider><agent-view /></n-message-provider>',
    }, { global: { plugins: [createPinia()] } })
    await flushPromises()

    // 首屏:每个目录各一页(/big 10 条 + /small 2 条),不是全量 17 条。
    expect(h.calls[0]).toEqual({ limit: 10 })
    expect(w.findAll('.sess-card')).toHaveLength(12)

    // 满一页的组(还有更早的)才有"加载更多";/small 不满一页,没有。
    expect(w.findAll('.sess-more')).toHaveLength(1)

    // 折叠徽标带 "+":这个目录里还有没拉下来的。
    const heads = w.findAll('.sess-group-head')
    expect(heads[0].text()).toContain('/big')
    await heads[0].trigger('click')
    expect(w.findAll('.sess-count')[0].text()).toBe('10+')
    await heads[0].trigger('click')

    // 翻下一页:按 workspace+offset 拉,组里接着长(10+5 条 + /small 的 2 条)。
    await w.find('.sess-more').trigger('click')
    await flushPromises()
    expect(h.calls[1]).toEqual({ workspace: '/big', offset: 10, limit: 10 })
    expect(w.findAll('.sess-card')).toHaveLength(17)
    // 翻到不满一页 = 到底,按钮收掉。
    expect(w.findAll('.sess-more')).toHaveLength(0)
    w.unmount()
  })
})
