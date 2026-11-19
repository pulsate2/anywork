import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { config, flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import { NTabs } from 'naive-ui'
import App from '@/App.vue'
import GitView from '@/views/GitView.vue'
import GitFileView from '@/views/GitFileView.vue'
import type { GitCommit, GitCommitDetail } from '@/api/client'

config.global.plugins = []
const mocks = vi.hoisted(() => ({
  log: vi.fn(), detail: vi.fn(), diff: vi.fn(), revert: vi.fn(), show: vi.fn(),
}))
vi.mock('@/api/gitAuth', () => ({ GitAuthClient: class { connect() {} close() {} } }))
vi.mock('@/api/client', async (importOriginal) => {
  const original = await importOriginal<typeof import('@/api/client')>()
  return { ...original, api: {
    ...original.api,
    me: async () => ({ root: '/repo' }),
    workspaces: async () => [{ id: 'project', name: 'Project', path: '/repo', favorite: false, sortOrder: 0, createdAt: '' }],
    gitRepo: async () => ({ dir: '/repo', root: '/repo', branch: 'main', repo: true, short: 'repo' }),
    gitStatus: async () => ({
      branch: 'main', ahead: 0, behind: 0, staged: [], unstaged: [], untracked: [], conflicted: [],
      clean: true, initial: false, detached: false, reverting: false,
    }),
    gitLog: mocks.log, gitCommitDetail: mocks.detail, gitDiff: mocks.diff, gitRevert: mocks.revert, gitShow: mocks.show,
    gitBranches: async () => ({ current: 'main', local: [{ name: 'main', current: true }, { name: 'feature', current: false }], remote: [] }),
    gitAuthors: async () => [{ name: 'Alice', email: 'alice@example.com' }],
  } }
})

function hash(n: number) { return n.toString(16).padStart(7, '0') + 'a'.repeat(33) }
function commit(n: number): GitCommit {
  return {
    hash: hash(n), short: hash(n).slice(0, 7), subject: 'feat: alpha ' + n, author: 'Alice', email: 'alice@example.com',
    date: '2026-11-08T19:55:00+08:00', parents: [hash(n + 1)], refs: [],
  }
}
const rows = Array.from({ length: 60 }, (_, i) => commit(i + 1))
const live: VueWrapper[] = []
async function mountGit(path = '/git') {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', name: 'home', component: { template: '<div />' } },
      { path: '/git', name: 'git', component: GitView },
      { path: '/git/file', name: 'git-file', component: GitFileView },
    ],
  })
  await router.push(path)
  await router.isReady()
  const w = mount(App, { attachTo: document.body, global: { plugins: [createPinia(), router], stubs: { BottomNav: true } } })
  live.push(w)
  await flushPromises()
  return { w, router }
}

describe('图形 Git 历史与文件页集成', () => {
  beforeEach(() => {
    vi.resetAllMocks()
    localStorage.clear()
    sessionStorage.clear()
    localStorage.setItem('lr.currentWorkspace', 'project')
    mocks.log.mockImplementation(async (_path: string, n: number, skip: number) => rows.slice(skip, skip + n))
    mocks.detail.mockImplementation(async (_path: string, hash: string): Promise<GitCommitDetail> => ({
      ...rows.find(c => c.hash === hash)!, body: '', files: [{ path: 'src/main.ts', add: 1, del: 0, binary: false }],
    }))
    mocks.diff.mockResolvedValue('diff --git a/src/main.ts b/src/main.ts\n--- a/src/main.ts\n+++ b/src/main.ts\n@@ -0,0 +1 @@\n+hello\n')
    mocks.show.mockResolvedValue('hello')
    mocks.revert.mockResolvedValue({ out: '' })
  })
  afterEach(() => {
    live.splice(0).forEach(w => w.unmount())
    document.body.innerHTML = ''
    localStorage.clear()
    sessionStorage.clear()
  })

  it('查看历史文件后返回保留后续页选中项、筛选与滚动,不再弹旧列表', async () => {
    const query = new URLSearchParams({
      history: hash(45), n: '60', search: 'alpha', branch: 'feature', author: 'alice@example.com', scroll: '242',
    })
    const { w, router } = await mountGit('/git?' + query.toString())
    expect(w.findComponent(NTabs).props('value')).toBe('log')
    expect(w.findAll('.history-row')).toHaveLength(60)
    expect(w.get('.history-row.selected').attributes('data-hash')).toBe(hash(45))
    expect((w.get('.history-scroll').element as HTMLElement).scrollTop).toBe(242)
    expect(router.currentRoute.value.query).toEqual({})
    await w.get('.history-file').trigger('click')
    await flushPromises()
    expect(router.currentRoute.value.path).toBe('/git/file')
    expect(router.currentRoute.value.query.ref).toBe(hash(45))
    expect(mocks.diff).toHaveBeenLastCalledWith('/repo', 'commit', 'src/main.ts', hash(45), false)
    await w.get('[aria-label="文件"]').trigger('click')
    await flushPromises()
    expect(mocks.show).toHaveBeenLastCalledWith('/repo', hash(45), 'src/main.ts')
    await w.get('[aria-label="返回列表"]').trigger('click')
    await flushPromises()
    expect(router.currentRoute.value.path).toBe('/git')
    expect(w.get('.history-row.selected').attributes('data-hash')).toBe(hash(45))
    expect((w.get('.history-scroll').element as HTMLElement).scrollTop).toBe(242)
    expect(mocks.log).toHaveBeenLastCalledWith('/repo', 60, 0, {
      search: 'alpha', ref: 'feature', author: 'alice@example.com',
    })
    expect(document.body.querySelector('.n-modal')).toBeNull()
  })

  it('兼容旧版 diff=commit 的返回记录', async () => {
    const { w } = await mountGit('/git?diff=commit&ref=' + hash(45) + '&n=60')
    expect(w.findComponent(NTabs).props('value')).toBe('log')
    expect(w.get('.history-row.selected').attributes('data-hash')).toBe(hash(45))
    expect(w.findAll('.history-file')).toHaveLength(1)
  })

  it('切换改动/提交历史标签不丢失已加载页和选中项', async () => {
    const { w } = await mountGit('/git?history=' + hash(45) + '&n=60&search=alpha')
    const calls = mocks.log.mock.calls.length
    w.findComponent(NTabs).vm.$emit('update:value', 'changes')
    await flushPromises()
    w.findComponent(NTabs).vm.$emit('update:value', 'log')
    await flushPromises()
    expect(mocks.log).toHaveBeenCalledTimes(calls)
    expect(w.get('.history-row.selected').attributes('data-hash')).toBe(hash(45))
    expect(w.findAll('.history-row')).toHaveLength(60)
    expect((w.get('.history-search input').element as HTMLInputElement).value).toBe('alpha')
  })

  it('回滚仍需确认,确认前不会请求 Git 写操作', async () => {
    const { w } = await mountGit('/git?history=' + hash(2))
    await w.get('.history-detail-actions button').trigger('click')
    await flushPromises()
    expect(mocks.revert).not.toHaveBeenCalled()
    const dialog = document.body.querySelector('.n-dialog')!
    expect(dialog.textContent).toContain('原提交仍留在历史里')
    const positive = [...dialog.querySelectorAll('button')].find(button => button.textContent?.trim() === '回滚')!
    positive.click()
    await flushPromises()
    expect(mocks.revert).toHaveBeenCalledWith('/repo', 'revert', hash(2))
  })
})
