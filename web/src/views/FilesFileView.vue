<script setup lang="ts">
// 文件浏览器二级页:单个文件的 只读语法高亮预览 / 编辑 视图。
// 一级(FilesView)点某文件 → 路由到本页,查询参数带 path(绝对路径)与 name(展示标题);
// 从搜索结果点进来时还带 line(命中行号)与 q/regex/case(一级用的关键词和开关),
// 进页面后把关键词的全部命中标出来,当前项落在命中行上 —— 不只是滚到那一行。
// 图片走 <img>、压缩包走条目列表,都不读正文;markdown 只读时可切换渲染视图。
import { ref, computed, watch, onMounted, nextTick } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  NButton, NIcon, NSpin, NEmpty, NInput, useMessage,
} from 'naive-ui'
import {
  ChevronBackOutline, SearchOutline, CreateOutline, SaveOutline,
  CloseOutline, ChevronUpOutline, ChevronDownOutline, SwapHorizontalOutline,
  ArrowUndoOutline, ArrowRedoOutline, EyeOutline, CodeOutline, DownloadOutline,
} from '@vicons/ionicons5'
import { api, type FsArchiveEntry } from '@/api/client'
import { highlightCode } from '@/utils/highlight'
import { renderMarkdown } from '@/utils/markdown'
import { fileIcon, isArchivePath, isImagePath, isMarkdownPath } from '@/utils/fileIcon'

const route = useRoute()
const router = useRouter()
const message = useMessage()

const path = (route.query.path as string) || ''
const name = ref((route.query.name as string) || basename(path))

function basename(p: string): string {
  const trimmed = p.replace(/\/+$/, '')
  return trimmed.split('/').pop() || p
}

const MAX_EDIT = 512 * 1024

// 按扩展名一次定型:图片/压缩包都是二进制,读正文只会拿到 400。
const kind = isImagePath(path) ? 'image' : isArchivePath(path) ? 'archive' : 'text'
const isMd = isMarkdownPath(path)
// 搜索结果带过来的命中行号(没有则 0)。
const targetLine = Number(route.query.line) || 0
// 一级搜索的关键词与开关。带了关键词就在本页复用文件内搜索那套命中标记,
// 让人落地先看见高亮的内容;点文件名那行进来(没有 line)则定位到第一处命中。
const initialQ = (route.query.q as string) || ''

// ---- 内容加载 ----
const content = ref('')
const editText = ref('')
const loadError = ref('')
const loading = ref(false)
const archiveEntries = ref<FsArchiveEntry[]>([])
const archiveTruncated = ref(false)

async function load() {
  if (!path) {
    loadError.value = '缺少文件路径参数'
    return
  }
  loading.value = true
  loadError.value = ''
  try {
    if (kind === 'archive') {
      const out = await api.fsArchiveList(path)
      archiveEntries.value = out.entries
      archiveTruncated.value = out.truncated
    } else if (kind === 'text') {
      content.value = await api.fsRead(path)
      editText.value = content.value
      resetHistory()
    }
  } catch (e: any) {
    loadError.value = `无法读取文件:${e?.message || e || '未知错误'}`
    content.value = ''
    editText.value = ''
  } finally {
    loading.value = false
  }
  // 命中高亮/行号定位都要等 DOM 落地,并且必须在源码视图下才有行号栏与命中标记。
  if (content.value && (initialQ || targetLine)) {
    await nextTick()
    if (initialQ) highlightFromSearch()
    else scrollToLine(targetLine)
  }
}

// ---- 压缩包内的目录浏览 ----
// 后端给的是整包平铺列表(name = 包内完整路径),这里在前端按前缀切成一层层目录。
// 目录必须从路径里推,不能只信 dir 标记:有些包(尤其 7z)根本不写目录条目,
// 文件夹只存在于文件路径中间。
const archiveCwd = ref('') // 包内当前目录,'' = 包根,非空时以 / 结尾

interface ArchiveRow { name: string; dir: boolean; size: number }

const archiveRows = computed<ArchiveRow[]>(() => {
  const prefix = archiveCwd.value
  const dirs = new Set<string>()
  const files: ArchiveRow[] = []
  for (const e of archiveEntries.value) {
    if (!e.name.startsWith(prefix)) continue
    const rest = e.name.slice(prefix.length).replace(/\/+$/, '')
    if (!rest) continue // 当前目录自己那条
    const slash = rest.indexOf('/')
    if (slash >= 0) dirs.add(rest.slice(0, slash))
    else if (e.dir) dirs.add(rest)
    else files.push({ name: rest, dir: false, size: e.size })
  }
  const cmp = (a: ArchiveRow, b: ArchiveRow) => a.name.localeCompare(b.name)
  // 目录优先,和文件列表页一致。
  return [...dirs].sort().map((name) => ({ name, dir: true, size: 0 })).concat(files.sort(cmp))
})

const archiveCrumbs = computed(() => {
  const out = [{ name: '/', prefix: '' }]
  let acc = ''
  for (const part of archiveCwd.value.split('/').filter(Boolean)) {
    acc += part + '/'
    out.push({ name: part, prefix: acc })
  }
  return out
})

function openArchiveRow(row: ArchiveRow) {
  if (row.dir) archiveCwd.value += row.name + '/'
}

// ---- markdown 渲染视图(F7,只读态可切) ----
// 从搜索结果进来时默认给源码(命中标记只存在于源码视图),否则 markdown 默认渲染态。
const mdRendered = ref(isMd && !targetLine && !initialQ)
const mdHtml = computed(() => (mdRendered.value ? renderMarkdown(content.value) : ''))

// ---- 编辑模式 ----
const editing = ref(false)

function enterEdit() {
  if (content.value.length > MAX_EDIT) {
    message.warning('文件过大,超过 512KB,无法编辑')
    return
  }
  editText.value = content.value
  resetHistory()
  mdRendered.value = false // 渲染态没法编辑,切回源码
  editing.value = true
}

