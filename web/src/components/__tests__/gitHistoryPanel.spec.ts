import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { NSelect } from 'naive-ui'
import GitHistoryPanel from '@/components/GitHistoryPanel.vue'
import type { GitCommit, GitCommitDetail } from '@/api/client'

const mocks = vi.hoisted(() => ({ log: vi.fn(), detail: vi.fn(), branches: vi.fn(), authors: vi.fn(), copy: vi.fn() }))
vi.mock('@/api/client', async (importOriginal) => {
  const original = await importOriginal<typeof import('@/api/client')>()
  return { ...original, api: {
    ...original.api, gitLog: mocks.log, gitCommitDetail: mocks.detail, gitBranches: mocks.branches, gitAuthors: mocks.authors,
  } }
})

function hash(n: number) { return n.toString(16).padStart(7, '0') + 'a'.repeat(33) }
function commit(n: number): GitCommit {
  return {
    hash: hash(n), short: hash(n).slice(0, 7), parents: n < 90 ? [hash(n + 1)] : [], refs: n === 1 ? ['main'] : [],
    subject: 'feat: commit ' + n, author: 'Chenyme', email: 'dev@example.com', date: '2026-11-08T19:55:00+08:00',
  }
}
function detail(n: number): GitCommitDetail {
  return {
    ...commit(n), body: '提交说明 ' + n, files: [
      { path: 'src/main.ts', add: 6, del: 2, binary: false },
      { path: 'assets/logo.png', add: 0, del: 0, binary: true },
    ],
  }
}
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(done => { resolve = done })
  return { promise, resolve }
}
const live: VueWrapper[] = []
async function panel(props: Record<string, unknown> = {}) {
  const w = mount(GitHistoryPanel, { props: { repoPath: '/repo', currentBranch: 'main', revision: 0, ...props } })
  live.push(w)
  await flushPromises()
  return w
}

