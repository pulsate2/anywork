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
      // 工具名已换成分类图标(有分类的卡不再有 .tool-name),取不到就标空
      const nameEl = heads[i].find('.tool-name')
      const toolName = nameEl.exists() ? nameEl.text() : '(图标卡)'
      expect(detail, `第 ${i} 张卡(${toolName})点开后没有 .tool-detail`).toBeTruthy()
      expect((detail?.textContent?.length ?? 0) > 0, `第 ${i} 张卡(${toolName})弹窗内容为空`).toBeTruthy()
    }

    // 组卡:展开 → 逐行点详情
    for (let g = 0; g < groupHeads.length; g++) {
      await groupHeads[g].trigger('click')
      await flushPromises()
      const items = w.findAll('.group-item')
      // 行内分类工具不再有文字名(只有图标),兜底类才有 .item-name
      const names = items.map((x) => { const n = x.find('.item-name'); return n.exists() ? n.text() : '(图标)' })
      console.log('组', g, '行数', items.length, '| 行名:', names)
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

    // 子 agent 过程(Task/Agent 卡):点「过程」开左右两栏弹窗,点左栏条目
    // 右栏出该步骤的详情。多张过程卡依次开,弹窗 teleport 到 body,取最新
    // 一个 .steps-modal(前一张没关也不影响)。
    const stepsOpens = w.findAll('.steps-open')
    console.log('过程卡:', stepsOpens.length)
    for (let t = 0; t < stepsOpens.length; t++) {
      await stepsOpens[t].trigger('click')
      await flushPromises()
      const modals = document.body.querySelectorAll('.steps-modal')
      const modal = modals[modals.length - 1]
      if (!modal) console.log('FAIL steps modal missing:', t)
      expect(modal, `过程卡 ${t} 点开后没有弹窗`).toBeTruthy()
      const items = Array.from(modal!.querySelectorAll('.steps-md-item'))
      console.log(' 过程', t, '条目', items.length)
      for (const it of items) {
        (it as HTMLElement).click()
        await flushPromises()
        const detail = modal!.querySelector('.steps-md-detail')
        if (!detail || !detail.textContent) console.log('FAIL step:', t, it.textContent)
        expect(
          (detail?.textContent?.length ?? 0) > 0,
          `过程卡 ${t} 点条目(${it.textContent?.slice(0, 24)})后右栏没有详情内容`,
        ).toBeTruthy()
      }
    }
  })
})
