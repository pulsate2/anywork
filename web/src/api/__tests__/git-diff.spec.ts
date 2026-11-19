import { afterEach, describe, expect, it, vi } from 'vitest'
import { api } from '@/api/client'

describe('全文件差异请求', () => {
  afterEach(() => vi.unstubAllGlobals())

  it.each([false, true])('只在全文件模式 %s 时传 full=1,并保留文件和提交参数', async full => {
    const fetch = vi.fn().mockResolvedValue(new Response('diff text'))
    vi.stubGlobal('fetch', fetch)
    expect(await api.gitDiff('/repo path', 'commit', '中文 文件.ts', 'abcdef0', full)).toBe('diff text')
    const url = new URL(fetch.mock.calls[0][0], 'http://localhost')
    expect(url.searchParams.get('full')).toBe(full ? '1' : null)
    expect(url.searchParams.get('path')).toBe('/repo path')
    expect(url.searchParams.get('scope')).toBe('commit')
    expect(url.searchParams.get('file')).toBe('中文 文件.ts')
    expect(url.searchParams.get('ref')).toBe('abcdef0')
  })
})
