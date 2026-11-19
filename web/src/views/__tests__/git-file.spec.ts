import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { config, flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'
import GitFileView from '@/views/GitFileView.vue'

config.global.plugins = []
const mocks = vi.hoisted(() => ({ diff: vi.fn(), show: vi.fn(), read: vi.fn() }))
vi.mock('@/api/client', () => ({ api: { gitDiff: mocks.diff, gitShow: mocks.show, fsRead: mocks.read } }))

const file = 'src/demo.ts'
const hash = 'a'.repeat(40)
const header = `diff --git a/${file} b/${file}\n--- a/${file}\n+++ b/${file}\n`
function patch(full: boolean) {
  const start = full ? 1 : 13
  const end = full ? 31 : 19
  const lines = [`@@ -${start},${end - start + 1} +${start},${end - start + 1} @@`]
  for (let n = start; n <= end; n++) {
    if (n === 16) lines.push('-  old value', '+  new value')
    else lines.push(`   unchanged line ${n}`)
  }
  return header + lines.join('\n')
}

const live: VueWrapper[] = []
async function mountFile(scope = 'commit') {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/git/file', component: GitFileView }],
  })
  await router.push({ path: '/git/file', query: { path: '/repo', root: '/repo', scope, file, ref: scope === 'commit' ? hash : '' } })
  await router.isReady()
  const w = mount(GitFileView, { attachTo: document.body, global: { plugins: [router] } })
  live.push(w)
  await flushPromises()
  return { w, router }
}