async function save() {
  try {
    await api.fsWrite(path, editText.value)
    message.success('已保存')
    content.value = editText.value
    editing.value = false
  } catch (e: any) {
    message.error(`保存失败:${e?.message || e || '未知错误'}`)
  }
}

function cancelEdit() {
  editText.value = content.value
  resetHistory()
  editing.value = false
}

// ---- 撤销 / 恢复(F6) ----
// v-model 的程序化赋值会清掉 textarea 自带的 undo 栈(替换、撤销本身都会触发),
// 所以自己维护快照栈。连续输入 800ms 内合并成一帧,免得一个字一步。
interface Snapshot { text: string; start: number; end: number }
const COALESCE_MS = 800
const HISTORY_MAX = 200
const undoStack = ref<Snapshot[]>([])
const redoStack = ref<Snapshot[]>([])
let lastPushAt = 0
let composing = false

const editorEl = ref<HTMLElement | null>(null)
const gutterEl = ref<HTMLElement | null>(null)
const inputEl = ref<HTMLTextAreaElement | null>(null)

function resetHistory() {
  undoStack.value = []
  redoStack.value = []
  lastPushAt = 0
}

function snapshot(): Snapshot {
  const el = inputEl.value
  return { text: editText.value, start: el?.selectionStart ?? 0, end: el?.selectionEnd ?? 0 }
}

// force = 结构性改动(替换、开始输入法组合),必定单独占一帧。
function pushUndo(force = false) {
  if (composing) return
  const now = Date.now()
  if (!force && undoStack.value.length && now - lastPushAt < COALESCE_MS) return
  lastPushAt = now
  undoStack.value.push(snapshot())
  if (undoStack.value.length > HISTORY_MAX) undoStack.value.shift()
  redoStack.value = []
}

function applySnapshot(s: Snapshot) {
  editText.value = s.text
  lastPushAt = 0 // 撤销之后的下一次输入另起一帧
  nextTick(() => {
    const el = inputEl.value
    if (!el) return
    el.focus()
    el.setSelectionRange(s.start, s.end)
  })
}

function undo() {
  const prev = undoStack.value.pop()
  if (!prev) return
  redoStack.value.push(snapshot())
  applySnapshot(prev)
}

function redo() {
  const next = redoStack.value.pop()
  if (!next) return
  undoStack.value.push(snapshot())
  applySnapshot(next)
}

// beforeinput 时 editText 还是改动前的值,正好用来存快照。
function onBeforeInput() {
  if (editing.value) pushUndo()
}

// 输入法组合期间 beforeinput 会逐字触发,整段组合只留组合前的一帧。
function onCompositionStart() {
  if (editing.value) pushUndo(true)
  composing = true
}

function onCompositionEnd() {
  composing = false
  lastPushAt = Date.now()
}

// 桌面端快捷键:接管 Ctrl+Z / Ctrl+Shift+Z / Ctrl+Y,别让浏览器自己那套残缺的 undo 插手。
function onEditorKeydown(e: KeyboardEvent) {
  if (!editing.value || !(e.ctrlKey || e.metaKey)) return
  const k = e.key.toLowerCase()
  if (k === 'z' && !e.shiftKey) {
    e.preventDefault()
    undo()
  } else if (k === 'y' || (k === 'z' && e.shiftKey)) {
    e.preventDefault()
    redo()
  }
}

// 点最后一行下方的空白:textarea 的高度必须贴合内容(否则光标会和高亮层错开),
// 那片空白落在滚动容器自己身上,textarea 收不到事件 —— 补一手,把焦点交给它、
// 光标落到文末,和常见编辑器的行为一致。右侧空白由 CSS 让 .ce-body grow 吃掉,不用管。
function onEditorMouseDown(e: MouseEvent) {
  if (e.target !== editorEl.value) return // 只处理落在容器自身空白上的点击
  const el = inputEl.value
  if (!el) return
  e.preventDefault() // 默认行为会把焦点收到容器上,反而抢掉 textarea 的
  el.focus()
  // 容器上下各有 8px padding,点上沿那条窄缝时甩到文末很突兀:按落点在内容上方还是
  // 下方分别落到文首/文末。
  const box = preEl.value?.getBoundingClientRect()
  const pos = box && e.clientY < box.top ? 0 : editText.value.length
  el.setSelectionRange(pos, pos)
}

// ---- 搜索(预览定位 / 编辑态替换共用) ----
// 关键词/开关从一级搜索带过来时直接沿用,并把搜索行展开:既解释了满屏高亮的来由,
// 也能就地按上一个/下一个在同一文件的命中间走。
//
// 当前屏幕上的原始文本:编辑态是编辑缓冲,预览态是已加载的正文。搜索、行号、
// 高亮、替换全都按这一份算 —— 早先搜索扫的是 content,编辑态下改过内容再搜,
// 命中下标属于旧文本、mark 却画在新文本上,标记落点和 N/M 计数都是错的。
const displayText = computed(() => (editing.value ? editText.value : content.value))

const searchQ = ref(initialQ)
const searchRegex = ref(route.query.regex === '1')
const searchCase = ref(route.query.case === '1')
const searchOpen = ref(Boolean(initialQ)) // 顶部工具栏「搜索」按钮是否展开输入行
const replaceOpen = ref(false) // 顶部工具栏「替换」按钮是否展开「替换为」那一行
const hits = ref<{ start: number; end: number }[]>([])
const hitIndex = ref(-1) // -1 = 未定位(无当前命中)

// 查找行(关键词 + 正则/大小写开关 + 上一个/下一个)只有一份,替换直接复用它 ——
// 两个查找框各写一遍关键词、开关只挂在其中一行上,是纯粹的重复。所以只要搜索或替换
// 有一个开着,这行就得在;editing 兜一手:退出编辑态后 replaceOpen 不该再撑着它。
const findOpen = computed(() => searchOpen.value || (editing.value && replaceOpen.value))

