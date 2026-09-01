// 轻量 HTTP 客户端:自动带 Cookie、JSON、统一错误处理。
// 会话 Cookie 由浏览器自动携带(SameSite=Strict)。

export class ApiError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

// 会话失效:回登录页。整页跳转,避免 client 反向依赖 router 形成循环。
function handleUnauthorized() {
  if (location.pathname !== '/login') location.replace('/login')
}

async function fail(res: Response, path: string): Promise<never> {
  // 登录接口自身的 401 表示密码错误,由 LoginView 展示,不跳转。
  if (res.status === 401 && path !== '/api/login') handleUnauthorized()
  let msg = res.statusText
  try {
    const text = await res.text()
    if (text) msg = text
  } catch { /* ignore */ }
  throw new ApiError(res.status, msg)
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: body !== undefined ? { 'Content-Type': 'application/json' } : undefined,
    body: body !== undefined ? JSON.stringify(body) : undefined,
    credentials: 'same-origin',
  })
  if (!res.ok) await fail(res, path)
  if (res.status === 204) return undefined as T
  return (await res.json()) as T
}

// 纯文本接口:/api/fs/read 与 /api/git/diff 直接返回文件正文/diff,不是 JSON。
async function requestText(path: string): Promise<string> {
  const res = await fetch(path, { credentials: 'same-origin' })
  if (!res.ok) await fail(res, path)
  return await res.text()
}

