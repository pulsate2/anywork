// 复现 #17 的实时形态:live 回合里审批事件让卡片在 单卡↔组卡 之间结构变化,
// 已打开的详情弹窗跟着被卸载(点开就没了)。
import { describe, it, expect, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { NMessageProvider } from 'naive-ui'
import AgentView from '@/views/AgentView.vue'
import type { AgentEvent } from '@/api/client'

vi.mock('@/api/client', () => {
  const sess = {
    id: 's-live', workspace: '/w', title: 'live', app: 'claude',
    status: 'running', createdAt: '2026-09-10T06:00:00Z', updatedAt: '2026-09-10T06:00:00Z',
  }
  return {
    api: {
      agentSessions: async () => [sess],
      agentMessages: async () => [] as AgentEvent[],
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
    // 把 WS 推送暴露出来,测试直接灌事件
    onEvent: (e: any) => void = () => {}
    connect() { this.connected = true }
    close() { this.connected = false }
    subscribe() {}
    unsubscribe() {}
    watch() {}
  },
}))

function ev(seq: number, kind: string, payload: unknown): AgentEvent {
  return { seq, kind, payload, sessionId: 's-live', createdAt: new Date().toISOString() } as AgentEvent
}

describe('live 回合:审批往返时弹窗存活', () => {
  const SID = 's-live'
  it('审批答复后 bash 卡并回组卡,详情弹窗不应消失', async () => {
    const w = mount({
      components: { NMessageProvider, AgentView },
      template: '<n-message-provider><agent-view /></n-message-provider>',
    }, { global: { plugins: [createPinia()] } })
    await flushPromises()
    const vm = w.findComponent(AgentView).vm as any
    await vm.openSession(SID)
    await flushPromises()

    // 用 AgentWS 实例的构造回调抓 onEvent:组件里 new AgentWS(cb),
    // mock 类把 cb 存到实例 —— 但组件拿不到实例引用,所以从 vm 里找。
    // 更直接:vm.messages 是 ref,直接 push(等效于 WS 推送路径的 messages.value.push)。
    const push = (e: AgentEvent) => { vm.messages.push(e) }

    // 1. Read + Bash 两个 tool_use → 一张组卡
    push(ev(1, 'tool_call', { tool: 'Read', toolUseId: 'tu_r', args: '{"file_path":"/w/a.txt"}', state: 'running' }))
    push(ev(2, 'tool_call', { tool: 'Bash', toolUseId: 'tu_b', args: '{"command":"ls -la /w"}', state: 'running' }))
    await flushPromises()
    expect(w.findAll('.group-head').length).toBe(1) // [Read, Bash] 一组

    // 2. bash 的审批请求到达 → bash 卡拆出组,单卡带审批
    push(ev(3, 'permission_request', { reqId: 'pr1', tool: 'Bash', input: '{"command":"ls -la /w"}', reason: '需要批准' }))
    await flushPromises()
    const heads = w.findAll('.tool-head')
    // 组被拆散:Read 与 Bash 都变回单卡,各自有 .tool-head
    expect(w.findAll('.group-head').length).toBe(0)
    expect(heads.length).toBe(2)
    // 3. 点开 bash 卡详情
    const bashHead = heads.find((h) => h.text().includes('Bash'))!
    await bashHead.trigger('click')
    await flushPromises()
    let detail = document.body.querySelector('.tool-detail')
    expect(detail?.textContent).toContain('ls -la /w')

    // 4. 用户点了允许 → permission_result → bash 并回组卡(单卡被组卡替换)。
    // 弹窗在 AgentView 层,不应随卡片重组消失。
    push(ev(4, 'permission_result', { reqId: 'pr1', allow: true }))
    await flushPromises()
    detail = document.body.querySelector('.tool-detail')
    expect(detail?.textContent).toContain('ls -la /w')

    // 5. 结果晚到:tool_result 回填进(已并回组的)bash 调用 —— 弹窗按
    // toolUseId 拿"活"对象,结果应出现在仍开着的弹窗里。
    push(ev(5, 'tool_call', { tool: 'Bash', toolUseId: 'tu_b', result: 'done', state: 'ok' }))
    await flushPromises()
    detail = document.body.querySelector('.tool-detail')
    expect(detail?.textContent).toContain('done')
  })
})
