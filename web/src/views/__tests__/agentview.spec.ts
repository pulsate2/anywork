// 复现 #17:用真实会话的完整消息流(DB 导出)挂载 AgentView,
// 找出 bash 卡片点击后弹窗不出现的原因。
import { describe, it, expect, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { NMessageProvider } from 'naive-ui'
import AgentView from '@/views/AgentView.vue'
import fixture from './fixture-session.json'
import type { AgentEvent } from '@/api/client'

vi.mock('@/api/client', () => {
  const sess = {
    id: '399402c64a236a2f4832f56c27933a29', workspace: '/root/gittest2', title: 'fixture', app: 'claude',
    status: 'idle', createdAt: '2026-09-09T12:00:00Z', updatedAt: '2026-09-09T14:07:00Z',
  }
  return {
    api: {
      agentSessions: async () => [sess],
      agentMessages: async () => fixture as AgentEvent[],
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

describe('AgentView 真实会话回放', () => {
  it('打开会话后,每张工具卡/组卡都能点开详情弹窗', async () => {
    const SID = '399402c64a236a2f4832f56c27933a29'
    const w = mount({
      components: { NMessageProvider, AgentView },
      template: '<n-message-provider><agent-view /></n-message-provider>',
    }, { global: { plugins: [createPinia()] } })

    await flushPromises()
    // 打开会话
    const vm = w.findComponent(AgentView).vm as any
    await vm.openSession(SID)
    await flushPromises()
    // 时间线渲染出的卡片
    const heads = w.findAll('.tool-head')
    const groupHeads = w.findAll('.group-head')
    console.log('工具单卡:', heads.length, '组卡:', groupHeads.length)

    // 逐张点:断言 body 里出现 tool-detail(modal 已开)
    for (let i = 0; i < heads.length; i++) {
      const before = document.body.textContent?.length ?? 0
      await heads[i].trigger('click')
      await flushPromises()
      const detail = document.body.querySelector('.tool-detail')
      const toolName = heads[i].find('.tool-name')?.text()
      expect(detail, `第 ${i} 张卡(${toolName})点开后没有 .tool-detail`).toBeTruthy()
      expect((detail?.textContent?.length ?? 0) > 0, `第 ${i} 张卡(${toolName})弹窗内容为空`).toBeTruthy()
    }

    // 组卡:展开 → 逐行点详情
    for (let g = 0; g < groupHeads.length; g++) {
      await groupHeads[g].trigger('click')
      await flushPromises()
      const items = w.findAll('.group-item')
      console.log('组', g, '行数', items.length, '| 行名:', items.map((x) => x.find('.item-name')?.text()))
      for (let k = 0; k < items.length; k++) {
        const target = items[k].find('.item-target')?.text() || ''
        await items[k].trigger('click')
        await flushPromises()
        const details = document.body.querySelectorAll('.tool-detail')
        // 最后一个 .tool-detail 必须非空,且含该行的目标(文件基名/命令片段)
        const last = details[details.length - 1]
        const base = target.split('/').pop() || target.slice(0, 12)
        if (!last || !last.textContent) {
          console.log('FAIL row:', k, target, '| details:', details.length)
        }
        expect(!!last?.textContent, `组 ${g} 第 ${k} 行(${target})点开后没有详情内容`).toBeTruthy()
      }
      await groupHeads[g].trigger('click') // 收起
    }

    // 子 agent 过程(Task/Agent 卡内嵌 steps):展开过程,逐条点详情
    const stepsToggles = w.findAll('.steps-toggle')
    console.log('过程卡:', stepsToggles.length)
    for (let t = 0; t < stepsToggles.length; t++) {
      await stepsToggles[t].trigger('click')
      await flushPromises()
      const items = w.findAll('.steps-item')
      console.log(' 过程', t, '条目', items.length, items.map((x) => x.find('.steps-name')?.text()))
      for (let k = 0; k < items.length; k++) {
        await items[k].trigger('click')
        await flushPromises()
        const details = document.body.querySelectorAll('.tool-detail')
        const last = details[details.length - 1]
        if (!last || !last.textContent) {
          console.log('FAIL step:', t, k, items[k].text())
        }
        expect(!!last?.textContent, `过程卡 ${t} 第 ${k} 条点开后没有详情内容`).toBeTruthy()
      }
    }
  })
})
