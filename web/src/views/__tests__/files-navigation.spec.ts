import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { config, flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import { NButton, NRadioGroup, NSelect } from 'naive-ui'
import App from '@/App.vue'
import FilesView from '@/views/FilesView.vue'
import FilesFileView from '@/views/FilesFileView.vue'
import { useWorkspaceStore } from '@/stores/workspace'
import type { FsEntry, Workspace } from '@/api/client'

// 使用应用的真实路由出口,覆盖列表和预览页之间的状态恢复。
config.global.plugins = []

const mocks = vi.hoisted(() => ({ list: vi.fn(), search: vi.fn(), workspaces: vi.fn(), me: vi.fn() }))
vi.mock('@/api/client', () => ({ api: {
  fsList: mocks.list,
  fsSearch: mocks.search,
  workspaces: mocks.workspaces,
  me: mocks.me,
  fsRead: async () => 'file content',
  fsDownloadUrl: (path: string) => `/api/fs/download?path=${encodeURIComponent(path)}`,
  fsInlineUrl: (path: string) => `/api/fs/download?path=${encodeURIComponent(path)}&inline=1`,
} }))

function entry(path: string, dir = false): FsEntry {
  return { path, name: path.split('/').pop()!, dir, size: 12, mtime: '', mode: '', symlink: false }
}

function workspace(id: string, path: string): Workspace {
  return { id, path, name: id, favorite: false, sortOrder: 0, createdAt: '' }
}

function files(dir: string) {
  return [{ ...entry(`${dir}/picture.png`), size: 100 }, entry(`${dir}/note.txt`)]
}

const dirs = new Map<string, FsEntry[]>()
const live: VueWrapper[] = []

async function mountFiles(preload = true) {
  const pinia = createPinia()
  const store = useWorkspaceStore(pinia)
  if (preload) await store.ensure()
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', name: 'home', component: { template: '<div />' } },
      { path: '/files', name: 'files', component: FilesView },
      { path: '/files/file', name: 'files-file', component: FilesFileView },
    ],
  })
  await router.push('/files')
  await router.isReady()
  const w = mount(App, { global: { plugins: [pinia, router], stubs: { BottomNav: true } } })
  live.push(w)
  await flushPromises()
  return { w, router, store }
}

async function openEntry(w: VueWrapper, name: string) {
  const row = w.findAll('.fs-item').find((item) => item.find('.fs-name-text').text() === name)
  expect(row, `目录中应有 ${name}`).toBeDefined()
  await row!.trigger('click')
  await flushPromises()
}