// 搜索钮 = 查找行的开关。关掉时连替换一起收:替换离了查找框没法用。
function toggleSearch() {
  if (findOpen.value) closeFind()
  else searchOpen.value = true
}

// Esc 专用:必须幂等 —— 面板上的 keydown 和输入框里的 keyup 会各来一次,
// 用 toggleSearch 的话第二次又给开回来了。
function closeFind() {
  searchOpen.value = false
  replaceOpen.value = false
}

const preEl = ref<HTMLElement | null>(null)

// 依据开关构造搜索正则。非正则模式转义特殊字符;g 或 gi 视大小写。
function buildRegex(q: string): RegExp {
  const src = searchRegex.value ? q : q.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  return new RegExp(src, searchCase.value ? 'g' : 'gi')
}

// 记录上一次搜索的"指纹"(关键词+开关),用于判断重复点击时是否要"下一个"。
// 用 ref 是为了让模板能判断"这次搜索到底跑过没有":命中数/无命中只有在搜过
// 之后才有意义 —— 光看输入框里有没有字,刚打第一个字就会报"无命中"。
const lastSig = ref('')

function sigOf() {
  return searchQ.value + '|' + (searchRegex.value ? 1 : 0) + (searchCase.value ? 1 : 0)
}

// 输入框里的关键词/开关与上次真正搜过的那次一致 —— 不一致(还没搜、或搜完又改了)
// 就把计数藏起来,免得拿旧结果糊弄当前的关键词。
const searchDone = computed(() => Boolean(lastSig.value) && sigOf() === lastSig.value)

// 收集全文命中下标。正则模式下 q 可能是后端(Go RE2)编得过、JS 编不过的写法,
// 也可能是用户正打到一半的半截正则,一律按"无命中"处理,别让异常炸出去。
function collectHits(q: string): { start: number; end: number }[] {
  const found: { start: number; end: number }[] = []
  if (!q) return found
  let re: RegExp
  try {
    re = buildRegex(q)
  } catch {
    return found
  }
  let m: RegExpExecArray | null
  // 用 exec 循环收集所有命中下标(RegExp 带 g,lastIndex 自动推进)。
  while ((m = re.exec(displayText.value)) !== null) {
    found.push({ start: m.index, end: m.index + m[0].length })
    // 空命中会死循环:手动推进 lastIndex。
    if (m[0].length === 0) re.lastIndex++
  }
  return found
}

function doSearch() {
  if (!searchQ.value) {
    hits.value = []
    hitIndex.value = -1
    lastSig.value = ''
    return
  }
  // 命中标记只存在于源码视图里,渲染态搜索先切回源码。
  mdRendered.value = false
  const sig = sigOf()
  hits.value = collectHits(searchQ.value)
  // 关键:若这次与上次是同一查询(关键词/开关都没变),重复点击视为"跳到下一个命中"。
  if (sig === lastSig.value && hits.value.length) {
    hitIndex.value = (hitIndex.value + 1 + hits.value.length) % hits.value.length
    scrollToHit()
    return
  }
  lastSig.value = sig
  hitIndex.value = hits.value.length ? 0 : -1
  scrollToHit()
}

function gotoHit(next: number) {
  if (!hits.value.length) return
  hitIndex.value = (hitIndex.value + next + hits.value.length) % hits.value.length
  scrollToHit()
}

// 定位当前命中:滚到它,并把 .current 记在它身上(其余命中只留浅底色)。
// class 是直接改 DOM 而不是掺进 displayHtml 的:后者会让每次"上一个/下一个"
// 都把全文重新过一遍 highlightCode,大文件上按一下卡一下。
// displayHtml 一变,mark 节点整批重建,current 自然消失,再由这里重新标上。
// scroll=false:只把 current 补回去,不动视口 —— 编辑时每敲一个字 mark 都会重建,
// 这时候把视口拽到当前命中上会让人没法打字。
function scrollToHit(smooth = true, scroll = true) {
  requestAnimationFrame(() => {
    const marks = preEl.value?.querySelectorAll<HTMLElement>('mark.hit')
    if (!marks) return
    marks.forEach((m) => m.classList.remove('current'))
    if (hitIndex.value < 0 || hitIndex.value >= hits.value.length) return
    const el = marks[hitIndex.value]
    if (!el) return
    el.classList.add('current')
    if (scroll) el.scrollIntoView({ block: 'center', behavior: smooth ? 'smooth' : 'auto' })
  })
}

// 命中下标是"搜索那一刻"算出来的绝对偏移,编辑态下正文每敲一个字就全体失效:
// 不重收的话 mark 会整体错位、N/M 也不再对得上,而替换用的是当下的 editText。
// 只在搜过一次(lastSig 非空)之后才跟,免得没用搜索的人白白扫全文。
watch(displayText, () => {
  if (!lastSig.value || !searchQ.value) return
  hits.value = collectHits(searchQ.value)
  if (hitIndex.value >= hits.value.length) hitIndex.value = hits.value.length - 1
  scrollToHit(false, false)
})

// 关键词或开关一改,上次那次搜索的结果就作废:标记和计数一起撤掉,回到"还没搜"的状态。
// 不这么做的话,屏幕上留着旧关键词的 mark、计数却按新关键词藏了,两边说的不是一回事。
watch([searchQ, searchRegex, searchCase], () => {
  if (sigOf() === lastSig.value) return // 改回和上次一样的条件,原结果仍然有效
  lastSig.value = ''
  hitIndex.value = -1
  if (hits.value.length) hits.value = []
})

