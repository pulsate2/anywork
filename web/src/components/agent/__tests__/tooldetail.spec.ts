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
})
