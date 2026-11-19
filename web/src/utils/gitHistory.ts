import type { GitCommit } from '@/api/client'

export const GIT_LOG_PAGE = 30

// 进入文件差异页之前保存在 /git 的历史记录里,返回时恢复筛选、选中项和滚动位置。
export interface GitHistoryState {
  search: string
  branch: string
  author: string
  selectedHash: string
  limit: number
  scrollTop: number
}

interface Lane {
  hash: string
  color: number
}

export interface CommitGraphRow {
  hash: string
  x: number
  color: number
  merge: boolean
  paths: { d: string; color: number }[]
}

const STEP = 16
const HEIGHT = 36
const MID = HEIGHT / 2
const x = (lane: number) => 12 + lane * STEP

function through(from: number, to: number): string {
  if (from === to) return `M ${from} 0 L ${to} ${HEIGHT}`
  return `M ${from} 0 L ${from} 9 C ${from} 18 ${to} 18 ${to} 27 L ${to} ${HEIGHT}`
}

function outgoing(from: number, to: number): string {
  if (from === to) return `M ${from} ${MID} L ${to} ${HEIGHT}`
  return `M ${from} ${MID} C ${from} 28 ${to} 26 ${to} ${HEIGHT}`
}

// 根据真实父提交分配泳道。逐行携带等待出现的父提交,合并时复用已有泳道,
// 分叉时才分配新颜色;追加下一页不会改变前面节点的颜色或连接。
// 说明/作者筛选会跳过中间提交,此时只连接实际显示的父子,不伪造连续历史。
export function buildCommitGraph(commits: GitCommit[], connectMissingParents = true) {
  let lanes: Lane[] = []
  let nextColor = 0
  let maxLanes = 1
  const visible = new Set(commits.map(c => c.hash))
  const rows: CommitGraphRow[] = []

  for (const commit of commits) {
    let lane = lanes.findIndex(l => l.hash === commit.hash)
    const incoming = lane >= 0
    if (!incoming) {
      lane = lanes.length
      lanes.push({ hash: commit.hash, color: nextColor++ % 6 })
    }
    const before = lanes.slice()
    const node = before[lane]
    const after = before.filter((_, index) => index !== lane)
    const parents = [...new Set(commit.parents || [])]
      .filter(parent => connectMissingParents || visible.has(parent))
    let insertAt = Math.min(lane, after.length)
    for (const [index, parent] of parents.entries()) {
      const existing = after.findIndex(l => l.hash === parent)
      if (existing < 0) {
        after.splice(insertAt, 0, { hash: parent, color: index === 0 ? node.color : nextColor++ % 6 })
        insertAt++
      } else if (index === 0 && lane <= existing) {
        // 主线与支线汇合到同一祖先时,保留靠左主线的颜色。
        after[existing] = { ...after[existing], color: node.color }
      }
    }
    const paths: CommitGraphRow['paths'] = []
    before.forEach((pending, index) => {
      if (index === lane) {
        if (incoming) paths.push({ d: `M ${x(index)} 0 L ${x(lane)} ${MID}`, color: node.color })
      } else {
        const target = after.findIndex(l => l.hash === pending.hash)
        paths.push({ d: through(x(index), x(target)), color: pending.color })
      }
    })
    for (const parent of parents) {
      const target = after.findIndex(l => l.hash === parent)
      paths.push({ d: outgoing(x(lane), x(target)), color: after[target].color })
    }
    maxLanes = Math.max(maxLanes, before.length, after.length)
    rows.push({ hash: commit.hash, x: x(lane), color: node.color, merge: (commit.parents || []).length > 1, paths })
    lanes = after
  }
  return { rows, width: Math.max(36, x(maxLanes - 1) + 12) }
}

// 保留 Git 记录里的当地时间;完整 ISO 时间(含时区)仍在 title / datetime 中。
export function commitDate(date: string, short = false): string {
  const text = date.replace('T', ' ')
  return short ? text.slice(5, 16) : text.slice(0, 19)
}
