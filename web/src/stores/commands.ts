// 终端命令收藏 + 输入历史。只存在浏览器里(localStorage),不上服务器:命令里常带着
// 密码、token 这类东西,存回服务端等于给它们又多了一份副本。
// 与 workspace 那份存储的约定一致:键名统一 lr. 前缀,读写都当它随时可能失败。
import { ref, computed } from 'vue'
import { defineStore } from 'pinia'

const FAV_KEY = 'lr.termFavorites'
const HIST_KEY = 'lr.termHistory'
const REC_KEY = 'lr.termHistoryOn'

// 历史条数上限,超出后丢最旧的。面板里也要显示这个数,所以导出。
export const HIST_MAX = 100
// 单条长度上限:防止误把一大段粘贴内容存进去撑爆 localStorage(整个域通常只有 5MB)。
const CMD_MAX = 2000

export interface CommandFavorite {
  id: string
  cmd: string
  note: string
  createdAt: string
}

// crypto.randomUUID 只在安全上下文里有(用 http 访问局域网 IP 时就没有),要兜底。
function newId(): string {
  const c = globalThis.crypto as Crypto | undefined
  if (typeof c?.randomUUID === 'function') return c.randomUUID()
  return `c${Date.now().toString(36)}${Math.random().toString(36).slice(2, 8)}`
}

// 命令一律收敛成单行:换行送进终端就是直接执行下一条,而"填入"这个动作
// 绝不能顺带执行任何东西(见 TerminalView 的 fillCommand)。
function normalize(cmd: string): string {
  return cmd.replace(/[\r\n]+/g, ' ').trim().slice(0, CMD_MAX)
}

function readJson(key: string): unknown {
  try {
    const raw = localStorage.getItem(key)
    return raw ? JSON.parse(raw) : null
  } catch {
    // 存储被禁用,或旧版本写进去的格式已经变了 —— 一律当空,不能让它把整页带崩。
    return null
  }
}

function writeJson(key: string, value: unknown) {
  try {
    localStorage.setItem(key, JSON.stringify(value))
  } catch {
    /* 隐私模式 / 配额满:存不下就算了,内存里这份照常用 */
  }
}

// 收藏逐条校验:某一条形状不对就只丢它,而不是整份收藏都不要。
function parseFavorites(v: unknown): CommandFavorite[] {
  if (!Array.isArray(v)) return []
  const out: CommandFavorite[] = []
  for (const it of v) {
    const o = (it ?? {}) as Record<string, unknown>
    const cmd = typeof o.cmd === 'string' ? normalize(o.cmd) : ''
    if (!cmd) continue
    out.push({
      id: typeof o.id === 'string' && o.id ? o.id : newId(),
      cmd,
      note: typeof o.note === 'string' ? o.note : '',
      createdAt: typeof o.createdAt === 'string' ? o.createdAt : new Date().toISOString(),
    })
  }
  return out
}

function parseHistory(v: unknown): string[] {
  if (!Array.isArray(v)) return []
  const out: string[] = []
  for (const it of v) {
    if (typeof it !== 'string') continue
    const c = normalize(it)
    if (c && !out.includes(c)) out.push(c)
    if (out.length >= HIST_MAX) break
  }
  return out
}

export const useCommandStore = defineStore('commands', () => {
  const favorites = ref<CommandFavorite[]>(parseFavorites(readJson(FAV_KEY)))
  const history = ref<string[]>(parseHistory(readJson(HIST_KEY)))
  // 记录开关:关掉之后只留收藏,输入过的命令不再落盘(共用设备上有用)。
  // 关掉不会顺手清掉已有的历史 —— 清空是另一个按钮,免得点一下开关就丢一堆东西。
  const recording = ref(readJson(REC_KEY) !== false)

  const favCmds = computed(() => new Set(favorites.value.map((f) => f.cmd)))

  function isFavorite(cmd: string): boolean {
    return favCmds.value.has(normalize(cmd))
  }

  function saveFav() {
    writeJson(FAV_KEY, favorites.value)
  }
  function saveHist() {
    writeJson(HIST_KEY, history.value)
  }

  // 加一条收藏。空的或已经收过的返回 null,调用方据此提示。
  function addFavorite(cmd: string, note = ''): CommandFavorite | null {
    const c = normalize(cmd)
    if (!c || favCmds.value.has(c)) return null
    const f: CommandFavorite = { id: newId(), cmd: c, note: note.trim(), createdAt: new Date().toISOString() }
    // 新的排在最前:刚加的那条一眼就能看到。
    favorites.value = [f, ...favorites.value]
    saveFav()
    return f
  }

  // 改一条收藏。命令为空、条目不存在、或改成了另一条已有的命令都算失败。
  function updateFavorite(id: string, cmd: string, note: string): boolean {
    const c = normalize(cmd)
    if (!c) return false
    const f = favorites.value.find((x) => x.id === id)
    if (!f) return false
    if (c !== f.cmd && favCmds.value.has(c)) return false
    f.cmd = c
    f.note = note.trim()
    saveFav()
    return true
  }

  function removeFavorite(id: string) {
    favorites.value = favorites.value.filter((f) => f.id !== id)
    saveFav()
  }

  // 历史列表上的星号走这条。返回操作之后是不是已收藏。
  function toggleFavorite(cmd: string): boolean {
    const c = normalize(cmd)
    if (!c) return false
    if (favCmds.value.has(c)) {
      favorites.value = favorites.value.filter((f) => f.cmd !== c)
      saveFav()
      return false
    }
    addFavorite(c)
    return true
  }

  // 记一条输入历史。同一条命令只留最近那次(挪到最前),重复执行不会把列表刷满。
  function pushHistory(cmd: string) {
    if (!recording.value) return
    const c = normalize(cmd)
    if (!c) return
    const next = history.value.filter((h) => h !== c)
    next.unshift(c)
    if (next.length > HIST_MAX) next.length = HIST_MAX
    history.value = next
    saveHist()
  }

  function clearHistory() {
    history.value = []
    saveHist()
  }

  function setRecording(on: boolean) {
    recording.value = on
    writeJson(REC_KEY, on)
  }

  return {
    favorites, history, recording, isFavorite,
    addFavorite, updateFavorite, removeFavorite, toggleFavorite,
    pushHistory, clearHistory, setRecording,
  }
})