// 第 n 行(1 起)在正文里的字符区间,end 指向行末(不含换行)。
function lineBounds(n: number): { start: number; end: number } {
  const text = displayText.value
  let start = 0
  for (let i = 1; i < n; i++) {
    const nl = text.indexOf('\n', start)
    if (nl < 0) return { start: text.length, end: text.length }
    start = nl + 1
  }
  const nl = text.indexOf('\n', start)
  return { start, end: nl < 0 ? text.length : nl }
}

// 落地该把哪一处设成当前:先找命中行上的第一处,退而找该行之后的第一处,
// 再退到全文第一处 —— 文件可能在搜索之后被改过,行号未必还对得上。
function hitAtLine(n: number): number {
  const { start, end } = lineBounds(n)
  const onLine = hits.value.findIndex((h) => h.start >= start && h.start <= end)
  if (onLine >= 0) return onLine
  const after = hits.value.findIndex((h) => h.start >= start)
  return after >= 0 ? after : 0
}

// 从一级搜索结果进页:标出关键词的全部命中,当前项落在命中行那一处。
// 正文里一处都不命中(文件已改、或该正则在 JS 里不成立)就退回只滚到行号。
function highlightFromSearch() {
  hits.value = collectHits(searchQ.value)
  lastSig.value = sigOf() // 展开的搜索行里再回车一次 = 下一个,而不是从头再来
  if (!hits.value.length) {
    hitIndex.value = -1
    if (targetLine) scrollToLine(targetLine)
    return
  }
  hitIndex.value = targetLine ? hitAtLine(targetLine) : 0
  scrollToHit(false) // 刚进页面直接落位,不做跨半个文件的平滑滚动
}

const currentHit = computed(() =>
  hitIndex.value >= 0 && hitIndex.value < hits.value.length ? hitIndex.value + 1 : 0)

// 滚到指定行(F3:从搜索结果带 line 进来时定位)。
// 行号栏的 <span> 与代码行同处一条流内,拿它做锚点即可,不用猜行高;
// scrollIntoView 会把沿路每一层滚动容器都带到位,不必关心究竟哪层在滚。
function scrollToLine(n: number) {
  const el = gutterEl.value?.children[n - 1] as HTMLElement | undefined
  el?.scrollIntoView({ block: 'center' })
}

// ---- 渲染(预览/编辑共用):命中切片包进 <mark class="hit">,其余片段走 highlightCode 高亮。
// 单独抽出,让 content(预览)与 editText(编辑)都经过同一套命中高亮逻辑。
function renderHighlight(text: string): string {
  if (!text) return ''
  if (!hits.value.length) return highlightCode(text, path)
  let out = ''
  let pos = 0
  for (const h of hits.value) {
    if (h.start > pos) out += highlightCode(text.slice(pos, h.start), path)
    // 命中段本身也过 highlightCode,保留语法色,外层再包 mark。
    out += `<mark class="hit">${highlightCode(text.slice(h.start, h.end), path)}</mark>`
    pos = h.end
  }
  if (pos < text.length) out += highlightCode(text.slice(pos), path)
  return out
}

// 行号 1..N,基于当前展示文本的换行数,编辑态随换行实时更新。
const gutterLines = computed<number[]>(() => {
  const n = displayText.value.split('\n').length
  return Array.from({ length: n }, (_, i) => i + 1)
})

// 覆盖层要显示的语法高亮 HTML。末尾必须补一个 \n:pre 里最后那个换行不生成行框
// (`<pre>a\n</pre>` 只有一行高),而 textarea 的值 "a\n" 是实打实的两行。文件基本都以
// 换行结尾,于是高亮层比 textarea 矮一行,而 .ce-body 的高度就是高亮层的高度 ——
// height: 100% 的 textarea 也跟着矮一行:最后那行空白行既点不到,光标一旦落到文末
// 还会让 textarea 自己内部滚一行,从此整层文字和高亮错开,点倒数第二行反而落到末行行首。
// 补一个换行后,pre 的行框数恒等于 split('\n').length,与行号列、textarea 三边对齐。
const displayHtml = computed<string>(() => renderHighlight(displayText.value) + '\n')

// ---- 替换(仅编辑模式) ----
// 查找词与正则/大小写开关都取上面那条查找行,这里只管「替换为」。
const replaceText = ref('')

function replaceAll() {
  if (!searchQ.value) {
    message.warning('请先输入查找内容')
    return
  }
  let re: RegExp
  try {
    re = buildRegex(searchQ.value)
  } catch {
    // 开着正则时用户可能打了半截表达式,别让异常炸出去。
    message.error('正则表达式无效')
    return
  }
  const count = (editText.value.match(re) || []).length
  if (!count) {
    message.warning('没有匹配内容')
    return
  }
  // String.replace 的替换串里 $ 是特殊记号($1 引用捕获组、$& 整个匹配、$$ 一个字面 $)。
  // 正则模式下这是能力,留着;非正则模式下查找侧已经转义成字面量了,替换侧也得对称,
  // 否则「把 a 换成 $&」会换出 a 自己。
  const rep = searchRegex.value ? replaceText.value : replaceText.value.replace(/\$/g, '$$$$')
  pushUndo(true) // 批量替换单独占一帧,一次撤销能整体还原
  editText.value = editText.value.replace(re, rep)
  message.success(`已替换 ${count} 处`)
}

function sizeHuman(n: number) {
  if (n < 1024) return n + ' B'
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB'
  return (n / 1024 / 1024).toFixed(1) + ' MB'
}

function goBack() {
  router.back()
}

onMounted(load)
</script>

