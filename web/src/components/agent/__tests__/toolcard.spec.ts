// ToolCallCard / ToolGroupCard / StepsModal 的行为:详情弹窗已提升到
// AgentView 层 (ToolDetailModal,见 live-restructure.spec.ts 的缘由),卡片
// 点击只 emit;子 agent 过程是卡内触发的左右两栏弹窗(StepsModal)。
import { describe, it, expect, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import ToolCallCard from '@/components/agent/ToolCallCard.vue'
import ToolGroupCard from '@/components/agent/ToolGroupCard.vue'
import StepsModal from '@/components/agent/StepsModal.vue'

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

  it('子 agent 过程:点「过程」开左右两栏弹窗,默认选最后一条', async () => {
    const host = {
      tool: 'Task', toolUseId: 'call_host', args: '{"description":"子代理"}', state: 'ok' as const,
    }
    const step = { ...bashUse, parentToolUseId: 'call_host' }
    const w = mount(ToolCallCard, { props: { call: host, steps: [step] } })
    // 触发行:数量徽标 + 实时进展摘要
    expect(w.find('.steps-open .steps-count').text()).toBe('1')
    expect(w.find('.steps-open .steps-live').text()).toContain('rm /root/x.txt')
    await w.find('.steps-open').trigger('click')
    await flushPromises()
    // StepsModal 经 n-modal teleport 到 body:左栏列表 + 右栏详情
    const modal = document.body.querySelector('.steps-modal')
    expect(modal).toBeTruthy()
    expect(modal!.querySelectorAll('.steps-md-item').length).toBe(1)
    // 默认选中最后一条:右栏渲染该步骤的参数(命令)
    expect(modal!.querySelector('.steps-md-detail')!.textContent).toContain('rm /root/x.txt && ls -la /root')
    w.unmount()
  })

  it('触发行的实时进展:正在跑的工具步优先,否则最后一条(含文本步首行)', () => {
    const host = {
      tool: 'Task', toolUseId: 'call_host', args: '{"description":"子代理"}', state: 'running' as const,
    }
    const textStep = {
      tool: '', toolUseId: 'call_host-msg-1', parentToolUseId: 'call_host',
      text: '正在分析依赖关系,稍后开始改代码。\n\n第二段不进摘要行。',
      state: 'ok' as const,
    }
    const w = mount(ToolCallCard, { props: { call: host, steps: [textStep] } })
    // 最后一条是文本步:进展行显示它的首行
    expect(w.find('.steps-open .steps-live').text()).toContain('正在分析依赖关系')
    expect(w.find('.steps-open .steps-live').text()).not.toContain('第二段')
    w.unmount()
    // 有正在跑的工具步时优先显示工具摘要
    const w2 = mount(ToolCallCard, {
      props: { call: host, steps: [textStep, { ...bashUse, parentToolUseId: 'call_host' }] },
    })
    expect(w2.find('.steps-open .steps-live').text()).toContain('rm /root/x.txt')
    w2.unmount()
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

  // 行目标与单卡 brief 同语义(全路径,不截基名):Read 行只显基名时
  // 认不出目录、同名文件分不清 —— 用户实测反馈过。
  it('行目标显示完整值:Read 全路径、Grep 模式·目录、Glob 无目录只显模式', async () => {
    const w = mount(ToolGroupCard, { props: { calls: [
      { tool: 'Read', toolUseId: 'r1', args: '{"file_path":"/root/gittest2/long_line.txt"}', state: 'ok' as const, result: 'x' },
      { tool: 'Grep', toolUseId: 'g1', args: '{"pattern":"TODO","path":"/root/anywork/src"}', state: 'ok' as const, result: 'x' },
      { tool: 'Glob', toolUseId: 'g2', args: '{"pattern":"**/*.spec.ts"}', state: 'ok' as const, result: 'x' },
    ] } })
    await w.find('.group-head').trigger('click')
    const targets = w.findAll('.group-item .item-target')
    expect(targets[0].text()).toBe('/root/gittest2/long_line.txt')
    expect(targets[1].text()).toBe('TODO · /root/anywork/src')
    expect(targets[2].text()).toBe('**/*.spec.ts')
  })

  // 回归:折叠卡里【什么都不藏】—— 用户明确要求(自用工具,不要安全过滤)。
  // 曾经:UUID 目录 / 长文件名被「长随机串 = 密钥」的启发式吞掉,整行只剩
  // 「N 行」;命令超 72 字符被截断。现在原样显示,靠行横向滚动看全。
  it('什么都不藏:UUID 目录、命令里的长裸串都照原样显示', async () => {
    const taskPath = '/tmp/claude-0/-root-gittest2/d6a4d8cd-a9e6-4939-a802-ca5f8d5aa9fc/tasks/blldv2me6.output'
    const cmd = 'export K=$(cat /root/token_abcdefghijklmnopqrstuvwxyz0123456789)'
    const w = mount(ToolGroupCard, { props: { calls: [
      { tool: 'Read', toolUseId: 'r1', args: `{"file_path":"${taskPath}"}`, state: 'ok' as const, result: 'a\nb\nc' },
      { tool: 'Bash', toolUseId: 'b1', args: JSON.stringify({ command: cmd }), state: 'ok' as const, result: 'x' },
    ] } })
    await w.find('.group-head').trigger('click')
    const targets = w.findAll('.group-item .item-target')
    expect(targets[0]!.text()).toBe(taskPath)
    expect(w.find('.group-title').text()).toContain('blldv2me6.output')
    expect(w.findAll('.group-item .item-lines')[0]!.text()).toBe('3 行')
    expect(targets[1]!.text()).toBe(cmd)
  })

  // 参数键千奇百怪(每个工具/MCP 各不同),键表必然漏。漏了就退到第一个字符串
  // 值,而不是留一行没有目标的图标(实测 PushNotification / CronCreate 就是空的)。
  it('键表没覆盖的工具:退到第一个字符串值,不留空行', async () => {
    const w = mount(ToolGroupCard, { props: { calls: [
      { tool: 'PushNotification', toolUseId: 'p1', args: '{"message":"推送测试:收到请留意","status":"proactive"}', state: 'ok' as const, result: 'ok' },
      { tool: 'CronCreate', toolUseId: 'c1', args: '{"cron":"*/5 * * * *","prompt":"喝水提醒"}', state: 'ok' as const, result: 'ok' },
      { tool: 'ApplyPatch', toolUseId: 'a1', args: '{"changes":[{"path":"/root/a.ts","kind":{"type":"add"},"diff":"x"}]}', state: 'ok' as const, result: 'ok' },
    ] } })
    await w.find('.group-head').trigger('click')
    const targets = w.findAll('.group-item .item-target')
    expect(targets[0]!.text()).toBe('推送测试:收到请留意')
    expect(targets[1]!.text()).toBe('喝水提醒')
    expect(targets[2]!.text()).toBe('/root/a.ts')
  })

  // 参数整个丢了(事件被分页截断在工具行前面)时退到工具名:只剩一枚图标 +
  // 「N 行」的话,这行到底执行了什么就完全认不出了。
  it('参数缺失时退到工具名', async () => {
    const w = mount(ToolGroupCard, { props: { calls: [
      { tool: 'Bash', toolUseId: 'b2', args: '', state: 'ok' as const, result: 'ok' },
    ] } })
    await w.find('.group-head').trigger('click')
    expect(w.find('.group-item .item-target').exists()).toBe(false)
    expect(w.find('.group-item .item-name').text()).toBe('Bash')
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

// 子 agent 过程弹窗(hapi 同款弹窗,左右两栏:左列表右详情)。
describe('StepsModal 左右两栏', () => {
  const readStep = {
    tool: 'Read', toolUseId: 's1', args: '{"file_path":"/w/a.txt"}',
    state: 'ok' as const, result: 'AAA',
  }
  const badStep = {
    tool: 'Bash', toolUseId: 's2', args: '{"command":"make build"}',
    state: 'error' as const, result: 'boom',
  }

  it('默认选中第一个出错步骤;点条目切到对应详情', async () => {
    const w = mount(StepsModal, { props: { show: true, steps: [readStep, badStep] } })
    await flushPromises()
    const modal = document.body.querySelector('.steps-modal')!
    expect(modal.querySelectorAll('.steps-md-item').length).toBe(2)
    // 左栏:出错条目 active,摘要单行省略;右栏:它的结果
    const active = modal.querySelector('.steps-md-item.active')!
    expect(active.textContent).toContain('make build')
    expect(active.classList.contains('error')).toBe(true)
    expect(modal.querySelector('.steps-md-detail')!.textContent).toContain('boom')
    // 点第一条(Read):右栏切到它的结果
    ;(modal.querySelector('.steps-md-item') as HTMLElement).click()
    await flushPromises()
    expect(modal.querySelector('.steps-md-detail')!.textContent).toContain('AAA')
    w.unmount()
  })

  it('没有出错时默认选最后一条;关闭再开会重置选中', async () => {
    const steps = [
      { ...readStep },
      { ...badStep, state: 'ok' as const }, // make build,但这次没出错
    ]
    const w = mount(StepsModal, { props: { show: false, steps } })
    await w.setProps({ show: true })
    await flushPromises()
    let modal = document.body.querySelector('.steps-modal')!
    // 没有出错 → 默认最后一条
    expect(modal.querySelector('.steps-md-item.active')!.textContent).toContain('make build')
    // 手动切到第一条
    ;(modal.querySelector('.steps-md-item') as HTMLElement).click()
    await flushPromises()
    expect(modal.querySelector('.steps-md-item.active')!.textContent).toContain('/w/a.txt')
    // 关闭再开:重置回默认选中,不停留在手动选的那条
    await w.setProps({ show: false })
    await w.setProps({ show: true })
    await flushPromises()
    modal = document.body.querySelector('.steps-modal')!
    expect(modal.querySelector('.steps-md-item.active')!.textContent).toContain('make build')
    w.unmount()
  })

  it('文本步:气泡行只出首行预览,右栏渲染全文(markdown)', async () => {
    const textStep = {
      tool: '', toolUseId: 'call_host-msg-1', parentToolUseId: 'call_host',
      text: '我先看看**目录结构**,再决定改哪里。\n\n第二段:继续分析。',
      state: 'ok' as const,
    }
    const w = mount(StepsModal, { props: { show: true, steps: [textStep, { ...badStep }] } })
    await flushPromises()
    const modal = document.body.querySelector('.steps-modal')!
    // 左栏:消息行(.msg,无状态/分类图标),预览只有首行
    const row = modal.querySelector('.steps-md-item.msg')!
    expect(row).toBeTruthy()
    expect(row.querySelector('.steps-md-state')).toBeNull()
    expect(row.querySelector('svg')).toBeTruthy() // 气泡图标
    expect(row.textContent).toContain('目录结构')
    expect(row.textContent).not.toContain('第二段')
    // 点消息行:右栏是文本详情(全文都在),不是工具的参数/结果块
    ;(row as HTMLElement).click()
    await flushPromises()
    const text = modal.querySelector('.steps-md-text')!
    expect(text).toBeTruthy()
    expect(modal.querySelector('.steps-md-detail .tool-block')).toBeNull()
    expect(text.textContent).toContain('目录结构')
    expect(text.textContent).toContain('第二段')
    w.unmount()
  })
})
