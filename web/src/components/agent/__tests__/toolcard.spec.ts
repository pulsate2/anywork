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
