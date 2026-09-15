// 子 agent 的审批:driver 按 sidechain 待批的 tool_use 反查出宿主
// (parentToolUseId),前端把审批嵌进宿主 Task 卡内问答 —— 不再单开一张
// 审批卡占主线,答复(permission_result)一到大线就干净了。宿主卡不在的
// 兜底单开一张(待答时得留着能答),答完撤出。
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { NMessageProvider } from 'naive-ui'
import AgentView from '@/views/AgentView.vue'
import { AgentWS } from '@/api/agent'

vi.mock('@/api/client', () => {
  const sess = {
    id: 's-sub', workspace: '/root/gittest2', title: 'subagent', app: 'claude',
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

// AgentWS 换成可注入的桩(同 think-timer.spec):类要有本地名,工厂里
// 引用 import 的 AgentWS 会被 vue-tsc 按真实类型检查。
vi.mock('@/api/agent', () => {
  class MockAgentWS {
    static handlers: Array<(e: any) => void> = []
    connected = false
    constructor(handler: (e: any) => void) { MockAgentWS.handlers.push(handler) }
    connect() { this.connected = true }
    close() { this.connected = false }
    subscribe() {} unsubscribe() {} watch() {}
  }
  return { AgentWS: MockAgentWS }
})
const MockWS = AgentWS as unknown as { handlers: Array<(e: any) => void> }

const SID = 's-sub'
let seq = 0
async function fire(kind: string, payload: unknown) {
  for (const h of MockWS.handlers) h({ type: 'message', event: { sessionId: SID, kind, payload, seq: ++seq } })
  await flushPromises()
}

async function setup() {
  const w = mount({
    components: { NMessageProvider, AgentView },
    template: '<n-message-provider><agent-view /></n-message-provider>',
  }, { global: { plugins: [createPinia()] } })
  await flushPromises()
  const vm = w.findComponent(AgentView).vm as any
  await vm.openSession(SID)
  await flushPromises()
  return w
}

describe('子 agent 审批:嵌入宿主 Task 卡', () => {
  beforeEach(() => {
    MockWS.handlers = []
    seq = 0
  })

  it('带 parentToolUseId 的审批嵌进 Task 卡内(带命令),主线不出审批行;答复后隐掉', async () => {
    const w = await setup()
    await fire('tool_call', { tool: 'Task', toolUseId: 't-host', args: '{"description":"跑测试"}', state: 'running' })
    await fire('tool_call', { tool: 'Bash', toolUseId: 't-sub1', parentToolUseId: 't-host', args: '{"command":"go test ./..."}', state: 'running' })
    await fire('permission_request', { reqId: 'r1', tool: 'Bash', args: '{"command":"go test ./..."}', parentToolUseId: 't-host' })

    // 主线没有单开的审批行;审批嵌在 Task 卡里,且带着命令摘要
    // (宿主卡头显示的是 Task 描述,命令只有审批里能看到)。
    expect(w.findAll('.chat-row.approval').length).toBe(0)
    const approval = w.find('.chat-row.tool .tool-approval')
    expect(approval.exists()).toBe(true)
    expect(approval.text()).toContain('go test ./...')
    expect(approval.text()).toContain('等待你的决定')

    // 答复到达:内嵌审批隐掉,Task 卡还在(过程照旧),主线依旧干净。
    await fire('permission_result', { reqId: 'r1', allow: true, session: false })
    expect(w.find('.tool-approval').exists()).toBe(false)
    expect(w.findAll('.chat-row.approval').length).toBe(0)
    expect(w.findAll('.chat-row.tool').length).toBe(1)
    w.unmount()
  })

  it('宿主卡不在(没赶上登记)时单开审批行;答复完撤出主线', async () => {
    const w = await setup()
    await fire('permission_request', { reqId: 'r9', tool: 'Bash', args: '{"command":"ls /"}', parentToolUseId: 't-gone' })
    // 待答的单开卡得留着 —— 否则没地方按允许。
    expect(w.findAll('.chat-row.approval').length).toBe(1)
    await fire('permission_result', { reqId: 'r9', allow: false, session: false })
    // 答完即撤,不占主线。
    expect(w.findAll('.chat-row.approval').length).toBe(0)
    w.unmount()
  })

  it('同卡第二条审批接续:前一条答完,新审批替换显示', async () => {
    const w = await setup()
    await fire('tool_call', { tool: 'Task', toolUseId: 't-host', args: '{"description":"构建"}', state: 'running' })
    await fire('permission_request', { reqId: 'r1', tool: 'Bash', args: '{"command":"npm run build"}', parentToolUseId: 't-host' })
    await fire('permission_result', { reqId: 'r1', allow: true, session: false })
    // 第二条同宿主的审批:前一条已隐,这张顶上(不是并排两张)。
    await fire('permission_request', { reqId: 'r2', tool: 'Bash', args: '{"command":"npm run test"}', parentToolUseId: 't-host' })
    const approvals = w.findAll('.tool-approval')
    expect(approvals.length).toBe(1)
    expect(approvals[0].text()).toContain('npm run test')
    w.unmount()
  })
})
