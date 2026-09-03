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
  renameWorkspace: (id: string, name: string) =>
    request<{ ok: boolean }>('PUT', `/api/workspaces/${id}`, { name }),
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
  fsArchiveList: (path: string, limit?: number) => {
    const qs = new URLSearchParams({ path })
    if (limit) qs.set('limit', String(limit))
    return request<FsArchiveList>('GET', `/api/fs/archive/list?${qs.toString()}`)
  },
  fsUpload: (dir: string, file: File) => {
    const fd = new FormData()
    fd.append('dir', dir)
    fd.append('file', file)
    return fetch('/api/fs/upload', { method: 'POST', body: fd, credentials: 'same-origin' })
  },
  fsArchiveUrl: (path: string) => `/api/fs/archive?path=${encodeURIComponent(path)}`,
  fsDownloadUrl: (path: string) => `/api/fs/download?path=${encodeURIComponent(path)}`,
  // 图片预览走同一个下载端点,inline=1 让后端改用 Content-Disposition: inline(仅图片白名单生效)。
  fsInlineUrl: (path: string) => `/api/fs/download?path=${encodeURIComponent(path)}&inline=1`,
  // ---- Git ----
  gitRepo: (path: string) => request<GitRepo>('GET', `/api/git/repo?path=${encodeURIComponent(path)}`),
  gitStatus: (path: string) => request<GitStatus>('GET', `/api/git/status?path=${encodeURIComponent(path)}`),
  gitDiff: (path: string, scope: string, file?: string, ref?: string) =>
    requestText(`/api/git/diff?path=${encodeURIComponent(path)}&scope=${scope}${file ? `&file=${encodeURIComponent(file)}` : ''}${ref ? `&ref=${encodeURIComponent(ref)}` : ''}`),
  // 某个提交里某个文件的全文。fsRead 读的是工作区版本,看历史提交时不能用它。
  gitShow: (path: string, ref: string, file: string) =>
    requestText(`/api/git/show?path=${encodeURIComponent(path)}&ref=${encodeURIComponent(ref)}&file=${encodeURIComponent(file)}`),
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
  // 只更新远端跟踪引用(refs/remotes/*),不合并 —— 状态里的 ↓behind 靠它才有意义:
  // git status 不联网,而 pull 内部虽然也 fetch,但紧接着就合并掉了。remote 空 = 所有远端。
  gitFetch: (path: string, remote?: string) => request<{ out: string }>('POST', '/api/git/fetch', { path, remote }),
  // 远端管理。value:add/set-url 时是 URL,rename 时是新名字,remove 不用。
  gitRemote: (path: string, op: RemoteOp, name: string, value?: string) =>
    request<{ out: string }>('POST', '/api/git/remote', { path, op, name, value }),
  // 回填 git 交互认证的用户名/密码(连接已在 WS 中保持)。空值 = 取消。
  gitAuthAnswer: (token: string, username?: string, password?: string) =>
    request<{ ok: boolean }>('POST', '/api/git/auth/answer', { token, username, password }),
  gitBranch: (path: string, op: string, name?: string, start?: string) =>
    request<{ out: string }>('POST', '/api/git/branch', { path, op, name, start }),
  gitStash: (path: string, op: string, message?: string) =>
    request<{ out: string }>('POST', '/api/git/stash', { path, op, message }),
  // 撤回改动。mode:worktree=丢弃工作区改动;all=暂存区与工作区一起回到 HEAD;
  // untracked=直接删除未跟踪文件(Git 里没有副本)。
  gitRestore: (path: string, files: string[], mode: RestoreMode) =>
    request<{ out: string }>('POST', '/api/git/restore', { path, files, mode }),
  // 回滚提交。op:revert=生成一个反向提交;abort=放弃卡在冲突里的 revert。
  gitRevert: (path: string, op: 'revert' | 'abort', hash?: string) =>
    request<{ out: string }>('POST', '/api/git/revert', { path, op, hash }),
  // ---- AI 配置切换 ----
  aiProviders: (app: AiApp) => request<AiProviderList>('GET', `/api/ai/providers?app=${app}`),
  aiProviderCreate: (p: AiProviderInput) => request<AiProvider>('POST', '/api/ai/provider', p),
  aiProviderUpdate: (p: AiProviderInput & { id: string }) => request<AiProvider>('PUT', '/api/ai/provider', p),
  aiProviderDelete: (app: AiApp, id: string) =>
    request<{ ok: boolean }>('DELETE', `/api/ai/provider?app=${app}&id=${encodeURIComponent(id)}`),
  aiSwitch: (app: AiApp, id: string) => request<{ ok: boolean }>('POST', '/api/ai/switch', { app, id }),
  aiExportUrl: () => '/api/ai/export',
  aiImport: (file: File) => {
    const fd = new FormData()
    fd.append('file', file)
    return fetch('/api/ai/import', { method: 'POST', body: fd, credentials: 'same-origin' })
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
  op: 'mkdir' | 'touch' | 'rename' | 'copy' | 'move' | 'delete'
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

// 压缩包预览:name 是包内相对路径(正斜杠)。
export interface FsArchiveEntry {
  name: string
  dir: boolean
  size: number
  mtime: string
}

export interface FsArchiveList {
  entries: FsArchiveEntry[]
  truncated: boolean
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
  // 有一次 revert 卡在冲突里(REVERT_HEAD 还在)。
  reverting: boolean
}

// 撤回改动的范围,与后端 Service.Restore 的 mode 一一对应。
export type RestoreMode = 'worktree' | 'all' | 'untracked'

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

// 远端管理的操作,与后端 Service.RemoteOp 的 op 一一对应。
export type RemoteOp = 'add' | 'remove' | 'rename' | 'set-url'

export type AiApp = 'claude' | 'codex'

// AiProvider.config 就是要落到真实配置文件里的内容:claude 存 settings.json 本身,
// codex 存 { auth, config }(config 是 config.toml 全文)。
export interface AiProvider {
  id: string
  app: AiApp
  name: string
  category: string
  websiteUrl: string
  config: unknown
  isCurrent: boolean
  createdAt: string
  updatedAt: string
}

export interface AiProviderInput {
  app: AiApp
  name: string
  category?: string
  websiteUrl?: string
  config: unknown
}

export interface AiProviderList {
  app: AiApp
  current: string
  providers: AiProvider[]
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