describe('文件预览返回列表', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    localStorage.clear()
    sessionStorage.clear()
    localStorage.setItem('lr.currentWorkspace', 'project')
    mocks.workspaces.mockResolvedValue([
      workspace('project', '/srv/project'), workspace('other', '/srv/other'),
    ])
    mocks.me.mockResolvedValue({ root: '/srv' })
    mocks.search.mockResolvedValue({
      results: [{ path: '/srv/shared/note.txt', rel: 'note.txt', dir: false, size: 12 }],
      truncated: true,
      scanned: 1,
    })
    dirs.clear()
    dirs.set('/srv', [entry('/srv/project', true), entry('/srv/shared', true), ...files('/srv')])
    dirs.set('/srv/project', [entry('/srv/project/sub', true)])
    dirs.set('/srv/project/sub', files('/srv/project/sub'))
    dirs.set('/srv/shared', files('/srv/shared'))
    dirs.set('/srv/other', [])
    mocks.list.mockImplementation(async (path: string) => {
      if (!dirs.has(path)) throw new Error('directory missing')
      return dirs.get(path)
    })
  })

  afterEach(() => {
    live.splice(0).forEach((w) => w.unmount())
    localStorage.clear()
    sessionStorage.clear()
  })

  it.each([
    { dir: '/srv/shared', file: 'picture.png', browserBack: false },
    { dir: '/srv/shared', file: 'note.txt', browserBack: true },
    { dir: '/srv', file: 'picture.png', browserBack: true },
    { dir: '/srv/project/sub', file: 'note.txt', browserBack: false },
  ])('从 $dir 预览 $file 后返回原目录', async ({ dir, file, browserBack }) => {
    const { w, router } = await mountFiles()
    if (dir === '/srv/project/sub') {
      await openEntry(w, 'sub')
    } else {
      await w.get('[aria-label="上一级"]').trigger('click')
      await flushPromises()
      if (dir === '/srv/shared') await openEntry(w, 'shared')
    }
    expect(w.get('.fs-crumb').attributes('title')).toBe(dir)
    await openEntry(w, file)
    expect(router.currentRoute.value.path).toBe('/files/file')
    expect(w.find('.file-view').exists()).toBe(true)
    if (browserBack) router.back()
    else await w.get('[aria-label="返回列表"]').trigger('click')
    await flushPromises()
    expect(router.currentRoute.value.path).toBe('/files')
    expect(w.get('.fs-crumb').attributes('title')).toBe(dir)
    expect(mocks.list).toHaveBeenLastCalledWith(dir)
  })

  it('首次加载工作区信息时保留记住的子目录', async () => {
    sessionStorage.setItem('lr.lastDir', '/srv/project/sub')
    const { w } = await mountFiles(false)
    expect(w.get('.fs-crumb').attributes('title')).toBe('/srv/project/sub')
  })

  it('Windows 盘根下的项目外目录也能恢复', async () => {
    mocks.me.mockResolvedValue({ root: 'D:/' })
    mocks.workspaces.mockResolvedValue([workspace('project', 'D:/project')])
    dirs.set('D:/shared', files('D:/shared'))
    sessionStorage.setItem('lr.lastDir', 'D:/shared')
    const { w } = await mountFiles()
    expect(w.get('.fs-crumb').attributes('title')).toBe('D:/shared')
  })

  it('切换工作区后回到新项目目录', async () => {
    sessionStorage.setItem('lr.lastDir', '/srv/shared')
    const { w, store } = await mountFiles()
    store.select('other')
    await flushPromises()
    expect(w.get('.fs-crumb').attributes('title')).toBe('/srv/other')
  })

  it('记住的目录不在服务端根目录内时回到项目目录', async () => {
    sessionStorage.setItem('lr.lastDir', '/srv-other/shared')
    const { w } = await mountFiles()
    expect(w.get('.fs-crumb').attributes('title')).toBe('/srv/project')
    expect(mocks.list).not.toHaveBeenCalledWith('/srv-other/shared')
  })

  it('记住的目录已删除时回到项目目录', async () => {
    sessionStorage.setItem('lr.lastDir', '/srv/project/deleted')
    const { w } = await mountFiles()
    expect(mocks.list).toHaveBeenCalledWith('/srv/project/deleted')
    expect(w.get('.fs-crumb').attributes('title')).toBe('/srv/project')
  })

  it.each([
    { mode: 'name', file: 'picture.png', browserBack: false },
    { mode: 'content', file: 'note.txt', browserBack: true },
  ])('预览 $file 返回后保留搜索和排序状态', async ({ mode, file, browserBack }) => {
    sessionStorage.setItem('lr.lastDir', '/srv/shared')
    const { w, router } = await mountFiles()
    await w.get('.fs-search input').setValue('f.*content')
    w.get('.fs-search-opts').findComponent(NRadioGroup).vm.$emit('update:value', mode)
    await w.get('[title="区分大小写"]').trigger('click')
    await w.get('[title="正则表达式"]').trigger('click')
    w.findComponent(NSelect).vm.$emit('update:value', 'size')
    await w.get('[aria-label="升序"]').trigger('click')
    await w.get('.fs-search input').trigger('keydown', { key: 'Enter' })
    await flushPromises()
    if (mode === 'content') {
      await w.get('.search-head').findAll('button').find((b) => b.text() === '替换')!.trigger('click')
      await w.get('.search-replace input').setValue('replacement')
    }
    const resultsText = w.get('.search-block').text()
    expect(w.findAll('.fs-name-text').map((item) => item.text())).toEqual(['picture.png', 'note.txt'])
    if (mode === 'content') {
      await w.get('.search-path').trigger('click')
      await flushPromises()
    } else await openEntry(w, file)
    expect(router.currentRoute.value.query.path).toBe(`/srv/shared/${file}`)
    if (browserBack) router.back()
    else await w.get('[aria-label="返回列表"]').trigger('click')
    await flushPromises()
    expect((w.get('.fs-search input').element as HTMLInputElement).value).toBe('f.*content')
    expect(w.get('.fs-search-opts').findComponent(NRadioGroup).props('value')).toBe(mode)
    for (const title of ['区分大小写', '正则表达式']) {
      expect(w.findAllComponents(NButton).find((button) => button.attributes('title') === title)!.props('type'))
        .toBe('primary')
    }
    expect(w.get('.search-block').text()).toBe(resultsText)
    expect(w.findComponent(NSelect).props('value')).toBe('size')
    expect(w.find('[aria-label="降序"]').exists()).toBe(true)
    expect(w.findAll('.fs-name-text').map((item) => item.text())).toEqual(['picture.png', 'note.txt'])
    if (mode === 'content') {
      expect((w.get('.search-replace input').element as HTMLInputElement).value).toBe('replacement')
    }
    expect(mocks.search).toHaveBeenCalledTimes(1)
  })

  it('连续预览不同文件后保留尚未执行的搜索输入', async () => {
    sessionStorage.setItem('lr.lastDir', '/srv/shared')
    const { w, router } = await mountFiles()
    await w.get('.fs-search input').setValue('unfinished query')
    for (const file of ['picture.png', 'note.txt']) {
      await openEntry(w, file)
      expect(router.currentRoute.value.query.path).toBe(`/srv/shared/${file}`)
      expect(w.get('.fv-title').text()).toContain(file)
      await w.get('[aria-label="返回列表"]').trigger('click')
      await flushPromises()
      expect((w.get('.fs-search input').element as HTMLInputElement).value).toBe('unfinished query')
    }
    expect(mocks.search).not.toHaveBeenCalled()
  })

  it('返回时更新文件信息,保留仍存在的勾选项', async () => {
    sessionStorage.setItem('lr.lastDir', '/srv/shared')
    const { w } = await mountFiles()
    await w.get('.fs-search input').setValue('file')
    await w.get('.fs-search input').trigger('keydown', { key: 'Enter' })
    await flushPromises()
    await w.get('[aria-label="批量选择"]').trigger('click')
    await openEntry(w, 'picture.png')
    await openEntry(w, 'note.txt')
    expect(w.get('.fs-batch-n').text()).toBe('已选 2 项')
    await w.get('.search-path').trigger('click')
    await flushPromises()
    dirs.set('/srv/shared', [{ ...entry('/srv/shared/note.txt'), size: 256 }])
    await w.get('[aria-label="返回列表"]').trigger('click')
    await flushPromises()
    expect(w.find('[aria-label="退出选择"]').exists()).toBe(true)
    expect(w.get('.fs-batch-n').text()).toBe('已选 1 项')
    expect(w.get('.fs-item[aria-checked="true"] .fs-name-text').text()).toBe('note.txt')
    expect(w.get('.fs-size').text()).toBe('256 B')
  })

  it('离开文件模块并切换项目后清理原搜索状态', async () => {
    sessionStorage.setItem('lr.lastDir', '/srv/shared')
    const { w, router, store } = await mountFiles()
    await w.get('.fs-search input').setValue('old query')
    await w.get('.fs-search input').trigger('keydown', { key: 'Enter' })
    await flushPromises()
    await w.get('.search-path').trigger('click')
    await flushPromises()
    await router.push('/')
    await flushPromises()
    store.select('other')
    await router.push('/files')
    await flushPromises()
    expect(w.get('.fs-crumb').attributes('title')).toBe('/srv/other')
    expect((w.get('.fs-search input').element as HTMLInputElement).value).toBe('')
    expect(w.find('.search-block').exists()).toBe(false)
  })

  it('预览期间原目录被删除时清理旧目录的搜索结果', async () => {
    sessionStorage.setItem('lr.lastDir', '/srv/shared')
    const { w } = await mountFiles()
    await w.get('.fs-search input').setValue('old query')
    await w.get('.fs-search input').trigger('keydown', { key: 'Enter' })
    await flushPromises()
    await w.get('.search-path').trigger('click')
    await flushPromises()
    dirs.delete('/srv/shared')
    await w.get('[aria-label="返回列表"]').trigger('click')
    await flushPromises()
    expect(w.get('.fs-crumb').attributes('title')).toBe('/srv/project')
    expect(w.find('.search-block').exists()).toBe(false)
  })
})