<template>
  <div class="file-view page-content">
    <!-- 顶部工具栏:固定在最上,滚动代码时始终可见。图标钮→点一下在下方展开对应输入行。 -->
    <div class="tom-bar">
      <div class="tom-row">
        <n-button quaternary size="small" title="返回列表" aria-label="返回列表" @click="goBack">
          <template #icon><n-icon :component="ChevronBackOutline" /></template>
        </n-button>
        <div class="fv-title">
          <div class="fv-path" :title="path">{{ name }}</div>
        </div>
        <div class="tom-tools">
          <n-button v-if="kind === 'text'" quaternary size="small" :type="findOpen ? 'primary' : 'default'"
            title="搜索" aria-label="搜索" @click="toggleSearch">
            <template #icon><n-icon :component="SearchOutline" /></template>
          </n-button>
          <n-button v-if="editing" quaternary size="small" :type="replaceOpen ? 'primary' : 'default'"
            title="替换" aria-label="替换" @click="replaceOpen = !replaceOpen">
            <template #icon><n-icon :component="SwapHorizontalOutline" /></template>
          </n-button>
          <template v-if="editing">
            <n-button quaternary size="small" :disabled="!undoStack.length"
              title="撤销" aria-label="撤销" @click="undo">
              <template #icon><n-icon :component="ArrowUndoOutline" /></template>
            </n-button>
            <n-button quaternary size="small" :disabled="!redoStack.length"
              title="恢复" aria-label="恢复" @click="redo">
              <template #icon><n-icon :component="ArrowRedoOutline" /></template>
            </n-button>
          </template>
          <!-- markdown 只读态:渲染视图 ↔ 源码视图 -->
          <n-button v-if="isMd && !editing" quaternary size="small" :type="mdRendered ? 'primary' : 'default'"
            :title="mdRendered ? '看源码' : '看渲染'" :aria-label="mdRendered ? '看源码' : '看渲染'"
            @click="mdRendered = !mdRendered">
            <template #icon><n-icon :component="mdRendered ? CodeOutline : EyeOutline" /></template>
          </n-button>
          <n-button v-if="kind !== 'text'" quaternary size="small" tag="a" :href="api.fsDownloadUrl(path)"
            title="下载" aria-label="下载">
            <template #icon><n-icon :component="DownloadOutline" /></template>
          </n-button>
          <template v-if="kind === 'text' && !editing">
            <n-button quaternary size="small" type="primary" title="编辑" aria-label="编辑" @click="enterEdit">
              <template #icon><n-icon :component="CreateOutline" /></template>
            </n-button>
          </template>
          <template v-else-if="editing">
            <n-button quaternary size="small" type="primary" title="保存" aria-label="保存" @click="save">
              <template #icon><n-icon :component="SaveOutline" /></template>
            </n-button>
            <n-button quaternary size="small" title="取消" aria-label="取消" @click="cancelEdit">
              <template #icon><n-icon :component="CloseOutline" /></template>
            </n-button>
          </template>
        </div>
      </div>

      <!-- 查找行:搜索钮或替换钮展开。替换共用这里的关键词与正则/大小写开关。 -->
      <div v-if="findOpen" class="tom-panel" @keydown.esc="closeFind">
        <n-input v-model:value="searchQ" size="small" :placeholder="replaceOpen ? '查找' : '搜索当前文件'"
          class="tom-search" clearable @keyup.enter="doSearch" @keyup.esc="closeFind" />
        <n-button size="small" :type="searchRegex ? 'primary' : 'default'" quaternary
          title="正则" aria-label="正则" @click="searchRegex = !searchRegex">.*</n-button>
        <n-button size="small" :type="searchCase ? 'primary' : 'default'" quaternary
          title="区分大小写" aria-label="区分大小写" @click="searchCase = !searchCase">Aa</n-button>
        <n-button size="small" type="primary" title="搜索" aria-label="搜索" @click="doSearch">
          <template #icon><n-icon :component="SearchOutline" /></template>
        </n-button>
        <span class="tom-sep"></span>
        <span v-if="searchDone && hits.length" class="ft-counter">{{ currentHit }}/{{ hits.length }}</span>
        <span v-else-if="searchDone" class="ft-nohit">无命中</span>
        <n-button size="small" quaternary :disabled="!hits.length" title="上一个" aria-label="上一个" @click="gotoHit(-1)">
          <template #icon><n-icon :component="ChevronUpOutline" /></template>
        </n-button>
        <n-button size="small" quaternary :disabled="!hits.length" title="下一个" aria-label="下一个" @click="gotoHit(1)">
          <template #icon><n-icon :component="ChevronDownOutline" /></template>
        </n-button>
      </div>

      <!-- 替换行:只放「替换为」,查找词取上面那一行(仅编辑态) -->
      <div v-if="editing && replaceOpen" class="tom-panel" @keydown.esc="replaceOpen = false">
        <n-input v-model:value="replaceText" size="small" placeholder="替换为" class="tom-search"
          @keyup.enter="replaceAll" @keyup.esc="replaceOpen = false" />
        <n-button size="small" type="error" :disabled="!searchQ" title="全部替换" aria-label="全部替换" @click="replaceAll">
          <template #icon><n-icon :component="SwapHorizontalOutline" /></template>
        </n-button>
      </div>
    </div>

    <!-- 主体 -->
    <n-spin :show="loading" class="fv-body">
      <div v-if="loadError" class="fv-error">{{ loadError }}</div>

      <!-- 图片(F2a):走下载端点的 inline 模式,会话 Cookie 由浏览器自动带上。 -->
      <div v-else-if="kind === 'image'" class="fv-image">
        <img :src="api.fsInlineUrl(path)" :alt="name" />
      </div>

      <!-- 压缩包(F2b):只列条目,不解压不读正文。目录可点进,层级在前端按路径前缀切。 -->
      <div v-else-if="kind === 'archive'" class="fv-archive">
        <div class="fa-crumb">
          <template v-for="(c, i) in archiveCrumbs" :key="c.prefix">
            <span v-if="i > 1" class="fa-sep">/</span>
            <button v-if="i < archiveCrumbs.length - 1" class="fa-seg" @click="archiveCwd = c.prefix">
              {{ c.name }}
            </button>
            <span v-else class="fa-seg current">{{ c.name }}</span>
          </template>
        </div>
        <div class="fa-head">
          {{ archiveRows.length }} 项
          <span v-if="archiveTruncated" class="fa-trunc">· 条目过多,整包仅读取前 {{ archiveEntries.length }} 条,可能不完整</span>
        </div>
        <n-empty v-if="!archiveRows.length" :description="archiveCwd ? '空目录' : '压缩包为空'" style="padding: 24px" />
        <div v-for="e in archiveRows" :key="e.name" class="fa-item" :class="{ 'fa-dir': e.dir }"
          :role="e.dir ? 'button' : undefined" :tabindex="e.dir ? 0 : undefined"
          @click="openArchiveRow(e)" @keydown.enter="openArchiveRow(e)">
          <n-icon class="fa-ico" :component="fileIcon(e.name, e.dir).icon" :color="fileIcon(e.name, e.dir).color" />
          <span class="fa-name">{{ e.name }}</span>
          <span v-if="!e.dir" class="fa-size">{{ sizeHuman(e.size) }}</span>
        </div>
      </div>

      <n-empty v-else-if="!editing && !content" description="文件内容为空" style="padding: 24px" />

      <!-- markdown 渲染视图(F7):只读态可切,渲染 HTML 由 markdown-it 生成(html: false)。 -->
      <div v-else-if="mdRendered" class="md-body" v-html="mdHtml"></div>

      <!-- 预览/编辑 共用编辑器:高亮 <pre> 打底 + 透明 <textarea> 覆盖,行号在左侧粘性栏。 -->
      <div v-else ref="editorEl" class="code-editor" @mousedown="onEditorMouseDown">
        <div ref="gutterEl" class="ce-gutter">
          <span v-for="n in gutterLines" :key="n">{{ n }}</span>
        </div>
        <div ref="preEl" class="ce-body">
          <pre><code v-html="displayHtml"></code></pre>
          <textarea ref="inputEl" class="ce-input" :readonly="!editing" v-model="editText"
            spellcheck="false" aria-label="文件内容"
            @beforeinput="onBeforeInput" @compositionstart="onCompositionStart"
            @compositionend="onCompositionEnd" @keydown="onEditorKeydown"></textarea>
        </div>
      </div>
    </n-spin>
  </div>