export const api = {
  login: (password: string) => request<{ ok: boolean }>('POST', '/api/login', { password }),
  logout: () => request<{ ok: boolean }>('POST', '/api/logout'),
  me: () => request<{ readonly: boolean; root: string }>('GET', '/api/me'),
  health: () => request<{ status: string; now: string }>('GET', '/api/health'),
  workspaces: () => request<Workspace[]>('GET', '/api/workspaces'),
  createWorkspace: (w: { name: string; path: string; favorite?: boolean }) =>
    request<{ ok: boolean; id: string }>('POST', '/api/workspaces', w),
  deleteWorkspace: (id: string) => request<{ ok: boolean }>('DELETE', `/api/workspaces/${id}`),
  sysinfo: () => request<SysInfo>('GET', '/api/sysinfo'),
  // ---- 文件操作 ----
  fsList: (path: string) => request<FsEntry[]>('GET', `/api/fs/list?path=${encodeURIComponent(path)}`),
  fsRead: (path: string) => requestText(`/api/fs/read?path=${encodeURIComponent(path)}`),
  fsWrite: (path: string, text: string) => request<{ ok: boolean }>('POST', '/api/fs/write', { path, text }),
  fsOp: (body: FsOp) => request<{ ok: boolean }>('POST', '/api/fs/op', body),
  fsSearch: (opt: FsSearchQuery) => {
    const qs = new URLSearchParams({ path: opt.path, q: opt.q })
    if (opt.mode === 'name') qs.set('mode', 'name')
    if (opt.regex) qs.set('regex', '1')
    if (opt.caseSensitive) qs.set('case', '1')
    if (opt.limit) qs.set('limit', String(opt.limit))
    return request<FsSearchOutcome>('GET', `/api/fs/search?${qs.toString()}`)
  },
  fsReplace: (body: { files: string[]; q: string; replace: string; regex?: boolean; case?: boolean }) =>
    request<{ files: number; count: number }>('POST', '/api/fs/replace', body),
  fsExtract: (dest: string, archive: string) => request<{ ok: boolean }>('POST', '/api/fs/extract', { dest, archive }),
  fsUpload: (dir: string, file: File) => {
    const fd = new FormData()
    fd.append('dir', dir)
    fd.append('file', file)
    return fetch('/api/fs/upload', { method: 'POST', body: fd, credentials: 'same-origin' })
  },
  fsArchiveUrl: (path: string) => `/api/fs/archive?path=${encodeURIComponent(path)}`,
  fsDownloadUrl: (path: string) => `/api/fs/download?path=${encodeURIComponent(path)}`,
  // ---- Git ----
  gitRepo: (path: string) => request<GitRepo>('GET', `/api/git/repo?path=${encodeURIComponent(path)}`),
  gitStatus: (path: string) => request<GitStatus>('GET', `/api/git/status?path=${encodeURIComponent(path)}`),
  gitDiff: (path: string, scope: string, file?: string) =>
    requestText(`/api/git/diff?path=${encodeURIComponent(path)}&scope=${scope}${file ? `&file=${encodeURIComponent(file)}` : ''}`),
  gitLog: (path: string, n?: number, skip?: number) => {
    const qs = new URLSearchParams({ path })
    if (n) qs.set('n', String(n))
    if (skip) qs.set('skip', String(skip))
    return request<GitCommit[]>('GET', `/api/git/log?${qs.toString()}`)
  },
  gitBranches: (path: string) => request<GitBranchList>('GET', `/api/git/branches?path=${encodeURIComponent(path)}`),
  gitRemotes: (path: string) => request<GitRemote[]>('GET', `/api/git/remotes?path=${encodeURIComponent(path)}`),
  gitStage: (path: string, files: string[], reset?: boolean) =>
    request<{ ok: boolean }>('POST', `/api/git/stage?op=${reset ? 'reset' : 'add'}`, { path, files }),
  gitCommit: (path: string, message: string, addAll: boolean) =>
    request<{ out: string }>('POST', '/api/git/commit', { path, message, addAll }),
  // remote 留空则走当前分支的上游配置(等价于裸 git push)。
  gitPush: (path: string, opt?: { remote?: string; branch?: string; setUpstream?: boolean }) =>
    request<{ out: string }>('POST', '/api/git/push', { path, ...opt }),
  gitPull: (path: string) => request<{ out: string }>('POST', '/api/git/pull', { path }),
  gitBranch: (path: string, op: string, name?: string, start?: string) =>
    request<{ out: string }>('POST', '/api/git/branch', { path, op, name, start }),
  gitStash: (path: string, op: string, message?: string) =>
    request<{ out: string }>('POST', '/api/git/stash', { path, op, message }),
  // ---- AI 配置档案 ----
  aiProfiles: () => request<AiProfile[]>('GET', '/api/ai/profiles'),
  aiProfile: (name: string) => request<AiProfile>('GET', `/api/ai/profile?name=${encodeURIComponent(name)}`),
  aiCreate: (b: { name: string; env?: Record<string,string>; preset?: string; cloneFrom?: string }) =>
    request<AiProfile>('POST', '/api/ai/profile', b),
  aiUpdate: (name: string, env: Record<string,string>) =>
    request<AiProfile>('PUT', '/api/ai/profile', { name, env }),
  aiDelete: (name: string) => request<{ ok: boolean }>('DELETE', `/api/ai/profile?name=${encodeURIComponent(name)}`),
  aiActive: () => request<{ name: string }>('GET', '/api/ai/active'),
  aiSetActive: (name: string) => request<{ ok: boolean }>('POST', '/api/ai/active', { name }),
  aiExportUrl: (name: string) => `/api/ai/profile/export?name=${encodeURIComponent(name)}`,
  aiImport: (name: string, file: File) => {
    const fd = new FormData()
    fd.append('name', name)
    fd.append('file', file)
    return fetch('/api/ai/profile/import', { method: 'POST', body: fd, credentials: 'same-origin' })
  },
  // ---- 备份 ----
  backupJobs: () => request<BackupJob[]>('GET', '/api/backup/jobs'),
  backupSave: (j: Partial<BackupJob>) => request<BackupJob>('POST', '/api/backup/job', j),
  backupDelete: (id: string) => request<{ ok: boolean }>('DELETE', `/api/backup/job?id=${encodeURIComponent(id)}`),
  backupRun: (id: string) => request<{ started: boolean }>('POST', `/api/backup/run?id=${encodeURIComponent(id)}`),
  backupSnapshots: (id: string) => request<BackupSnapshot[]>('GET', `/api/backup/snapshots?id=${encodeURIComponent(id)}`),
  backupRestore: (id: string, snapshot?: string) =>
    request<{ ok: boolean }>('POST', `/api/backup/restore?id=${encodeURIComponent(id)}`, { snapshot }),
  backupDownloadUrl: (id: string, snapshot: string) => `/api/backup/download?id=${encodeURIComponent(id)}&snapshot=${encodeURIComponent(snapshot)}`,

  pushStatus: () => request<PushStatus>('GET', '/api/push/status'),
  pushSubscribe: (sub: PushSubscriptionJSON) => request<{ ok: boolean }>('POST', '/api/push/subscribe', sub),
  pushUnsubscribe: (endpoint?: string) => request<{ ok: boolean; removed: number }>('POST', '/api/push/unsubscribe', { endpoint }),
  pushTest: () => request<{ ok: boolean; sent: number; failed: number }>('POST', '/api/push/test'),
}

