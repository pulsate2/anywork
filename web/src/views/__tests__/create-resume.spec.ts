// 新建会话弹窗的「恢复已有会话」下拉框:列出当前目录的 CLI 原生会话,
// 选中后创建时带 resumeExternal + title(后端列表逻辑见 resumable_test.go)。
// 下拉的弹层(VFollower)在 happy-dom 里不渲染,选中动作直接对 NSelect 实例
// 发 update:value —— 验证的是 v-model 接线与创建参数,弹层交互浏览器自测。
import { describe, it, expect, vi, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { NMessageProvider, NSelect } from 'naive-ui'
import AgentView from '@/views/AgentView.vue'
import type { AgentEvent } from '@/api/client'

const mocks = vi.hoisted(() => ({
  resumable: vi.fn(),
  create: vi.fn(),
}))

vi.mock('@/api/client', () => {
  const sess = {
    id: 's-new', workspace: '/w', title: '', app: 'claude',
    status: 'idle', createdAt: '2026-09-10T06:00:00Z', updatedAt: '2026-09-10T06:00:00Z',
  }
  return {
    api: {
      agentSessions: async () => [sess],
      agentMessages: async () => [] as AgentEvent[],
      agentSend: async () => null,
      agentQueue: async () => [],
      agentEnqueue: async () => ({ queued: true, item: null, event: null }),
      agentQueueCancel: async () => ({ ok: true }),
      agentResumable: mocks.resumable,
      agentSessionCreate: mocks.create,
      agentInterrupt: async () => ({ ok: true }),
      agentApprove: async () => null,
      agentAnswer: async () => null,
      agentKill: async () => null,
      agentDelete: async () => null,
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

async function openModal() {
  const w = mount({
    components: { NMessageProvider, AgentView },
    template: '<n-message-provider><agent-view /></n-message-provider>',
  }, { global: { plugins: [createPinia()] } })
  await flushPromises()
  const vm = w.findComponent(AgentView).vm as any
  vm.createModal = true
  vm.createWorkspace = '/w/proj'
  await flushPromises()
  return { w, vm }
}

// 在"恢复已有会话"下拉框里选中一项:弹窗里有多个 n-select(工作区/权限),
// 按 options 里含目标 id 认。
function pickResume(w: { findAllComponents: (c: unknown) => any[] }, id: string) {
  const sel = w.findAllComponents(NSelect)
    .find((c: any) => (c.props('options') || []).some((o: any) => o.value === id))
  expect(sel, '恢复会话下拉框应已渲染').toBeTruthy()
  sel.vm.$emit('update:value', id)
}

describe('新建会话:下拉框恢复当前目录的已有会话', () => {
  // n-modal teleport 到 body:每个用例收尾清掉 body,避免上一个用例的
  // 弹窗 DOM 混进后续断言。
  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('列出会话 → 下拉选中 → 创建带 resumeExternal + title', async () => {
    mocks.resumable.mockResolvedValue([
      { id: 'ext-1', title: '终端里跑过的任务', app: 'claude', updatedAt: '2026-09-10T05:00:00Z' },
      { id: 'ext-2', title: '更早的会话', app: 'claude', updatedAt: '2026-09-09T05:00:00Z' },
    ])
    mocks.create.mockResolvedValue({
      id: 's-new', workspace: '/w', title: '终端里跑过的任务', app: 'claude',
      status: 'idle', createdAt: '2026-09-10T06:00:00Z', updatedAt: '2026-09-10T06:00:00Z',
    })
    const { w, vm } = await openModal()

    // 打开创建弹窗、选定目录 → 列表加载成下拉选项。
    expect(mocks.resumable).toHaveBeenCalledWith('claude', '/w/proj')
    expect(vm.resumeOptions.length).toBe(2)
    expect(vm.resumeOptions[0]).toMatchObject({ value: 'ext-1', label: '终端里跑过的任务' })

    // 下拉选中第一条:底部按钮切到"恢复会话"。
    pickResume(w, 'ext-1')
    await flushPromises()
    expect(vm.createResume).toBe('ext-1')

    // 创建:带 external id 与提炼的标题。
    await vm.createSession()
    await flushPromises()
    expect(mocks.create).toHaveBeenCalledWith(expect.objectContaining({
      resumeExternal: 'ext-1',
      title: '终端里跑过的任务',
    }))
  })

  it('目录切换会清掉已选并重拉列表', async () => {
    mocks.resumable.mockResolvedValue([{ id: 'ext-1', title: 'a', app: 'claude', updatedAt: '' }])
    mocks.create.mockClear()
    const { w, vm } = await openModal()
    pickResume(w, 'ext-1')
    await flushPromises()
    expect(vm.createResume).toBe('ext-1')

    // 换目录:选择清空,列表重新拉。
    vm.createWorkspace = '/w/two'
    await flushPromises()
    expect(vm.createResume).toBeNull()
    expect(mocks.resumable).toHaveBeenLastCalledWith('claude', '/w/two')
  })
})
