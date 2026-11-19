import { describe, expect, it } from 'vitest'
import type { GitCommit } from '@/api/client'
import { buildCommitGraph, commitDate } from '@/utils/gitHistory'

function commit(hash: string, parents: string[] = []): GitCommit {
  return { hash, short: hash.slice(0, 7), parents, refs: [], subject: hash, date: '', author: '', email: '' }
}

describe('Git 提交分支图', () => {
  it('连接真实父提交,根提交不再向下延伸', () => {
    const graph = buildCommitGraph([commit('a', ['b']), commit('b', ['c']), commit('c')])
    expect(graph.width).toBe(36)
    expect(graph.rows.map(r => r.x)).toEqual([12, 12, 12])
    expect(graph.rows.map(r => r.color)).toEqual([0, 0, 0])
    expect(graph.rows[0].paths).toEqual([{ d: 'M 12 18 L 12 36', color: 0 }])
    expect(graph.rows[1].paths).toHaveLength(2)
    expect(graph.rows[2].paths).toEqual([{ d: 'M 12 0 L 12 18', color: 0 }])
  })

  it('合并提交分出两条泳道,共享祖先汇回主线', () => {
    const graph = buildCommitGraph([
      commit('merge', ['main', 'feature']), commit('feature', ['base']), commit('main', ['base']), commit('base'),
    ])
    expect(graph.width).toBe(40)
    expect(graph.rows[0].merge).toBe(true)
    expect(graph.rows[0].paths.map(p => p.color)).toEqual([0, 1])
    expect(graph.rows[1].x).toBe(28)
    expect(graph.rows[1].color).toBe(1)
    expect(graph.rows[2].x).toBe(12)
    expect(graph.rows[3].x).toBe(12)
    expect(graph.rows[3].color).toBe(0)
  })

  it('多分支及 octopus merge 不丢失父提交', () => {
    const graph = buildCommitGraph([
      commit('m', ['a', 'b', 'c']), commit('a', ['root']), commit('b', ['root']), commit('c', ['root']), commit('root'),
    ])
    expect(graph.width).toBe(56)
    expect(graph.rows[0].paths).toHaveLength(3)
    expect(graph.rows.flatMap(r => r.paths).every(p => !p.d.includes('-4'))).toBe(true)
    expect(graph.rows[graph.rows.length - 1]?.paths).toHaveLength(1)
  })

  it('加载下一页不会改变先前的节点和连线', () => {
    const commits = [
      commit('m', ['a', 'b']), commit('b', ['root']), commit('a', ['root']), commit('root'),
    ]
    const firstPage = buildCommitGraph(commits.slice(0, 2))
    const complete = buildCommitGraph(commits)
    expect(complete.rows.slice(0, 2)).toEqual(firstPage.rows)
  })

  it('筛选隐藏中间提交时不伪造跨行连接', () => {
    const graph = buildCommitGraph([commit('a', ['hidden']), commit('b')], false)
    expect(graph.rows[0].paths).toEqual([])
    expect(graph.rows[1].paths).toEqual([])
    expect(graph.rows.map(r => r.x)).toEqual([12, 12])
    const connected = buildCommitGraph([commit('a', ['b']), commit('b')], false)
    expect(connected.rows[0].paths).toHaveLength(1)
  })

  it('空历史与空父提交安全返回', () => {
    expect(buildCommitGraph([])).toEqual({ width: 36, rows: [] })
    expect(buildCommitGraph([{ ...commit('root'), parents: null as unknown as string[] }]).rows[0].paths).toEqual([])
  })
})

describe('提交时间', () => {
  it('显示 Git 本地时间并保留分钟', () => {
    expect(commitDate('2026-11-08T19:55:03+08:00')).toBe('2026-11-08 19:55:03')
    expect(commitDate('2026-11-08T19:55:03+08:00', true)).toBe('11-08 19:55')
    expect(commitDate('2026-11-08', true)).toBe('11-08')
  })
})
