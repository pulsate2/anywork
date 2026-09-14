// codex 并行 Bash 调用的审批配对:审批 reqId 就是工具卡的 toolUseId(同一
// itemId)。旧逻辑"往前找 3 张同工具 running 卡"在并行时会张冠李戴,4 条
// 以上时配不上,单开出一张审批卡(bash 卡 + 审核卡两张)。
import { describe, it, expect, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { NMessageProvider } from 'naive-ui'
import AgentView from '@/views/AgentView.vue'
import type { AgentEvent } from '@/api/client'

vi.mock('@/api/client', () => {
  const sess = {
    id: 's-par', workspace: '/w', title: 'par', app: 'codex',
    status: 'running', createdAt: '2026-09-14T06:00:00Z', updatedAt: '2026-09-14T06:00:00Z',
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
    connect() { this.connected = true }
    close() { this.connected = false }
    subscribe() {}
    unsubscribe() {}
    watch() {}
  },
}))

function ev(seq: number, kind: string, payload: unknown): AgentEvent {
  return { seq, kind, payload, sessionId: 's-par', createdAt: new Date().toISOString() } as AgentEvent
}

async function mountView() {
  const w = mount({
    components: { NMessageProvider, AgentView },
    template: '<n-message-provider><agent-view /></n-message-provider>',
  }, { global: { plugins: [createPinia()] } })
  await flushPromises()
  const vm = w.findComponent(AgentView).vm as any
  await vm.openSession('s-par')
  await flushPromises()
  return vm
}

describe('codex 并行调用的审批配对', () => {
  it('reqId=toolUseId 精确配对:四条并行 Bash,审批各归各,不单开审核卡', async () => {
    const vm = await mountView()
    // codex 实测形态(用户库 2026-09-07 会话):多条 item/started 先到,
    // requestApproval 随后集中到达,顺序与 started 无关。
    const cmds: Array<[string, string]> = [
      ['exec-a', 'git status --short --branch'],
      ['exec-b', 'pwd'],
      ['exec-c', 'rg --files'],
      ['exec-d', 'ls -la'],
    ]
    for (const [id, cmd] of cmds) {
      vm.messages.push(ev(cmds.findIndex(([i]) => i === id) + 1, 'tool_call', {
        tool: 'Bash', toolUseId: id, args: JSON.stringify({ command: `/bin/bash -lc '${cmd}'` }), state: 'running',
      }))
    }
    // 审批倒序到达(最坏的配对顺序)。
    let seq = 10
    for (const [id, cmd] of [...cmds].reverse()) {
      vm.messages.push(ev(seq++, 'permission_request', {
        reqId: id, tool: 'Bash', args: `/bin/bash -lc '${cmd}'`,
      }))
    }
    await flushPromises()

    const cards = vm.cards as any[]
    // 不该有任何单开的审批卡(bash 卡 + 审核卡两张的 bug 形态)。
    expect(cards.filter((c) => c.kind === 'approval')).toHaveLength(0)
    // 每张工具卡挂的是"自己那条命令"的审批:reqId === toolUseId。
    const toolCards = cards.filter((c) => c.kind === 'tool' && c.call?.tool === 'Bash')
    expect(toolCards).toHaveLength(4)
    for (const c of toolCards) {
      expect(c.pendingReq?.reqId).toBe(c.call.toolUseId)
      expect(c.pendingReq?.args).toContain(JSON.parse(c.call.args).command)
    }
  })

  it('claude 式审批(reqId 与 toolUseId 无关)仍走启发式嵌入', async () => {
    const vm = await mountView()
    vm.messages.push(ev(1, 'tool_call', { tool: 'Bash', toolUseId: 'tu_b', args: '{"command":"ls"}', state: 'running' }))
    vm.messages.push(ev(2, 'permission_request', { reqId: 'pr1', tool: 'Bash', args: 'ls' }))
    await flushPromises()

    const cards = vm.cards as any[]
    expect(cards.filter((c) => c.kind === 'approval')).toHaveLength(0)
    const bash = cards.find((c) => c.kind === 'tool' && c.call?.tool === 'Bash')
    expect(bash?.pendingReq?.reqId).toBe('pr1')
  })
})