describe('单文件全文件对比', () => {
  beforeEach(() => {
    vi.resetAllMocks()
    mocks.diff.mockImplementation(async (_path, _scope, _file, _ref, full) => patch(full))
    mocks.show.mockResolvedValue('committed file content')
    mocks.read.mockResolvedValue('worktree file content')
  })
  afterEach(() => {
    live.splice(0).forEach(w => w.unmount())
    document.body.innerHTML = ''
  })

  it('默认仍为差异,全文件模式包含首尾未修改行并保留增删标记与计数', async () => {
    const { w } = await mountFile()
    expect(mocks.diff).toHaveBeenLastCalledWith('/repo', 'commit', file, hash, false)
    expect(w.findAll('.dl-body').some(row => row.text() === 'unchanged line 1')).toBe(false)
    expect(w.find('.fv-tabs [aria-label="全文件对比"]').exists()).toBe(false)
    expect(w.findAll('.fv-tabs button')).toHaveLength(3)
    expect(w.get('.diff-head [aria-label="全文件对比"]').element.tagName).toBe('BUTTON')
    expect(w.get('.dh-full svg').attributes('viewBox')).toBe('0 0 512 512')
    expect(w.get('.dh-full svg path:nth-child(2)').attributes('d')).toBe('M405.77 106.2L304 208 M208 304L111.98 400.02')
    expect(w.get('[aria-label="全文件对比"]').attributes('aria-pressed')).toBe('false')

    await w.get('[aria-label="全文件对比"]').trigger('click')
    await flushPromises()
    expect(mocks.diff).toHaveBeenLastCalledWith('/repo', 'commit', file, hash, true)
    expect(w.get('[aria-label="全文件对比"]').attributes('aria-pressed')).toBe('true')
    expect(w.findAll('.dl.ctx')).toHaveLength(30)
    expect(w.findAll('.dl.ctx .dl-body')[0].element.textContent).toBe('  unchanged line 1')
    expect(w.findAll('.dl.ctx .dl-body').at(-1)?.element.textContent).toBe('  unchanged line 31')
    expect(w.get('.dl.add .dl-body').element.textContent).toBe('  new value')
    expect(w.get('.dl.del .dl-body').element.textContent).toBe('  old value')
    expect(w.get('.dl.add .dl-sign').text()).toBe('+')
    expect(w.get('.dl.del .dl-sign').text()).toBe('-')
    expect(w.get('.dh-add').text()).toBe('+1')
    expect(w.get('.dh-del').text()).toBe('-1')
    expect(mocks.show).not.toHaveBeenCalled()
    expect(mocks.read).not.toHaveBeenCalled()

    await w.get('[aria-label="全文件对比"]').trigger('click')
    await flushPromises()
    expect(mocks.diff).toHaveBeenLastCalledWith('/repo', 'commit', file, hash, false)
    expect(w.findAll('.dl.ctx')).toHaveLength(6)
  })

  it.each(['worktree', 'staged', 'head'])('全文件模式保留 %s 对比范围,不改用当前文件内容', async scope => {
    const { w } = await mountFile(scope)
    await w.get('[aria-label="全文件对比"]').trigger('click')
    await flushPromises()
    expect(mocks.diff).toHaveBeenLastCalledWith('/repo', scope, file, undefined, true)
    expect(mocks.read).not.toHaveBeenCalled()
    expect(mocks.show).not.toHaveBeenCalled()
  })

  it('全文件对比继续支持自动换行和原有文件视图', async () => {
    const { w, router } = await mountFile()
    await w.get('[aria-label="全文件对比"]').trigger('click')
    await flushPromises()
    await w.get('[aria-label="自动换行"]').trigger('click')
    await flushPromises()
    expect(w.find('.diff-scroll.wrap').exists()).toBe(true)
    expect(router.currentRoute.value.query.wrap).toBe('1')
    await w.get('[aria-label="文件"]').trigger('click')
    await flushPromises()
    expect(mocks.show).toHaveBeenLastCalledWith('/repo', hash, file)
    expect(w.get('.file-body').text()).toBe('committed file content')
    expect(w.find('.fv-diff').exists()).toBe(false)
    expect(w.find('[aria-label="全文件对比"]').exists()).toBe(false)
    await w.get('[aria-label="差异"]').trigger('click')
    await flushPromises()
    expect(w.get('[aria-label="全文件对比"]').attributes('aria-pressed')).toBe('true')
    expect(mocks.diff).toHaveBeenLastCalledWith('/repo', 'commit', file, hash, true)
  })

  it('快速切换后,较慢的全文件响应不能覆盖差异视图', async () => {
    const { w } = await mountFile()
    let resolve!: (value: string) => void
    mocks.diff.mockImplementationOnce(() => new Promise<string>(done => { resolve = done }))
    await w.get('[aria-label="全文件对比"]').trigger('click')
    await w.get('[aria-label="全文件对比"]').trigger('click')
    await flushPromises()
    resolve(patch(true))
    await flushPromises()
    expect(w.findAll('.dl.ctx')).toHaveLength(6)
    expect(w.get('[aria-label="全文件对比"]').attributes('aria-pressed')).toBe('false')
  })

  it('全文件读取失败时提示错误,仍能返回原差异模式', async () => {
    const { w } = await mountFile()
    mocks.diff.mockRejectedValueOnce(new Error('读取全文件差异失败'))
    await w.get('[aria-label="全文件对比"]').trigger('click')
    await flushPromises()
    expect(w.find('.n-alert').text()).toContain('读取全文件差异失败')
    expect(w.find('.diff-head [aria-label="全文件对比"]').exists()).toBe(true)
    await w.get('[aria-label="全文件对比"]').trigger('click')
    await flushPromises()
    expect(w.find('.n-alert').exists()).toBe(false)
    expect(w.findAll('.dl.ctx')).toHaveLength(6)
  })

  it('二进制文件保留 Git 的说明,不展示乱码', async () => {
    mocks.diff.mockResolvedValue(`diff --git a/${file} b/${file}\nBinary files a/${file} and b/${file} differ`)
    const { w } = await mountFile()
    await w.get('[aria-label="全文件对比"]').trigger('click')
    await flushPromises()
    expect(w.get('.dl.meta').text()).toContain('Binary files')
    expect(w.findAll('.dl.add')).toHaveLength(0)
    expect(w.findAll('.dl.del')).toHaveLength(0)
  })

  it('卡头只显示箭头图标,每次点击只触发一次切换', async () => {
    const { w } = await mountFile()
    expect(w.get('.dh-full').text()).toBe('')
    expect(w.find('.diff-head .n-switch').exists()).toBe(false)
    await w.get('.dh-full').trigger('click')
    await flushPromises()
    expect(mocks.diff).toHaveBeenCalledTimes(2)
    expect(w.get('[aria-label="全文件对比"]').attributes('aria-pressed')).toBe('true')
    await w.get('[aria-label="全文件对比"]').trigger('click')
    await flushPromises()
    expect(mocks.diff).toHaveBeenCalledTimes(3)
    expect(w.get('[aria-label="全文件对比"]').attributes('aria-pressed')).toBe('false')
  })

  it('没有差异时仍保留开关,不会因空内容而丢失入口', async () => {
    mocks.diff.mockResolvedValueOnce('')
    const { w } = await mountFile()
    expect(w.find('.n-empty').text()).toContain('该文件没有内容差异')
    await w.get('[aria-label="全文件对比"]').trigger('click')
    await flushPromises()
    expect(w.findAll('.dl.ctx')).toHaveLength(30)
  })

  it('未跟踪文件仍只看文件,不显示全文件对比开关', async () => {
    const { w } = await mountFile('untracked')
    expect(w.get('[aria-label="差异"]').attributes('disabled')).toBeDefined()
    expect(w.find('[aria-label="全文件对比"]').exists()).toBe(false)
    expect(mocks.diff).not.toHaveBeenCalled()
    expect(mocks.read).toHaveBeenLastCalledWith('/repo/' + file)
    expect(w.get('.file-body').text()).toBe('worktree file content')
  })
})
