// ToolCallCard / ToolGroupCard 的点击行为:详情弹窗已提升到 AgentView 层
// (ToolDetailModal,见 live-restructure.spec.ts 的缘由),卡片点击只 emit。
import { describe, it, expect, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import ToolCallCard from '@/components/agent/ToolCallCard.vue'
import ToolGroupCard from '@/components/agent/ToolGroupCard.vue'

vi.mock('@/api/client', () => ({
  api: { agentFileUrl: (id: string, n: string) => `/api/agent/sessions/${id}/files/${n}` },
}))

// DB 实测形态:tool_use 行带 args+state=running;结果行 args="" 且【无 state 字段】。
const bashUse = {
  tool: 'Bash',
  toolUseId: 'call_1d671e180d444aba89b94fc3',
  args: '{"command":"rm /root/x.txt && ls -la /root","description":"Remove generated files"}',
  state: 'running' as const,
}
// AgentView 的合并只回填 result/state/images,不 spread 结果行的空 args。
const bashMerged = { ...bashUse, result: 'total 44\ndrwxr-xr-x 4 root root 4096 .\n-rw-r--r-- 1 root root 226 index.html', state: 'ok' as const }

describe('ToolCallCard 点击', () => {
  it('点头部 emit detail(合并后的卡:state 被 result 事件抹成 ok)', async () => {
    const w = mount(ToolCallCard, { props: { call: bashMerged } })
    await w.find('.tool-head').trigger('click')
    expect(w.emitted('detail')?.[0]?.[0]).toStrictEqual({ ...bashMerged })
  })

  it('点头部 emit detail(纯 tool_use 行,运行中)', async () => {
    const w = mount(ToolCallCard, { props: { call: { ...bashUse } } })
    await w.find('.tool-head').trigger('click')
    expect(w.emitted('detail')?.[0]?.[0]).toStrictEqual({ ...bashUse })
  })

  it('子 agent 过程条目点击 emit detail', async () => {
    const host = {
      tool: 'Task', toolUseId: 'call_host', args: '{"description":"子代理"}', state: 'ok' as const,
    }
    const step = { ...bashUse, parentToolUseId: 'call_host' }
    const w = mount(ToolCallCard, { props: { call: host, steps: [step] } })
    await w.find('.steps-toggle').trigger('click')
    await w.find('.steps-item').trigger('click')
    expect(w.emitted('detail')?.[0]?.[0]).toStrictEqual({ ...step })
  })
})

describe('ToolGroupCard 行点击', () => {
  it('展开组 → 点行 → emit detail(该行的调用对象)', async () => {
    const readCall = {
      tool: 'Read',
      toolUseId: 'call_e2c8598b0ce347578d6eebbe',
      args: '{"file_path":"/root/gittest2/long_line.txt","limit":4}',
      state: 'ok' as const,
      result: 'AAAA(超长行)',
    }
    const w = mount(ToolGroupCard, { props: { calls: [readCall, bashMerged] } })
    await w.find('.group-head').trigger('click')
    const items = w.findAll('.group-item')
    expect(items.length).toBe(2)
    await items[1].trigger('click')
    expect(w.emitted('detail')?.[0]?.[0]).toStrictEqual({ ...bashMerged })
  })
})

// hapi 同款图标样式:分类图标替代文字工具名(读/写/命令有专属图标,
// 兜底类保留名字),状态用 勾/叉/锁/spinner 替代文字。
describe('工具卡图标(hapi 同款)', () => {
  it('Bash 卡:终端图标 + 状态图标,不再有文字工具名', () => {
    const w = mount(ToolCallCard, { props: { call: { ...bashMerged } } })
    expect(w.find('.tool-icon svg').exists()).toBe(true)
    expect(w.find('.tool-name').exists()).toBe(false)
    // 完成 = 圈勾(ok 态的 path 在同一 svg 里)
    const stateSvg = w.find('.tool-state svg')
    expect(stateSvg.exists()).toBe(true)
    expect(stateSvg.attributes('viewBox')).toBe('0 0 16 16')
    expect(w.find('.tool-state').classes()).toContain('ok')
  })

  it('兜底类(Task):扳手图标旁保留工具名', () => {
    const w = mount(ToolCallCard, {
      props: { call: { tool: 'Task', toolUseId: 't1', args: '{"description":"d"}', state: 'ok' as const } },
    })
    expect(w.find('.tool-icon svg').exists()).toBe(true)
    expect(w.find('.tool-name').text()).toBe('Task')
  })

  it('出错卡:红圈叉(圆圈 + 交叉斜线)', () => {
    const w = mount(ToolCallCard, {
      props: { call: { ...bashUse, state: 'error' as const } },
    })
    expect(w.find('.tool-state').classes()).toContain('error')
    const d = w.findAll('.tool-state svg path')
    expect(d.some((x) => (x.attributes('d') || '').includes('M5.6 5.6l4.8 4.8'))).toBe(true)
  })

  it('等审批:挂锁;被拒:归并红圈叉', () => {
    const w = mount(ToolCallCard, {
      props: {
        call: { ...bashUse },
        pendingReq: { reqId: 'r1', tool: 'Bash', args: 'ls' },
      },
    })
    expect(w.find('.tool-state').classes()).toContain('pending')
    const w2 = mount(ToolCallCard, {
      props: {
        call: { ...bashUse },
        pendingReq: { reqId: 'r1', tool: 'Bash', args: 'ls' },
        pendingResolved: { allow: false },
      },
    })
    expect(w2.find('.tool-state').classes()).toContain('error')
  })

  it('组卡:头部状态图标,行内 状态图标+分类图标,无文字状态', async () => {
    const w = mount(ToolGroupCard, { props: { calls: [
      { tool: 'Read', toolUseId: 'r', args: '{"file_path":"/w/a"}', state: 'ok' as const, result: 'x' },
      { ...bashMerged },
    ] } })
    await w.find('.group-head').trigger('click')
    expect(w.find('.group-head .group-state svg').exists()).toBe(true)
    const items = w.findAll('.group-item')
    expect(items[0].find('.item-state svg').exists()).toBe(true)
    expect(items[0].find('.item-icon svg').exists()).toBe(true)
    expect(w.find('.item-dot').exists()).toBe(false)
  })
})
