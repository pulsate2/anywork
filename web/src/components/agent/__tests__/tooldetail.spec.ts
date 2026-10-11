// 详情弹窗(Edit/Read)里的文件名:必须完整可见、可换行。
// Edit 的 diff 头(.diff-path)与 Read 的路径行(.tool-path)都曾用单行省略,
// 长目录一上来文件名就被省略号吃掉,弹窗里认不出看的是哪个文件(用户实测)。
// 这里只断言 DOM:文件名原样渲染、不重复;换行/省略是样式,靠 CSS 评审。
import { describe, it, expect, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import ToolDetailBody from '@/components/agent/ToolDetailBody.vue'

vi.mock('@/api/client', () => ({
  api: { agentFileUrl: (id: string, n: string) => `/api/agent/sessions/${id}/files/${n}` },
}))

// 实测形态:claude 的任务输出文件,UUID 目录 + 长文件名。
const longPath = '/tmp/claude-0/-root-anywork/d6a4d8cd-a9e6-4939-a802-ca5f8d5aa9fc/tasks/blldv2me6.output'

describe('详情弹窗的文件名', () => {
  it('Read:路径单独成行,完整显示(不截断、不塞进参数块重复一遍)', () => {
    const w = mount(ToolDetailBody, { props: { call: {
      tool: 'Read', toolUseId: 'r1', state: 'ok' as const,
      args: JSON.stringify({ file_path: longPath, limit: 4 }),
      result: 'a\nb\nc',
    } } })
    const path = w.find('.tool-path')
    expect(path.exists()).toBe(true)
    expect(path.text()).toBe(longPath)
    // 路径不再作为参数块出现(否则弹窗里同样的路径显示两遍)
    expect(w.findAll('.tool-block').length).toBe(1)
    expect(w.find('.tool-block').text()).toBe('a\nb\nc')
  })

  it('NotebookRead:notebook_path 同样认路径', () => {
    const w = mount(ToolDetailBody, { props: { call: {
      tool: 'NotebookRead', toolUseId: 'n1', state: 'ok' as const,
      args: JSON.stringify({ notebook_path: '/root/x.ipynb' }),
      result: 'ok',
    } } })
    expect(w.find('.tool-path').text()).toBe('/root/x.ipynb')
  })

  it('Edit:diff 头是完整路径,后接 +/- 统计', () => {
    const w = mount(ToolDetailBody, { props: { call: {
      tool: 'Edit', toolUseId: 'e1', state: 'ok' as const,
      args: JSON.stringify({ file_path: longPath, old_string: 'a', new_string: 'b' }),
      result: 'ok',
    } } })
    expect(w.find('.diff-path').text()).toBe(longPath)
    expect(w.find('.diff-badge.add').text()).toBe('+1')
  })

  it('Bash:没有路径行,命令照旧进参数块', () => {
    const w = mount(ToolDetailBody, { props: { call: {
      tool: 'Bash', toolUseId: 'b1', state: 'ok' as const,
      args: JSON.stringify({ command: 'ls -la /root' }),
      result: 'x',
    } } })
    expect(w.find('.tool-path').exists()).toBe(false)
    expect(w.find('.tool-block').text()).toBe('ls -la /root')
  })

  // 参数被后端截断成信封时解析不出 file_path:退回参数原文块,不显示半截路径。
  it('Read 参数截断(信封形态):不出路径行,退回参数原文', () => {
    const w = mount(ToolDetailBody, { props: { call: {
      tool: 'Read', toolUseId: 'r2', state: 'ok' as const,
      args: JSON.stringify({ truncated: true, raw: '{"file_path":"/tmp/aaa…(已截断)' }),
      result: 'x',
    } } })
    expect(w.find('.tool-path').exists()).toBe(false)
    expect(w.find('.tool-block').text()).toContain('truncated')
  })

  // pi 编辑类工具:后端把结果 details.patch 挂到 call.diff,详情弹窗据此渲染 diff。
  it('pi edit:call.diff(标准 unified patch)渲染成 DiffView', () => {
    const w = mount(ToolDetailBody, { props: { call: {
      tool: 'edit', toolUseId: 'pe1', state: 'ok' as const,
      args: JSON.stringify({ path: '/root/a.md' }),
      result: 'Successfully replaced 1 block(s).',
      diff: '--- /root/a.md\n+++ /root/a.md\n@@ -1 +1 @@\n-old\n+new\n',
    } } })
    expect(w.find('.diff-path').text()).toBe('/root/a.md')
    expect(w.find('.diff-badge.add').text()).toBe('+1')
  })

  // pi 的 write:无 patch,按 start 入参的 content 当新建内容呈现。
  it('pi write:无 diff 字段时按 content 入参呈现新建内容', () => {
    const w = mount(ToolDetailBody, { props: { call: {
      tool: 'write', toolUseId: 'pw1', state: 'ok' as const,
      args: JSON.stringify({ path: '/root/new.md', content: 'hello\nworld' }),
      result: 'Successfully wrote to /root/new.md',
    } } })
    expect(w.find('.diff-path').text()).toBe('/root/new.md')
    expect(w.find('.diff-badge.add').text()).toBe('+2')
  })

  // pi 的 move/批次合并 patch 是裸多文件(无 diff --git,靠 --- / +++ 头切文件):
  // 必须拆成每文件一张 DiffView,不能糊成一块、路径还取后一个。
  it('pi 裸多文件 patch:按 --- / +++ 头拆成多张 DiffView', () => {
    const diff = [
      '--- edit_src.txt', '+++ edit_src.txt', '@@ -1,5 +1,4 @@',
      ' line4: delta', '-line5: epsilon',
      '--- edit_dst.txt', '+++ edit_dst.txt', '@@ -3,4 +3,5 @@',
      ' DEST-C', '+line5: epsilon', '',
    ].join('\n')
    const w = mount(ToolDetailBody, { props: { call: {
      tool: 'move', toolUseId: 'mv1', state: 'ok' as const,
      args: JSON.stringify({ path: 'edit_src.txt' }),
      result: 'ok', diff,
    } } })
    // 修复前:无 diff --git 分隔,两文件糊成一张卡、路径取后一个。
    const views = w.findAll('.diff-view')
    expect(views.length).toBe(2)
    expect(views[0].find('.diff-path').text()).toBe('edit_src.txt')
    expect(views[1].find('.diff-path').text()).toBe('edit_dst.txt')
    // 各卡徽标互不串:第一文件有删除、第二文件有新增。
    expect(views[0].find('.diff-badge.del').exists()).toBe(true)
    expect(views[1].find('.diff-badge.add').exists()).toBe(true)
  })
})