export interface PushSubscriptionJSON {
  endpoint: string
  keys: { p256dh: string; auth: string }
}

export interface PushStatus {
  vapidPublicKey: string
  count: number
  configured: boolean
  idleSeconds: number
}

export interface FsEntry {
  name: string
  path: string
  dir: boolean
  size: number
  mtime: string
  mode: string
  symlink: boolean
  target?: string
}

export interface FsOp {
  op: 'mkdir' | 'rename' | 'copy' | 'move' | 'delete'
  path?: string
  from?: string
  to?: string
}

export interface FsSearchQuery {
  path: string
  q: string
  mode: 'name' | 'content'
  regex?: boolean
  caseSensitive?: boolean
  limit?: number
}

// col/len 是 JS 字符串下标(UTF-16 码元),可直接用于 text.slice。
export interface FsSearchMatch {
  col: number
  len: number
}

export interface FsSearchResult {
  path: string
  rel: string
  dir: boolean
  size: number
  line?: number
  text?: string
  matches?: FsSearchMatch[]
}

export interface FsSearchOutcome {
  results: FsSearchResult[]
  truncated: boolean
  scanned: number
}

export interface Workspace {
  id: string
  name: string
  path: string
  favorite: boolean
  sortOrder: number
  createdAt: string
}

export interface SysInfo {
  cpu: { load: number; cores: number; percent: number }
  memory: { totalMB: number; usedMB: number; freeMB: number; usedPct: number }
  disk: { totalGB: number; usedGB: number; freeGB: number; usedPct: number }
}


export interface GitRepo {
  dir: string
  root: string
  branch: string
  repo: boolean
  short: string
}

export interface GitEntry {
  raw: string
  x: string
  y: string
  path: string
  orig?: string
  kind: string
}

export interface GitStatus {
  branch: string
  upstream?: string
  ahead: number
  behind: number
  staged: GitEntry[]
  unstaged: GitEntry[]
  untracked: GitEntry[]
  conflicted: GitEntry[]
  clean: boolean
  initial: boolean
  detached: boolean
}

export interface GitCommit {
  hash: string
  short: string
  parents: string[]
  refs: string[]
  subject: string
  date: string
}

export interface GitBranch {
  name: string
  current: boolean
  remote: boolean
  upstream?: string
  date?: string
  subject?: string
}

export interface GitBranchList {
  current: string
  local: GitBranch[]
  remote: GitBranch[]
}

export interface GitRemote {
  name: string
  url: string
}

export interface AiProfile {
  name: string
  env: Record<string, string>
  hasClaude: boolean
  hasCodex: boolean
  createdAt: string
  updatedAt: string
}

export interface BackupJob {
  id: string
  name: string
  sourceDir: string
  webdavUrl: string
  webdavUser: string
  webdavPass: string
  schedule: string
  excludes: string[]
  retention: number
  autoRestore: boolean
  enabled: boolean
  createdAt: string
  nextRun?: string
  lastRun?: string
  lastOk?: boolean
  lastErr?: string
  running?: boolean
}

export interface BackupSnapshot {
  name: string
  size: number
}
