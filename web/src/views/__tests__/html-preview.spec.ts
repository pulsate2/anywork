// HTML 文件的只读预览:走 iframe srcdoc。
// 这里钉的主要是安全不变量 —— 后端不把 html 当 inline 直出(见 internal/fs 的
// inlineTypes)就是为了堵同源 XSS,前端要是给 srcdoc 帧配上 allow-same-origin,
// 等于从这条路把同一个洞原样开回来:页面里的脚本就能摸到本应用的 DOM 与 Cookie。
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { NMessageProvider } from 'naive-ui'
import FilesFileView from '@/views/FilesFileView.vue'

const HTML = '<!doctype html><meta charset="utf-8"><h1>hi</h1>'
let query: Record<string, string> = {}

vi.mock('vue-router', () => ({
  useRoute: () => ({ query }),
  useRouter: () => ({ back: vi.fn(), replace: vi.fn() }),
}))

vi.mock('@/api/client', () => ({
  api: {
    fsRead: async () => HTML,
    fsWrite: async () => ({ ok: true }),
    fsDownloadUrl: (p: string) => `/api/fs/download?path=${encodeURIComponent(p)}`,
    fsInlineUrl: (p: string) => `/api/fs/download?path=${encodeURIComponent(p)}&inline=1`,
  },
}))

function mountView() {
  return mount({
    components: { NMessageProvider, FilesFileView },
    template: '<n-message-provider><files-file-view /></n-message-provider>',
  })
}

describe('HTML 预览', () => {
  beforeEach(() => {
    query = { path: '/w/index.html', name: 'index.html' }
  })

  it('默认给预览:iframe 喂 srcdoc,脚本放行但同源不给', async () => {
    const w = mountView()
    await flushPromises()
    const frame = w.find('iframe.fv-html')
    expect(frame.exists()).toBe(true)
    expect((frame.element as HTMLIFrameElement).srcdoc).toBe(HTML)
    const sandbox = frame.attributes('sandbox') || ''
    expect(sandbox).toContain('allow-scripts')
    expect(sandbox).not.toContain('allow-same-origin')
    expect(w.find('.code-editor').exists()).toBe(false)
  })

  it('从搜索结果进来先给源码:命中标记只存在于源码视图', async () => {
    query = { path: '/w/index.html', name: 'index.html', q: 'hi' }
    const w = mountView()
    await flushPromises()
    expect(w.find('iframe.fv-html').exists()).toBe(false)
    expect(w.find('.code-editor').exists()).toBe(true)
  })

  it('普通文本文件没有预览帧', async () => {
    query = { path: '/w/a.txt', name: 'a.txt' }
    const w = mountView()
    await flushPromises()
    expect(w.find('iframe.fv-html').exists()).toBe(false)
    expect(w.find('.code-editor').exists()).toBe(true)
  })
})