</template>

<style scoped>
.file-view { display: flex; flex-direction: column; height: 100%; }
/* 顶部工具栏:sticky 固定在最上,代码滚动时始终可见。 */
.tom-bar {
  position: sticky;
  top: 0;
  z-index: 20;
  background: var(--lr-bg);
  box-shadow: 0 1px 0 rgba(127, 127, 127, 0.12);
  border-radius: var(--lr-radius);
  flex: none;
}
.tom-row {
  display: flex; align-items: center; gap: 6px;
  padding: 4px 0;
}
.fv-title {
  flex: 1; min-width: 0;
  display: flex; align-items: center; gap: 6px;
}
.fv-path {
  font-family: ui-monospace, monospace; font-size: 13px; font-weight: 600;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.tom-tools { flex: none; display: flex; align-items: center; gap: 2px; }
/* 搜索/替换 展开的输入行 */
.tom-panel {
  display: flex; align-items: center; gap: 4px;
  padding: 6px 0 8px;
  flex-wrap: nowrap;
}
.tom-search { width: 180px; }
.tom-sep { flex: 1; }
.ft-counter {
  flex: none; font-size: 12px; color: var(--lr-fg-muted);
  font-family: ui-monospace, monospace; white-space: nowrap;
}
.ft-nohit {
  flex: none; font-size: 12px; color: var(--lr-danger, #d03050);
  font-family: ui-monospace, monospace; white-space: nowrap;
}
.fv-body { flex: 1; min-height: 0; }
.fv-error {
  margin: 8px 0; padding: 8px 12px; border-radius: 4px;
  color: var(--lr-danger, #d03050); background: rgba(208, 48, 80, 0.1);
  font-size: 13px;
}
/* 图片预览:按容器宽度自适应,超高时容器滚动;棋盘底衬托透明像素。 */
.fv-image {
  overflow: auto;
  padding: 8px 0;
  text-align: center;
}
.fv-image img {
  max-width: 100%;
  height: auto;
  border-radius: 4px;
  background-image:
    linear-gradient(45deg, rgba(127, 127, 127, 0.12) 25%, transparent 25%, transparent 75%, rgba(127, 127, 127, 0.12) 75%),
    linear-gradient(45deg, rgba(127, 127, 127, 0.12) 25%, transparent 25%, transparent 75%, rgba(127, 127, 127, 0.12) 75%);
  background-size: 16px 16px;
  background-position: 0 0, 8px 8px;
}
/* 压缩包条目列表 */
.fv-archive { overflow: auto; padding: 4px 0 8px; }
/* 包内面包屑:可点的段是 <button>、当前段是 <span>,统一 flex 居中免得一上一下。 */
.fa-crumb {
  display: flex; align-items: center; gap: 2px;
  overflow-x: auto; white-space: nowrap;
  padding: 2px 0;
}
.fa-seg {
  flex: none; display: flex; align-items: center;
  min-height: 30px; padding: 0 6px;
  border: 0; background: none; border-radius: 6px;
  font: inherit; font-size: 13px; color: var(--lr-accent); cursor: pointer;
}
.fa-seg:active { background: rgba(127, 127, 127, 0.16); }
.fa-seg.current { color: var(--lr-fg-muted); cursor: default; }
.fa-seg.current:active { background: none; }
.fa-sep { flex: none; color: var(--lr-fg-muted); }
.fa-head {
  padding: 6px 2px; font-size: 12px; color: var(--lr-fg-muted);
}
.fa-trunc { color: var(--lr-danger, #d03050); }
.fa-item {
  display: flex; align-items: center; gap: 8px;
  padding: 6px 2px;
  border-bottom: 1px solid rgba(127, 127, 127, 0.12);
  font-size: 13px;
}
.fa-item.fa-dir { cursor: pointer; }
.fa-item.fa-dir:active { background: rgba(127, 127, 127, 0.12); }
.fa-ico { flex: none; font-size: 14px; }
.fa-name {
  flex: 1; min-width: 0;
  font-family: ui-monospace, monospace;
  overflow-wrap: anywhere;
}
.fa-size {
  flex: none; font-size: 12px; color: var(--lr-fg-muted);
  font-family: ui-monospace, monospace; white-space: nowrap;
}
/* 代码编辑器:单个滚动容器承载 行号栏 + 高亮 pre + 透明 textarea。 */
.code-editor {
  position: relative;
  overflow: auto; /* 纵向横向都在这滚动,行号栏内部 sticky 跟着走 */
  flex: 1;
  min-height: 0;
  display: flex;
  /* 必须 flex-start,不能用默认的 stretch:单行 flex 容器高度是确定的,stretch 会把
     .ce-body 的高度钉成容器高度(cross 轴的 min-height: auto 解析成 0,不按内容兜底),
     而 textarea 是 height: 100% —— 长文件下半截就没有 textarea 可点了。 */
  align-items: flex-start;
  padding: 8px 0;
  /* 共享字级:pre/code/textarea 全部继承,保证覆盖层像素对齐 */
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
  line-height: 1.5;
  letter-spacing: normal;
  white-space: pre;
  overflow-wrap: normal;
  word-break: keep-all;
  tab-size: 4;
}
/* 行号栏:sticky 横向贴左,纵向随容器一起滚,天然与代码行对齐 */
.ce-gutter {
  position: sticky;
  left: 0;
  z-index: 2;
  flex: none;
  min-width: 14px;
  padding: 0 4px 0 0;
  text-align: right;
  user-select: none;
  color: var(--lr-fg-muted);
  background: var(--lr-bg);
}
.ce-gutter span { display: block; }
/* 高亮层:宽度基准贴合内容(max-content),textarea 才能精确覆盖到行尾。
   pre/code 的 UA 样式自带 font-family: monospace,会盖掉从 .code-editor 继承来的字体,
   于是高亮层和 textarea 用两种字体、字宽不同 —— 光标就会和文字逐渐错开。font: inherit 打掉它。
   flex: 1 0 auto —— max-content 只是"起点":全文都是短行时,余下的宽度得由它 grow 吃掉,
   否则右边那一大片空白落在 .code-editor 上,textarea 收不到 mousedown,点了不聚焦光标;
   shrink 保持 0,行比容器长时照旧溢出、由 .code-editor 横向滚动。 */
.ce-body { position: relative; flex: 1 0 auto; width: max-content; }
.ce-body pre { margin: 0; white-space: pre; font: inherit; }
.ce-body pre code { display: block; padding: 0; font: inherit; color: var(--lr-fg); }
/* 覆盖层 textarea:绝对铺满 .ce-body(= pre 同尺寸),透明文字只留光标。
   .ce-body 随内容变宽变高,textarea 一并长,纵向滚动时逐行仍与高亮对齐。 */
.ce-input {
  position: absolute;
  top: 0; left: 0;
  width: 100%; height: 100%;
  margin: 0; padding: 0; border: 0; outline: none; resize: none;
  background: transparent;
  color: transparent;
  -webkit-text-fill-color: transparent;
  caret-color: var(--lr-fg);
  font-family: inherit; font-size: inherit; line-height: inherit;
  letter-spacing: inherit; tab-size: inherit;
  white-space: pre; overflow-wrap: normal; word-break: keep-all;
  overflow: hidden;
}
.ce-input::selection { background: rgba(64, 120, 242, 0.35); }
/* 只读(预览)态:仍可点选/滚动,但光标不闪烁可输入 */
.ce-input[readonly] { user-select: none; }
</style>

<!-- 语法高亮令牌色 + 命中标记:非 scoped。v-html 注入的 <span class="hljs-*"> 与
     <mark class="hit"> 不带组件 scope 属性,scoped 选择器永远匹配不上,须用全局样式。
     .file-body 前缀限定作用域,映射到 --lr-* 令牌,浅/深主题自动适配。 -->
<style>
.ce-body :where(.hljs) { color: var(--lr-fg); }
.ce-body .hljs-attr, .ce-body .hljs-selector-tag, .ce-body .hljs-name { color: #c26; }
.ce-body .hljs-comment, .ce-body .hljs-quote { color: var(--lr-fg-muted); font-style: italic; }
.ce-body .hljs-keyword, .ce-body .hljs-selector-class, .ce-body .hljs-meta { color: #a626a4; }
.ce-body .hljs-string, .ce-body .hljs-regexp, .ce-body .hljs-addition { color: #3d8c3e; }
.ce-body .hljs-number, .ce-body .hljs-literal, .ce-body .hljs-deletion { color: #b76b01; }
.ce-body .hljs-title, .ce-body .hljs-title.function_, .ce-body .hljs-section,
.ce-body .hljs-built_in { color: #286983; }
.ce-body .hljs-type, .ce-body .hljs-class, .ce-body .hljs-selector-id { color: #4078f2; }
.ce-body .hljs-variable, .ce-body .hljs-params, .ce-body .hljs-property { color: var(--lr-fg); }
.ce-body .hljs-bullet, .ce-body .hljs-emphasis, .ce-body .hljs-strong {
  color: var(--lr-fg); font-weight: 600;
}
/* 搜索命中标记:命中仍保留语法色(mark 内嵌 hljs span),仅加底色高亮。 */
.ce-body mark.hit {
  background: rgba(255, 213, 0, 0.45);
  color: var(--lr-fg);
  border-radius: 2px;
  padding: 0;
}
/* 当前命中(上一个/下一个走到的那处、从搜索结果跳进来落在的那处):
   底色更重并描边,一屏几十处高亮时也能立刻认出停在哪。 */
.ce-body mark.hit.current {
  background: rgba(255, 145, 0, 0.6);
  box-shadow: 0 0 0 1px rgba(217, 108, 0, 0.9);
}
/* markdown 渲染视图(F7):v-html 注入的节点同样不带 scope 属性,须用全局样式。
   配色一律走 --lr-* 令牌或 rgba,浅/深主题自动适配,不必再写一份深色覆盖。 */
.md-body {
  overflow: auto;
  padding: 8px 2px 24px;
  font-size: 14px;
  line-height: 1.7;
  color: var(--lr-fg);
  overflow-wrap: anywhere;
}
.md-body > :first-child { margin-top: 0; }
.md-body h1, .md-body h2, .md-body h3,
.md-body h4, .md-body h5, .md-body h6 {
  margin: 1.4em 0 0.6em; line-height: 1.3; font-weight: 600;
}
.md-body h1 { font-size: 1.6em; }
.md-body h2 { font-size: 1.35em; }
.md-body h3 { font-size: 1.15em; }
.md-body h4, .md-body h5, .md-body h6 { font-size: 1em; }
.md-body h1, .md-body h2 {
  padding-bottom: 0.3em;
  border-bottom: 1px solid rgba(127, 127, 127, 0.24);
}
.md-body p, .md-body ul, .md-body ol, .md-body blockquote, .md-body table {
  margin: 0.7em 0;
}
.md-body ul, .md-body ol { padding-left: 1.5em; }
.md-body li { margin: 0.25em 0; }
.md-body a { color: var(--lr-accent, #4078f2); }
.md-body blockquote {
  padding: 0 0 0 12px;
  border-left: 3px solid rgba(127, 127, 127, 0.35);
  color: var(--lr-fg-muted);
}
.md-body hr {
  margin: 1.4em 0; border: 0;
  border-top: 1px solid rgba(127, 127, 127, 0.24);
}
.md-body img { max-width: 100%; height: auto; }
/* 行内 code 与围栏 pre.md-pre(见 utils/markdown.ts 的 highlight 回调) */
.md-body code {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 0.88em;
  padding: 0.15em 0.35em;
  border-radius: 3px;
  background: rgba(127, 127, 127, 0.16);
}
.md-body pre.md-pre {
  margin: 0.8em 0; padding: 10px 12px;
  border-radius: 4px;
  background: rgba(127, 127, 127, 0.12);
  overflow: auto;
  line-height: 1.5;
}
.md-body pre.md-pre code {
  padding: 0; background: none; font-size: 12px;
  white-space: pre; overflow-wrap: normal;
}
/* 表格:窄屏靠外层 .md-body 横向滚动 */
.md-body table { border-collapse: collapse; }
.md-body th, .md-body td {
  padding: 5px 10px;
  border: 1px solid rgba(127, 127, 127, 0.28);
}
.md-body th { background: rgba(127, 127, 127, 0.12); font-weight: 600; }
/* 围栏代码块的令牌色:复用 .ce-body 那套定义,这里只做选择器映射。 */
.md-body .hljs-attr, .md-body .hljs-selector-tag, .md-body .hljs-name { color: #c26; }
.md-body .hljs-comment, .md-body .hljs-quote { color: var(--lr-fg-muted); font-style: italic; }
.md-body .hljs-keyword, .md-body .hljs-selector-class, .md-body .hljs-meta { color: #a626a4; }
.md-body .hljs-string, .md-body .hljs-regexp, .md-body .hljs-addition { color: #3d8c3e; }
.md-body .hljs-number, .md-body .hljs-literal, .md-body .hljs-deletion { color: #b76b01; }
.md-body .hljs-title, .md-body .hljs-title.function_, .md-body .hljs-section,
.md-body .hljs-built_in { color: #286983; }
.md-body .hljs-type, .md-body .hljs-class, .md-body .hljs-selector-id { color: #4078f2; }
@media (prefers-color-scheme: dark) {
  .ce-body .hljs-attr, .ce-body .hljs-selector-tag, .ce-body .hljs-name { color: #f7797b; }
  .ce-body .hljs-keyword, .ce-body .hljs-selector-class, .ce-body .hljs-meta { color: #d88cf6; }
  .ce-body .hljs-string, .ce-body .hljs-regexp, .ce-body .hljs-addition { color: #8fcc7a; }
  .ce-body .hljs-number, .ce-body .hljs-literal, .ce-body .hljs-deletion { color: #ffc37a; }
  .ce-body .hljs-title, .ce-body .hljs-title.function_, .ce-body .hljs-section,
  .ce-body .hljs-built_in { color: #7bd5ff; }
  .ce-body .hljs-type, .ce-body .hljs-class, .ce-body .hljs-selector-id { color: #9bb8ff; }
  .ce-body mark.hit { background: rgba(255, 213, 0, 0.35); }
  .ce-body mark.hit.current {
    background: rgba(255, 145, 0, 0.5);
    box-shadow: 0 0 0 1px rgba(255, 176, 66, 0.9);
  }
  .md-body .hljs-attr, .md-body .hljs-selector-tag, .md-body .hljs-name { color: #f7797b; }
  .md-body .hljs-keyword, .md-body .hljs-selector-class, .md-body .hljs-meta { color: #d88cf6; }
  .md-body .hljs-string, .md-body .hljs-regexp, .md-body .hljs-addition { color: #8fcc7a; }
  .md-body .hljs-number, .md-body .hljs-literal, .md-body .hljs-deletion { color: #ffc37a; }
  .md-body .hljs-title, .md-body .hljs-title.function_, .md-body .hljs-section,
  .md-body .hljs-built_in { color: #7bd5ff; }
  .md-body .hljs-type, .md-body .hljs-class, .md-body .hljs-selector-id { color: #9bb8ff; }
}
</style>