describe('VS Code 风格 Git 历史面板', () => {
  beforeEach(() => {
    vi.resetAllMocks()
    mocks.log.mockResolvedValue([commit(1), commit(2), commit(3)])
    mocks.detail.mockImplementation(async (_path: string, value: string) => detail(parseInt(value.slice(0, 7), 16)))
    mocks.branches.mockResolvedValue({
      current: 'main', local: [{ name: 'main', current: true }, { name: 'feature', current: false }],
      remote: [{ name: 'origin/main', remote: true }],
    })
    mocks.authors.mockResolvedValue([
      { name: 'Chenyme', email: 'dev@example.com' }, { name: 'Other author', email: 'other@example.com' },
    ])
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: mocks.copy.mockResolvedValue(undefined) } })
    Object.defineProperty(HTMLElement.prototype, 'scrollIntoView', { configurable: true, value: vi.fn() })
  })
  afterEach(() => {
    live.splice(0).forEach(w => w.unmount())
    vi.useRealTimers()
  })

  it('显示分支图、作者、时间、选中详情和文件统计', async () => {
    const w = await panel()
    expect(mocks.log).toHaveBeenLastCalledWith('/repo', 30, 0, { ref: '', search: '', author: '' })
    expect(w.findAll('.history-row')).toHaveLength(3)
    expect(w.findAll('.history-graph')).toHaveLength(3)
    expect(w.get('.history-row.selected').attributes('data-hash')).toBe(hash(1))
    expect(w.get('.history-detail-subject').text()).toBe(commit(1).subject)
    expect(w.get('.history-files-header').text()).toContain('2 个文件')
    expect(w.get('.history-files-header').text()).toContain('+6')
    expect(w.get('.history-files-header').text()).toContain('−2')
    expect(w.get('.history-binary').text()).toBe('二进制')
    expect(w.get('.history-detail-date').text()).toContain('19:55')
  })

  it('点击行更新详情,方向键和 Home/End 可导航', async () => {
    const w = await panel()
    await w.get('[data-hash="' + hash(2) + '"]').trigger('click')
    await flushPromises()
    expect(w.get('.history-detail-subject').text()).toBe(commit(2).subject)
    await w.get('[data-hash="' + hash(2) + '"]').trigger('keydown', { key: 'ArrowDown' })
    await flushPromises()
    expect(w.get('.history-row.selected').attributes('data-hash')).toBe(hash(3))
    await w.get('[data-hash="' + hash(3) + '"]').trigger('keydown', { key: 'Home' })
    await flushPromises()
    expect(w.get('.history-row.selected').attributes('data-hash')).toBe(hash(1))
    await w.get('[data-hash="' + hash(1) + '"]').trigger('keydown', { key: 'End' })
    await flushPromises()
    expect(w.get('.history-row.selected').attributes('data-hash')).toBe(hash(3))
  })

  it('打开文件传递确切提交和返回状态,回滚只发出意图,复制使用完整提交号', async () => {
    const w = await panel()
    const scroll = w.get('.history-scroll').element as HTMLElement
    scroll.scrollTop = 85
    await w.get('.history-file').trigger('click')
    const [file, selected, state] = w.emitted('open-file')![0] as [string, GitCommit, Record<string, unknown>]
    expect(file).toBe('src/main.ts')
    expect(selected.hash).toBe(hash(1))
    expect(state).toMatchObject({ selectedHash: hash(1), limit: 30, scrollTop: 85 })
    await w.get('.history-detail-actions button').trigger('click')
    expect((w.emitted('revert')![0][0] as GitCommit).hash).toBe(hash(1))
    await w.get('[aria-label="复制完整提交号"]').trigger('click')
    await flushPromises()
    expect(mocks.copy).toHaveBeenCalledWith(hash(1))
    expect(w.find('[aria-label="已复制"]').exists()).toBe(true)
  })

  it('搜索/分支/作者在服务端组合筛选,作者选项来自整个仓库', async () => {
    const w = await panel()
    const selects = w.findAllComponents(NSelect)
    expect(selects[1].props('options')).toContainEqual({ label: 'Other author', value: 'other@example.com' })
    await w.get('.history-search input').setValue('feature')
    await w.get('.history-search input').trigger('keydown', { key: 'Enter' })
    await flushPromises()
    expect(mocks.log).toHaveBeenLastCalledWith('/repo', 30, 0, { ref: '', search: 'feature', author: '' })
    selects[0].vm.$emit('update:value', 'feature')
    await flushPromises()
    selects[1].vm.$emit('update:value', 'other@example.com')
    await flushPromises()
    expect(mocks.log).toHaveBeenLastCalledWith('/repo', 30, 0, { ref: 'feature', search: 'feature', author: 'other@example.com' })
    expect(w.get('.history-footer').text()).toContain('仅连接已显示')
    await w.get('.history-clear').trigger('click')
    await flushPromises()
    expect(mocks.log).toHaveBeenLastCalledWith('/repo', 30, 0, { ref: '', search: '', author: '' })
  })

  it('输入搜索会防抖,不会每个字符都请求', async () => {
    const w = await panel()
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    const initialCalls = mocks.log.mock.calls.length
    await w.get('.history-search input').setValue('f')
    await w.get('.history-search input').setValue('feat')
    expect(mocks.log).toHaveBeenCalledTimes(initialCalls)
    await vi.advanceTimersByTimeAsync(281)
    await flushPromises()
    expect(mocks.log).toHaveBeenCalledTimes(initialCalls + 1)
    expect(mocks.log.mock.calls[mocks.log.mock.calls.length - 1]?.[3].search).toBe('feat')
  })

  it('尚未应用的搜索输入不会污染当前文件的返回状态', async () => {
    const w = await panel()
    await w.get('.history-search input').setValue('pending search')
    await w.get('.history-file').trigger('click')
    const state = w.emitted('open-file')![0][2] as Record<string, unknown>
    expect(state.search).toBe('')
    expect(state.selectedHash).toBe(hash(1))
  })

  it('分页沿用当前筛选,刷新保留已加载页、选中项和滚动', async () => {
    const rows = Array.from({ length: 33 }, (_, i) => commit(i + 1))
    mocks.log.mockImplementation(async (_path: string, n: number, skip: number) => rows.slice(skip, skip + n))
    const w = await panel()
    await w.get('.history-footer button').trigger('click')
    await flushPromises()
    expect(mocks.log).toHaveBeenLastCalledWith('/repo', 30, 30, { ref: '', search: '', author: '' })
    expect(w.findAll('.history-row')).toHaveLength(33)
    await w.get('[data-hash="' + hash(32) + '"]').trigger('click')
    await flushPromises()
    ;(w.get('.history-scroll').element as HTMLElement).scrollTop = 320
    await w.get('.history-refresh').trigger('click')
    await flushPromises()
    expect(mocks.log).toHaveBeenLastCalledWith('/repo', 33, 0, { ref: '', search: '', author: '' })
    expect(w.get('.history-row.selected').attributes('data-hash')).toBe(hash(32))
    expect((w.get('.history-scroll').element as HTMLElement).scrollTop).toBe(320)
  })

  it('文件页返回时恢复筛选、后续页提交和滚动', async () => {
    const rows = Array.from({ length: 60 }, (_, i) => commit(i + 1))
    mocks.log.mockResolvedValue(rows)
    const w = await panel({ initialState: {
      search: 'feat', branch: 'feature', author: 'dev@example.com', selectedHash: hash(45), limit: 60, scrollTop: 240,
    } })
    expect(mocks.log).toHaveBeenLastCalledWith('/repo', 60, 0, { ref: 'feature', search: 'feat', author: 'dev@example.com' })
    expect(w.get('.history-row.selected').attributes('data-hash')).toBe(hash(45))
    expect((w.get('.history-scroll').element as HTMLElement).scrollTop).toBe(240)
  })

  it('慢的旧筛选结果不能覆盖新的结果', async () => {
    const first = deferred<GitCommit[]>()
    const second = deferred<GitCommit[]>()
    mocks.log.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise)
    const w = await panel()
    w.findAllComponents(NSelect)[0].vm.$emit('update:value', 'feature')
    second.resolve([commit(2)])
    await flushPromises()
    first.resolve([commit(1)])
    await flushPromises()
    expect(w.get('.history-row.selected').attributes('data-hash')).toBe(hash(2))
  })

  it('慢的旧详情不能覆盖刚选中的提交', async () => {
    const first = deferred<GitCommitDetail>()
    mocks.detail.mockReturnValueOnce(first.promise).mockResolvedValueOnce(detail(2))
    const w = await panel()
    await w.get('[data-hash="' + hash(2) + '"]').trigger('click')
    await flushPromises()
    first.resolve(detail(1))
    await flushPromises()
    expect(w.get('.history-detail-subject').text()).toBe(commit(2).subject)
    expect(w.get('.history-commit-body').text()).toBe('提交说明 2')
  })

  it('父提交在当前页时直接选中,不在当前页时按提交号定位', async () => {
    const w = await panel()
    await w.get('.history-parent').trigger('click')
    await flushPromises()
    expect(w.get('.history-row.selected').attributes('data-hash')).toBe(hash(2))
    await w.get('[data-hash="' + hash(3) + '"]').trigger('click')
    await flushPromises()
    mocks.log.mockResolvedValueOnce([commit(4)])
    await w.get('.history-parent').trigger('click')
    await flushPromises()
    expect(mocks.log).toHaveBeenLastCalledWith('/repo', 30, 0, { ref: '', search: hash(4), author: '' })
    expect(w.get('.history-row.selected').attributes('data-hash')).toBe(hash(4))
  })

  it('空结果、读取失败和重试有明确反馈', async () => {
    mocks.log.mockRejectedValueOnce(new Error('network unavailable'))
    const w = await panel()
    expect(w.get('.history-error').text()).toContain('network unavailable')
    mocks.log.mockResolvedValueOnce([])
    await w.get('.history-error button').trigger('click')
    await flushPromises()
    expect(w.find('.history-error').exists()).toBe(false)
    expect(w.findAll('.history-row')).toHaveLength(0)
    expect(w.get('.history-detail-empty').text()).toContain('选择提交')
  })

  it('切换仓库清掉上一仓库的筛选和详情', async () => {
    const w = await panel()
    w.findAllComponents(NSelect)[0].vm.$emit('update:value', 'feature')
    await flushPromises()
    mocks.log.mockResolvedValueOnce([commit(9)])
    await w.setProps({ repoPath: '/other' })
    await flushPromises()
    expect(mocks.log).toHaveBeenLastCalledWith('/other', 30, 0, { ref: '', search: '', author: '' })
    expect(w.get('.history-row.selected').attributes('data-hash')).toBe(hash(9))
    expect(mocks.detail).toHaveBeenLastCalledWith('/other', hash(9))
  })
})
