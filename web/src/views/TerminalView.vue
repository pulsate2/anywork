<script setup lang="ts">
// 终端视图:多窗口 PTY + 服务端滚动缓冲 + 移动键盘层。
import { ref, onMounted, onUnmounted, nextTick, watch } from 'vue'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import {
  NButton, NIcon, NModal, NList, NListItem,
  NTag, NEmpty, useMessage, NSelect, NForm, NFormItem, NInput,
} from 'naive-ui'
import { TerminalOutline, AddOutline, PlayOutline, CopyOutline, ClipboardOutline, StarOutline } from '@vicons/ionicons5'
import { TermClient, type TermSummary } from '@/api/term'
import { type Workspace } from '@/api/client'
import { useWorkspaceStore } from '@/stores/workspace'
import { useCommandStore } from '@/stores/commands'
import { copyText, selectAllIn } from '@/utils/clipboard'
import { useKeyboardInset } from '@/utils/keyboardInset'
import TerminalKeyboard from '@/mobile/TerminalKeyboard.vue'
import CommandFavorites from '@/components/CommandFavorites.vue'

const message = useMessage()
const store = useWorkspaceStore()
const cmds = useCommandStore()
// 软键盘遮挡高度。Chrome 默认不会因为键盘压缩 layout viewport,100dvh 的页面因此
// 完全感知不到键盘,底部的键盘条被压在键盘下面。拿它当额外的底部内边距用:
// 页面自己变矮 → 键盘条回到键盘上沿,终端跟着 fit。Via 那类会整页 resize 的浏览器
// 算出来是 0,这条路自动失效,不会重复补偿(见 utils/keyboardInset.ts)。
const kbInset = useKeyboardInset()
const termEl = ref<HTMLDivElement>()
const terminalWrap = ref<HTMLDivElement>()
const kbd = ref<InstanceType<typeof TerminalKeyboard> | null>(null)
let term: Terminal | null = null
let fitAddon: FitAddon | null = null
const client = new TermClient(handleEvent)

const sessions = ref<TermSummary[]>([])
const activeId = ref<string | null>(null)
const showSessionList = ref(false)
const showNewModal = ref(false)
const showCmdModal = ref(false)
const workspaces = ref<Workspace[]>([])
const selectedWs = ref<string | null>(null)
const execShell = ref('')

// 复制弹窗:手机上没法在 xterm 画布里拖选,只能把缓冲区文本倒进一个普通
// DOM 元素,交给系统的长按选取。
const showCopyModal = ref(false)
const dumpText = ref('')
const dumpSelection = ref('')
const dumpEl = ref<HTMLPreElement>()

// 终端尝试 resize 用的信号。
const fitSignal = ref(0)

// 「稳定化」调度。软键盘的开合是一段动画,ResizeObserver 会连着响很多次;
// xterm 自己也把一部分工作排到了 rAF / requestIdleCallback 上(Viewport.queueSync、
// RenderService 在被 IntersectionObserver 判为不可见时会把 renderer.handleResize
// 推到 idle 回调)。所以「fit 完立刻贴底重绘」是不够的 —— 我们同步做完之后,
// xterm 排在后面的那些回调还会按旧尺寸把视口拽走。这里改成延后再做一遍:
// 动画停下来后(trailing 120ms)一次,再补一次(420ms)兜住 idle 回调那一路。
let settleTimer: number | undefined
let settleTimer2: number | undefined
// 附加会话后,回放输出还在异步解析中,要等解析完再贴底。
let pendingAttachSettle = false
// 已同步给 PTY 的尺寸,避免 settle 反复发同样的 resize 引起 SIGWINCH 风暴。
let sentCols = 0
let sentRows = 0

// 进入页面后自动附加到最新会话。只认首次列表响应:之后的 list()(新建/结束/退出触发)
// 不能再自动跳转,否则用户结束当前会话就会被塞进另一个会话。
let autoAttachDone = false

function handleEvent(e: any) {
  switch (e.type) {
    case 'output':
      if (!term) break
      // write 是异步解析的。attach 的回放会先于 session 帧到达,若在这里之前就贴底,
      // 贴的是还没写进缓冲区的旧状态。用 write 的解析回调,等这批真的进了缓冲区再稳定化。
      if (pendingAttachSettle) term.write(e.data, () => scheduleSettle())
      else term.write(e.data)
      break
    case 'session':
      // create/attach 都会回这一帧:切掉空状态遮罩,并把真实尺寸同步给 PTY。
      // 回放帧到这里已经全部发出(服务端先推回放再发本帧),后续输出不用再排稳定化。
      pendingAttachSettle = false
      activeId.value = e.session.id
      nextTick(() => {
        attemptFit()
        syncPtySize(true)
        term?.focus()
      })
      client.list()
      break
    case 'sessionList':
      sessions.value = e.list
      if (!autoAttachDone && !activeId.value) {
        autoAttachDone = true
        // 服务端已按创建时间倒序,第一个活着的就是最新会话。
        const latest = (e.list as TermSummary[]).find((s) => !s.dead)
        if (latest) attachSession(latest)
      }
      break
    case 'exit':
      message.info(`会话已退出 (退出码 ${e.exitCode})`)
      // 服务端紧跟着会广播新的 sessionList,这里不用再 list()。
      if (e.id === activeId.value) releaseActive()
      break
    case 'error':
      message.error(e.message)
      break
    case 'close':
      break
  }
}

function ensureTerm() {
  if (term) return
  term = new Terminal({
    cursorBlink: true,
    fontSize: 14,
    fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
    scrollback: 2000,
    convertEol: true,
    allowProposedApi: true,
    theme: {
      background: 'transparent',
    },
  })
  fitAddon = new FitAddon()
  term.loadAddon(fitAddon)
  term.open(termEl.value!)
  // 软键盘/物理键盘的输入先过一遍键盘条的粘滞修饰符(Ctrl/Alt),再发给 PTY。
  term.onData((d) => sendKeys(kbd.value?.applySticky(d) ?? d))
  // 每来一次换行就把量到的输入起点作废。输出一行行流进来的时候光标一直在动,
  // 上一次量的列号对新的一行毫无意义;作废掉,下次按键会在新提示符后重新量。
  term.onLineFeed(() => resetTypedMark())
  // 物理键盘 resize 也在 resize observer 里处理。
  term.onResize(() => syncPtySize())
  applyTheme()
}

function applyTheme() {
  // 跟随当前实际主题(浅/深)。NAI 主题由 CSS 类控制,这里用媒体查询判断。
  const dark = window.matchMedia('(prefers-color-scheme: dark)').matches
  term?.options.theme && Object.assign(term.options.theme, {
    foreground: dark ? '#e5e7eb' : '#1c1f24',
    background: 'transparent',
    cursor: dark ? '#e5e7eb' : '#1c1f24',
    selectionBackground: 'rgba(37,99,235,.3)',
  })
  term?.refresh?.(0, term.rows * 1000)
}

let resizeObserver: ResizeObserver | null = null
function setUpResize() {
  if (!terminalWrap.value) return
  resizeObserver = new ResizeObserver(() => {
    attemptFit()
  })
  resizeObserver.observe(terminalWrap.value)
  // 键盘不一定会让布局视口变矮:有的 WebView 是平移页面而不是压缩它,那样 ResizeObserver
  // 根本不响,上面这条路就完全不会走。visualViewport 在两种行为下都会变,补一条。
  window.visualViewport?.addEventListener('resize', onVisualViewport)
  window.visualViewport?.addEventListener('scroll', onVisualViewport)
}

function onVisualViewport() {
  scheduleSettle()
}

function attemptFit() {
  if (!fitAddon || !term) return
  try {
    fitAddon.fit()
  } catch {
    // 元素尚未布局,忽略。
    return
  }
  // 软键盘顶上来会压缩整个布局视口(见截图:页头到底部导航全被挤到键盘上方),
  // 容器变矮 → 这里 fit → xterm 按新行数重排。重排前后有几件事不是同步完成的
  // (见 scheduleSettle 的注释),视口(ydisp)可能被留在内容之外的空白区,
  // 一整屏都是空的 —— 就是"拉起键盘就白屏"。所以贴底/重绘放到 settle 里延后做,
  // 而不是在这里同步做完就算完。
  scheduleSettle()
}

// syncPtySize 把当前行列同步给服务端 PTY。force 用于 attach/create 之后必须发一次的场合。
function syncPtySize(force = false) {
  if (!term || !activeId.value) return
  if (!force && term.cols === sentCols && term.rows === sentRows) return
  sentCols = term.cols
  sentRows = term.rows
  client.resize(term.cols, term.rows)
}

function scheduleSettle() {
  clearSettle()
  settleTimer = window.setTimeout(() => { settleTimer = undefined; settle() }, 120)
  settleTimer2 = window.setTimeout(() => { settleTimer2 = undefined; settle() }, 420)
}

function clearSettle() {
  if (settleTimer !== undefined) window.clearTimeout(settleTimer)
  if (settleTimer2 !== undefined) window.clearTimeout(settleTimer2)
  settleTimer = undefined
  settleTimer2 = undefined
}

// settle 把终端恢复到"该有的样子":尺寸对、视口贴底、整屏重绘一次。
function settle() {
  if (!term || !fitAddon) return
  try {
    fitAddon.fit()
  } catch {
    return
  }
  const t = term
  // fit() 里的 resize 会让 xterm 往 rAF 上排一次视口同步(Viewport.queueSync),
  // 那次同步按当时的渲染尺寸夹取 scrollTop,夹完还会反过来改 ydisp。所以贴底必须排在
  // 它后面 —— 这里晚一帧再做,我们才是最后一个说话的人。
  requestAnimationFrame(() => {
    if (term !== t) return
    // scrollToBottom 除了把视口拉回内容,还有个关键副作用:清掉 xterm 内部的
    // isUserScrolling 粘滞标记。那个标记一旦立起来(手指拖过、或者视口同步时
    // 按旧尺寸算出一个负的行差),后续输出就不再跟着底部走,视口能一直停在空白处;
    // 用户随手输一个字就好了,正是因为输入会触发同样的贴底。这里主动做掉。
    t.scrollToBottom()
    unstickRenderer(t)
    t.refresh(0, t.rows - 1)
    // 最后再走一条不经过上面那些闸门的重绘(见 forceRepaint)。
    forceRepaint(t)
    syncPtySize()
  })
}

// unstickRenderer 处理 xterm 渲染层的两处「卡住」。两个都只在 Via(系统 WebView)上见得到,
// Chrome 复现不出来 —— 因为它们都要求某个已排队的回调被浏览器丢掉,而 Chrome 不丢。
// 读私有字段是有意的:这两个状态没有公开出口,外面只能看到「终端整屏空白、refresh 无效」。
// 结构随版本会变,所以整段包在 try 里,读不到就当没这回事。
function unstickRenderer(t: Terminal) {
  try {
    const core: any = (t as any)._core
    const rs: any = core?._renderService
    if (!rs) return
    // 一、RenderService._isPaused:它由 IntersectionObserver 驱动,判为不可见时
    // refreshRows 全部丢弃、renderer.handleResize 推到 requestIdleCallback。键盘顶上来
    // 那一下会让 WebView 把 .xterm-screen 判成不可见,若之后没有再报一次 intersecting,
    // 这个标记就留在 true 上,行元素被 idle 任务重建成空的,而所有重绘都被丢掉 → 整屏空白。
    // 只在元素确实在视口里时才敢清它,不然会破坏 xterm 省电的本意。
    if (rs._isPaused && isOnScreen()) {
      rs._isPaused = false
      rs._pausedResizeTask?.flush?.()
      rs._needsFullRefresh = false
    }
    // 二、RenderDebouncer._animationFrame:refresh() 只要看到这个字段是真值就直接 return,
    // 而它只在自己的 rAF 回调里被清掉。那个回调一旦被 WebView 丢掉(键盘动画期间整个
    // WebView 停止出帧就会),这个字段就永远留着,此后每一次 refreshRows 都被静默吞掉,
    // term.refresh() 也彻底无效。我们此刻正跑在一个 rAF 回调里,rAF 显然是通的;
    // 比我们更早排队的回调必然已经跑过 —— 所以这时字段还是真值,基本就是个死值。
    // 直接清掉(不 cancel:万一它其实排在我们后面,照样能跑,只是多画一次)。
    const rd: any = rs._renderDebouncer
    if (rd?._animationFrame) rd._animationFrame = undefined
    // 三、SelectionService._refreshAnimationFrame:同一个模式(refresh() 见到真值就不再排队),
    // 而下面 forceRepaint 正是走它。这条也得清,否则我们最后那张底牌一样会被吞掉。
    const ss: any = core?._selectionService
    if (ss?._refreshAnimationFrame) ss._refreshAnimationFrame = undefined
  } catch {
    // xterm 内部结构变了,放弃这条修复,下面的 forceRepaint 还在。
  }
}

// isOnScreen 判断终端元素是不是真的摆在屏幕上(有尺寸、且和视口有交集)。
function isOnScreen(): boolean {
  const el = termEl.value
  if (!el) return false
  const r = el.getBoundingClientRect()
  if (r.width <= 0 || r.height <= 0) return false
  const vh = window.visualViewport?.height || window.innerHeight
  return r.bottom > 0 && r.top < vh
}

// forceRepaint 用公开 API 逼出一次整屏重绘。
// clearSelection() 会走到 SelectionService.refresh() → onRequestRedraw →
// RenderService.handleSelectionChanged() → DomRenderer.renderRows(0, rows-1)。
// 这条路既不看 _isPaused,也不过 RenderDebouncer —— 正是上面两个「卡住」都绕不开我们的地方。
// 它也不动焦点,所以手机软键盘不会被收起来(用 blur()/focus() 就会)。
// 代价只是丢掉用户已有的选区,而我们刚刚才把视口贴了底,选区本来也没了意义。
function forceRepaint(t: Terminal) {
  try {
    t.clearSelection()
  } catch {
    /* 忽略 */
  }
}

// 手指拖动滚屏。xterm 的原生滚动只发生在 .xterm-viewport 上,而 .xterm-screen 是它的
// 兄弟节点并盖在上面,触摸永远落在 screen 上 —— 而 screen 的祖先里没有可滚元素,
// 浏览器无从下手,所以手指拖不动,只有那根滚动条能拖。xterm 6 虽然打包了 VS Code 的
// Gesture,却从没 addTarget,它自己也不管触摸。这里自己接:竖向位移换算成行数,
// 走公开的 scrollLines。
let touchY = 0 // 上一次落点的 Y
let touchRest = 0 // 不满一行的余量,攒着,否则慢速拖动永远不动
let touchScrolling = false // 已判定为竖向拖动,开始吃掉事件
let touchIgnore = false // 这一轮不接(多指 / 落在滚动条上)

function rowHeight(): number {
  const row = term?.element?.querySelector('.xterm-rows > div') as HTMLElement | null
  const h = row?.getBoundingClientRect().height || 0
  if (h > 0) return h
  // 渲染器没铺出行元素时退回平均值。
  const el = term?.element
  return el && term?.rows ? el.clientHeight / term.rows : 17
}

function onTermTouchStart(e: TouchEvent) {
  touchScrolling = false
  touchRest = 0
  // 落在滚动条上时 target 就是 .xterm-viewport 本身(文本区被 screen 盖住,永远命不中它),
  // 那种情况原生拖动已经能滚,别再叠一层。多指留给系统缩放。
  const onScrollbar = (e.target as HTMLElement | null)?.classList?.contains('xterm-viewport')
  touchIgnore = e.touches.length !== 1 || !!onScrollbar
  if (touchIgnore) return
  touchY = e.touches[0].clientY
}

function onTermTouchMove(e: TouchEvent) {
  if (touchIgnore || !term || e.touches.length !== 1) return
  const y = e.touches[0].clientY
  const dy = touchY - y
  // 10px 之内还不算拖动:点一下要能弹出软键盘,不能被这里抢走。阈值不能太小 ——
  // 一次轻点手指也会滑几个像素,那点位移足够换算出一行,而向上滚一行就会把
  // xterm 的 isUserScrolling 粘滞标记立起来,之后输出不再跟着底部走。
  if (!touchScrolling && Math.abs(dy) < 10) return
  touchScrolling = true
  // 吃掉事件,页面不要跟着橡皮筋。
  e.preventDefault()
  touchY = y
  touchRest += dy
  const h = rowHeight()
  const lines = Math.trunc(touchRest / h)
  if (!lines) return
  touchRest -= lines * h
  // 手指往上 = 内容跟着往上 = 看更新的输出,正好是 scrollLines 的正方向。
  // 备用屏(vim/less 这类)没有回滚区,这里就是个空操作,交给程序自己处理。
  term.scrollLines(lines)
}

async function connect() {
  try {
    await client.connect()
    client.list()
  } catch (e: any) {
    message.error(e.message || '连接失败')
  }
}

function pickWorkspaceAndCreate() {
  selectedWs.value = store.currentId
  showNewModal.value = true
}

function createSession() {
  // 没有工作区也要能开终端:退回 root 目录。
  const ws = workspaces.value.find((w) => w.id === selectedWs.value)
  const dir = ws?.path ?? store.root
  if (ws) store.select(ws.id)
  // 切换前整屏重置:新会话从空回放开始,避免上个会话的残留输出盖在新终端上。
  term?.reset()
  sentCols = 0
  sentRows = 0
  resetTypedMark()
  pendingAttachSettle = true
  client.createSession(dir, execShell.value, term?.cols || 80, term?.rows || 24)
  showNewModal.value = false
  execShell.value = ''
}

function attachSession(sum: TermSummary) {
  if (sum.dead) {
    message.warning('该会话已结束,请新建')
    return
  }
  // 切换前整屏重置,避免上一个会话的内容残留(clear() 会留下当前提示行)。
  term?.reset()
  activeId.value = sum.id
  sentCols = 0
  sentRows = 0
  resetTypedMark()
  // 回放帧在 session 帧之前就会到,那批输出解析完要再稳定化一次(见 handleEvent)。
  pendingAttachSettle = true
  client.attach(sum.id)
  // 回放是按会话原来的行列生成的,尺寸先对上,程序收到 SIGWINCH 才会照现在的屏幕重画。
  syncPtySize(true)
  showSessionList.value = false
}

function killSession(id: string) {
  client.kill(id)
  // 结束的是当前会话:立刻脱离。否则 activeId 还指着已死的 PTY,
  // 输入照发但不会有任何回显,就是"结束之后终端无法操作"。
  if (id === activeId.value) releaseActive()
  // 进程退出是异步的,服务端会在真正退出后广播 exit + 最新列表。
}

// releaseActive 解除与当前会话的绑定,回到"选择或新建一个会话"空状态。
// 用 reset() 而不是 clear():clear() 会保留当前提示行,它会留在空状态遮罩后面。
function releaseActive() {
  client.detach()
  activeId.value = null
  pendingAttachSettle = false
  sentCols = 0
  sentRows = 0
  resetTypedMark()
  clearSettle()
  term?.reset()
}

// 会话列表渲染。
function sessionShortName(s: TermSummary): string {
  // 取工作区名(路径最后一段)+ 时间。Windows 下 dir 是反斜杠形式,两种分隔符都要切。
  const base = s.dir.split(/[/\\]/).filter(Boolean).pop() || s.dir
  const t = new Date(s.createdAt)
  const hh = String(t.getHours()).padStart(2, '0')
  const mm = String(t.getMinutes()).padStart(2, '0')
  return `${base} · ${hh}:${mm}`
}

// 粘贴按钮:手机剪贴板 → 终端。
async function pasteIntoTerm() {
  try {
    const text = await navigator.clipboard.readText()
    if (text) sendKeys(text)
    else message.info('剪贴板为空')
  } catch {
    message.warning('无法读取剪贴板(需用户手势且在安全上下文)')
  }
}

// ── 输入与命令历史 ────────────────────────────────────────────────────────────
// 所有送往 PTY 的按键都从 sendKeys 过:物理/软键盘走 term.onData,键盘条走它的
// on-key,粘贴和收藏面板的填入也走这里。回车那一刻顺手把这一行记进历史。

// 这一行里「用户从第几列开始输入」,以及量它的时候光标落在缓冲区的哪一行。
// -1 = 还没量过。行号一起记是必须的:只有量的时候那条逻辑行就是回车时的那条,
// 列号才作数。否则 —— 输出正一行行流进来时用户按了键(Ctrl-C、随手乱按),量到的
// 是某个输出行上的列;这个数留到后面真正的命令上,就会把提示符切进来(记成
// "ot@host:~/x# ping ll")或者从中间截断(记成 "k")。两种脏记录同一个来源。
let typedFrom = -1
let typedRow = -1

function resetTypedMark() {
  typedFrom = -1
  typedRow = -1
}

// 回车既可能是单独的 \r(键盘、键盘条),也可能夹在粘贴进来的一段文本里。
function isEnter(data: string): boolean {
  return data.includes('\r') || data.includes('\n')
}

function sendKeys(data: string) {
  if (!isEnter(data)) {
    // 在按键真正送出去之前量一次光标位置:此刻它还停在提示符末尾,那个列号就是
    // 提示符的显示宽度。PS1 可以随便定,认不全,量一下比拿正则去猜可靠得多。
    if (typedFrom < 0 && term) {
      const buf = term.buffer.active
      typedFrom = buf.cursorX
      typedRow = buf.baseY + buf.cursorY
    }
    client.input(data)
    return
  }
  const segs = data.split(/[\r\n]+/).filter((s) => s.trim())
  // 单独的回车:内容在缓冲区里(shell 已经回显过),去那儿读。
  if (!segs.length) captureCommand()
  // 粘贴的一段自带回车:内容就在 data 里,缓冲区此刻还没回显,直接按它记。
  // 只记单条 —— 多行粘贴那是一段文本(heredoc、配置文件),不是命令。
  else if (segs.length === 1) cmds.pushHistory(segs[0])
  resetTypedMark()
  client.input(data)
}

// 键盘条的按键也从这里走。它自己那颗回车键不经过 term.onData,
// 漏了它手机上就一条历史都记不到。
function onKbdKey(k: string) {
  sendKeys(k)
}

// captureCommand 在回车那一刻从缓冲区读出这一行的命令。读缓冲区而不是攒按键流,
// 是因为缓冲区里是 shell 自己回显出来的最终结果 —— Tab 补全、↑ 调历史、退格改词
// 全都已经算进去了。附带一个好处:没有回显的输入(密码、passphrase)在缓冲区里
// 根本不存在,结构上就采不到,不用另外去猜哪一行是密码。
function captureCommand() {
  if (!term) return
  // 备用屏 = 全屏程序(vim/less/top)在跑,那里的回车不是在敲命令。
  if (term.buffer.active.type !== 'normal') return
  cmds.pushHistory(currentInput(term))
}

// currentInput 拼出光标所在那条逻辑行里「用户输入的那一段」。
function currentInput(t: Terminal): string {
  const buf = t.buffer.active
  // 往上收:isWrapped 表示这一行是上一行按行宽硬折下来的续行,一条长命令会占好几行。
  let start = buf.baseY + buf.cursorY
  while (start > 0 && buf.getLine(start)?.isWrapped) start--
  // 量到的列号只在「量的时候和现在是同一条逻辑行」时才用。对不上就当没量过,
  // 退回按提示符记号切 —— 宁可切不准被丢掉,也不要记一条带提示符的脏命令。
  const useCol = typedFrom > 0 && typedRow === start
  const rows: string[] = []
  for (let y = start; y < buf.length; y++) {
    const line = buf.getLine(y)
    if (!line) break
    if (y > start && !line.isWrapped) break
    // 首行从 typedFrom 列切起,提示符就留在了外面。translateToString 按列取,
    // 提示符里有宽字符(中文、部分符号)也不会错位。
    rows.push(line.translateToString(false, y === start && useCol ? typedFrom : 0))
  }
  const text = rows.join('').replace(/\s+$/, '')
  // 量到过输入起点就用它;没量到(这一行整个是粘贴进来的)才退回按提示符记号切。
  return useCol ? text.trim() : stripPrompt(text)
}

// stripPrompt 从一整行里切掉提示符。提示符总以 "$ "/"# " 这类记号加一个空格收尾,
// 取最早出现的那个,后面就是命令本体 —— 取最早而不是最晚,因为命令里带 "# 注释"、
// "> 重定向" 很常见,提示符里带这些不常见。
// 一个记号都找不到就当这行不是命令行(密码提示、程序自己的输出),返回空放过。
const PROMPT_MARKS = ['$ ', '# ', '> ', '% ', '❯ ']
function stripPrompt(line: string): string {
  let at = -1
  let len = 0
  for (const m of PROMPT_MARKS) {
    const i = line.indexOf(m)
    if (i >= 0 && (at < 0 || i < at)) {
      at = i
      len = m.length
    }
  }
  return at < 0 ? '' : line.slice(at + len).trim()
}

// 填入:只把命令送到输入行,不带回车。要跑起来还得自己按一下回车 ——
// 这是远程机器,误触执行一条命令和填错一行不是一个量级的事。
function fillCommand(cmd: string) {
  if (!requireSession()) return
  sendKeys(cmd)
  term?.focus()
}

// 执行:填入 + 回车。这一条是我们自己发的,缓冲区里还没有回显,
// 所以历史直接记账,不走 captureCommand 那条路。
function runCommand(cmd: string) {
  if (!requireSession()) return
  cmds.pushHistory(cmd)
  resetTypedMark()
  client.input(cmd + '\r')
  term?.focus()
}

function requireSession(): boolean {
  if (activeId.value) return true
  message.warning('先选择或新建一个会话')
  return false
}

// dumpBuffer 把整个滚动缓冲区(含已滚出屏幕的历史)导成纯文本。
function dumpBuffer(): string {
  if (!term) return ''
  const buf = term.buffer.active
  const out: string[] = []
  for (let i = 0; i < buf.length; i++) {
    const line = buf.getLine(i)
    if (!line) continue
    // isWrapped 表示这一行是上一行按行宽硬折下来的续行:拼回去,否则复制出来的
    // 长命令中间会多出换行,粘回终端就是错的。拼接时不能截右侧空白,等整条
    // 逻辑行拼完再统一去掉行尾填充。
    const raw = line.translateToString(false)
    if (line.isWrapped && out.length) out[out.length - 1] += raw
    else out.push(raw)
  }
  const lines = out.map((s) => s.replace(/\s+$/, ''))
  // 缓冲区总是补满 rows 行,末尾的空行没有意义。
  while (lines.length && !lines[lines.length - 1]) lines.pop()
  return lines.join('\n')
}

// ── 诊断 ──────────────────────────────────────────────────────────────────────
// 这台机器上没有浏览器,Via(系统 WebView)上的表现在本地一行都复现不出来。
// 所以把「判断白屏是哪一环坏掉」需要的状态一次性抓下来,放进复制弹窗里让用户拷回来:
// 缓冲区有没有内容、行元素有几个、里面是不是空的、渲染服务是不是被暂停/防抖卡住、
// 各层元素的尺寸对不对。抓取时机就是弹窗打开的那一刻 —— 白屏正摆在眼前的时候。
const diagText = ref('')
const showDiag = ref(false)

function rectOf(sel: string, root?: Element | null): string {
  const el = sel ? (root ?? document).querySelector(sel) : (root as Element | null)
  if (!el) return `${sel || 'el'}: <无>`
  const r = el.getBoundingClientRect()
  const round = (n: number) => Math.round(n * 10) / 10
  return `${sel || 'el'}: ${round(r.width)}×${round(r.height)} @${round(r.left)},${round(r.top)}`
}

function collectDiag(): string {
  const L: string[] = []
  const t = term
  L.push(`UA: ${navigator.userAgent}`)
  const vv = window.visualViewport
  L.push(`window: ${window.innerWidth}×${window.innerHeight}  dpr=${window.devicePixelRatio}`)
  L.push(vv
    ? `visualViewport: ${Math.round(vv.width)}×${Math.round(vv.height)} offset=${Math.round(vv.offsetLeft)},${Math.round(vv.offsetTop)} scale=${vv.scale}`
    : 'visualViewport: <无>')
  L.push(`document.scrollTop: ${document.documentElement.scrollTop}`)
  if (!t) return L.concat('term: <未初始化>').join('\n')

  // 尺寸链:容器 → xterm → screen → rows → 单行。哪一层塌成 0 高就是哪一层的问题。
  L.push(rectOf('', terminalWrap.value))
  L.push(rectOf('', termEl.value))
  const xt = t.element ?? null
  L.push(rectOf('', xt))
  L.push(rectOf('.xterm-screen', xt))
  L.push(rectOf('.xterm-viewport', xt))
  const rowsEl = xt?.querySelector('.xterm-rows') as HTMLElement | null
  L.push(rectOf('.xterm-rows', xt))

  // 缓冲区。用户已经确认复制弹窗里有文本,所以这里预期是「有内容」,
  // 重点看 viewportY 落在哪:它决定可见的是哪一段。
  const buf = t.buffer.active
  L.push(`term: ${t.cols}×${t.rows}`)
  L.push(`buffer: type=${buf.type} length=${buf.length} baseY=${buf.baseY} viewportY=${buf.viewportY} cursor=${buf.cursorX},${buf.cursorY}`)
  const vis = (): string => {
    const out: string[] = []
    for (let i = 0; i < Math.min(3, t.rows); i++) {
      const line = buf.getLine(buf.viewportY + i)
      out.push(JSON.stringify((line?.translateToString(true) ?? '<无行>').slice(0, 40)))
    }
    return out.join(' | ')
  }
  L.push(`buffer 可见前 3 行: ${vis()}`)
  return L.concat(diagDom(rowsEl), diagInternals(t)).join('\n')
}

// diagDom 看行元素本身:DomRenderer 是把每行画成 .xterm-rows 下的一个 div。
// 白屏的两种可能在这里能分开 —— div 数量不对/内容为空(渲染没跑),
// 还是 div 有内容但看不见(尺寸、颜色、裁剪出了问题)。
function diagDom(rowsEl: HTMLElement | null): string[] {
  if (!rowsEl) return ['.xterm-rows: <无>']
  const kids = Array.from(rowsEl.children) as HTMLElement[]
  const isFilled = (d: HTMLElement) => (d.textContent ?? '').trim().length > 0
  const cs = getComputedStyle(rowsEl)
  const out = [
    `行元素: 共 ${kids.length} 个,非空 ${kids.filter(isFilled).length} 个,首个非空 index=${kids.findIndex(isFilled)}`,
    `.xterm-rows 样式: font=${cs.fontSize}/${cs.lineHeight} ls=${cs.letterSpacing} color=${cs.color} opacity=${cs.opacity} visibility=${cs.visibility} overflow=${cs.overflow} transform=${cs.transform}`,
  ]
  const first = kids[0]
  if (first) {
    const fs = getComputedStyle(first)
    out.push(`行[0]: ${rectOf('', first)} w=${first.style.width} h=${first.style.height} overflow=${fs.overflow} 文本=${JSON.stringify((first.textContent ?? '').slice(0, 40))}`)
    const span = first.querySelector('span')
    if (span) {
      const ss = getComputedStyle(span)
      out.push(`行[0] span: ${rectOf('', span)} display=${ss.display} h=${ss.height} color=${ss.color}`)
    } else out.push('行[0] span: <无>')
  }
  const last = kids[kids.length - 1]
  if (last) out.push(`行[末 ${kids.length - 1}]: ${rectOf('', last)} 文本=${JSON.stringify((last.textContent ?? '').slice(0, 40))}`)
  return out
}

// diagInternals 抓 xterm 内部那几个没有公开出口的状态。全部包在 try 里,
// 版本一换字段就可能没了,抓不到不影响上面那些。
function diagInternals(t: Terminal): string[] {
  const out: string[] = []
  try {
    const core: any = (t as any)._core
    const rs: any = core?._renderService
    const rd: any = rs?._renderDebouncer
    out.push(`renderService: isPaused=${rs?._isPaused} needsFullRefresh=${rs?._needsFullRefresh} rowCount=${rs?._rowCount}`)
    out.push(`renderDebouncer: animationFrame=${rd?._animationFrame} rowStart=${rd?._rowStart} rowEnd=${rd?._rowEnd}`)
    out.push(`selectionService: refreshAnimationFrame=${core?._selectionService?._refreshAnimationFrame}`)
    out.push(`pausedResizeTask: ${rs?._pausedResizeTask ? '存在' : '<无>'}`)
    out.push(`isUserScrolling: ${core?._bufferService?.isUserScrolling}`)
    const d = rs?.dimensions
    out.push(`dimensions: cell=${d?.css?.cell?.width}×${d?.css?.cell?.height} canvas=${d?.css?.canvas?.width}×${d?.css?.canvas?.height} device.cell=${d?.device?.cell?.width}×${d?.device?.cell?.height}`)
    out.push(`charSize: valid=${core?._charSizeService?.hasValidSize} ${core?._charSizeService?.width}×${core?._charSizeService?.height}`)
    out.push(`renderer: ${rs?._renderer?.value?.constructor?.name}`)
  } catch (e: any) {
    out.push(`internals: 读取失败 ${e?.message}`)
  }
  return out
}

// 打开复制弹窗。桌面端如果已经用鼠标选好了,顺手把那段也摆出来一个按钮。
function openCopyModal() {
  dumpText.value = dumpBuffer()
  dumpSelection.value = term?.getSelection() || ''
  // 白屏的时候用户能打开的就是这个弹窗,顺手把状态一起抓下来(展开才显示,不占地方)。
  diagText.value = collectDiag()
  showCopyModal.value = true
}

// 弹窗内容是懒渲染的,进场动画跑完才有布局,这时候才能把视口挪到底部 ——
// 最新的输出在最下面,那才是用户要复制的东西。
function scrollDumpToBottom() {
  const el = dumpEl.value
  if (el) el.scrollTop = el.scrollHeight
}

async function copyDump(text: string, label = '已复制') {
  if (await copyText(text)) message.success(label)
  else message.warning('浏览器不给写剪贴板,请长按选取后用系统菜单复制')
}

function selectAllDump() {
  if (dumpEl.value) selectAllIn(dumpEl.value)
}

async function loadWorkspaces() {
  try {
    await store.ensure()
    workspaces.value = store.list
    selectedWs.value = store.currentId
  } catch {
    workspaces.value = []
  }
}

onMounted(async () => {
  ensureTerm()
  await nextTick()
  attemptFit()
  setUpResize()
  await connect()
  loadWorkspaces()
})

onUnmounted(() => {
  resizeObserver?.disconnect()
  window.visualViewport?.removeEventListener('resize', onVisualViewport)
  window.visualViewport?.removeEventListener('scroll', onVisualViewport)
  clearSettle()
  client.close()
  term?.dispose()
})

// 主题切换时重刷配色。
watch(fitSignal, () => applyTheme())
</script>

<template>
  <div class="page-content terminal-page" :class="{ 'kb-open': kbInset > 0 }">
    <!-- 顶部操作栏 -->
    <div class="term-toolbar">
      <n-button size="small" quaternary @click="showSessionList = true">
        <template #icon><n-icon :component="TerminalOutline" /></template>
        会话
      </n-button>
      <!-- 收藏 / 复制 / 粘贴:只留图标。这排横向很挤,带字的话「新建」会被挤出去。
           title + aria-label 补上说明,鼠标悬停和读屏都还知道是什么。 -->
      <n-button class="term-ico" size="small" quaternary title="命令收藏" aria-label="命令收藏"
        @click="showCmdModal = true">
        <template #icon><n-icon :component="StarOutline" /></template>
      </n-button>
      <div class="spacer"></div>
      <n-button class="term-ico" size="small" quaternary aria-label="复制"
        title="导出终端文本,可自己选取复制" @click="openCopyModal">
        <template #icon><n-icon :component="CopyOutline" /></template>
      </n-button>
      <n-button class="term-ico" size="small" quaternary aria-label="粘贴"
        title="把剪贴板内容粘到终端" @click="pasteIntoTerm">
        <template #icon><n-icon :component="ClipboardOutline" /></template>
      </n-button>
      <n-button size="small" type="primary" @click="pickWorkspaceAndCreate">
        <template #icon><n-icon :component="AddOutline" /></template>
        新建
      </n-button>
    </div>

    <!-- xterm 容器。终端元素常驻:一旦 display:none,xterm 就是在一个没有布局的元素里
         open()/fit(),行高列宽、IntersectionObserver 的可见性判定全建立在错误状态上,
         回放输出也画不到屏幕上。空状态改成盖一层遮罩。 -->
    <div ref="terminalWrap" class="term-wrap" :class="{ focused: activeId }"
      @touchstart="onTermTouchStart" @touchmove="onTermTouchMove">
      <div ref="termEl" class="term-el"></div>
      <div v-if="!activeId" class="term-mask">
        <n-empty description="选择或新建一个会话开始" />
      </div>
    </div>

    <!-- 移动键盘层 -->
    <TerminalKeyboard ref="kbd" :on-key="onKbdKey" />

    <!-- 命令收藏 / 输入历史 -->
    <CommandFavorites v-model:show="showCmdModal" @fill="fillCommand" @run="runCommand" />

    <!-- 会话列表抽屉 -->
    <n-modal v-model:show="showSessionList" preset="card" title="终端会话" style="width: 92%; max-width: 420px">
      <n-list v-if="sessions.length">
        <n-list-item v-for="s in sessions" :key="s.id" class="sess-item">
          <div class="sess-main" @click="attachSession(s)">
            <div class="sess-name">
              {{ sessionShortName(s) }}
              <n-tag v-if="s.id === activeId" size="small" type="info" :bordered="false">当前</n-tag>
              <n-tag v-if="s.dead" size="small" type="error" :bordered="false">结束</n-tag>
              <n-tag v-else size="small" type="success" :bordered="false">运行中</n-tag>
            </div>
            <div class="sess-dir">{{ s.dir }}</div>
          </div>
          <template #suffix>
            <n-button class="sess-kill" size="tiny" quaternary type="error" @click.stop="killSession(s.id)">结束</n-button>
          </template>
        </n-list-item>
      </n-list>
      <n-empty v-else description="暂无会话" style="padding: 24px 0" />
    </n-modal>

    <!-- 复制弹窗:纯 DOM 文本,手机长按就能选 -->
    <n-modal v-model:show="showCopyModal" preset="card" title="终端文本" class="dump-modal"
      @after-enter="scrollDumpToBottom">
      <div v-if="dumpText" class="dump-bar">
        <n-button size="tiny" type="primary" secondary @click="copyDump(dumpText, '已复制全部')">
          复制全部
        </n-button>
        <n-button size="tiny" secondary @click="selectAllDump">全选</n-button>
        <n-button v-if="dumpSelection" size="tiny" quaternary
          @click="copyDump(dumpSelection, '已复制选中内容')">
          复制终端选中
        </n-button>
        <span class="dump-hint">长按选取可只复制一部分</span>
      </div>
      <pre v-if="dumpText" ref="dumpEl" class="dump" tabindex="0">{{ dumpText }}</pre>
      <n-empty v-else description="终端还没有输出" style="padding: 24px 0" />
      <!-- 诊断:白屏这类问题只在真机上出现,把状态摆出来让用户能一键拷回来。 -->
      <div class="diag-bar">
        <n-button size="tiny" quaternary @click="showDiag = !showDiag">
          {{ showDiag ? '收起诊断' : '诊断信息' }}
        </n-button>
        <n-button v-if="showDiag" size="tiny" secondary @click="copyDump(diagText, '已复制诊断')">
          复制诊断
        </n-button>
      </div>
      <pre v-if="showDiag" class="dump diag">{{ diagText }}</pre>
    </n-modal>

    <!-- 新建会话 -->
    <n-modal v-model:show="showNewModal" preset="card" title="新建终端会话" style="width: 92%; max-width: 420px">
      <n-form label-placement="top">
        <n-form-item label="工作区目录">
          <n-select
            v-model:value="selectedWs"
            :options="workspaces.map(w => ({ label: `${w.name} (${w.path})`, value: w.id }))"
            :placeholder="`不选则用根目录 ${store.root}`"
            clearable
          />
        </n-form-item>
        <n-form-item label="Shell(可选)">
          <n-input v-model:value="execShell" placeholder="Windows 默认 powershell,Unix 默认 $SHELL" />
        </n-form-item>
      </n-form>
      <template #footer>
        <div class="modal-footer">
          <n-button @click="showNewModal = false">取消</n-button>
          <n-button type="primary" @click="createSession">
            <template #icon><n-icon :component="PlayOutline" /></template>
            启动
          </n-button>
        </div>
      </template>
    </n-modal>
  </div>
</template>

<style scoped>
.terminal-page {
  display: flex;
  flex-direction: column;
  height: 100vh;
  /* dvh 跟随移动端地址栏收缩,100vh 会把键盘条顶到屏幕外 */
  height: 100dvh;
  /* 自己接管留白:覆盖 .page-content 的 72px 底部内边距,只给固定导航留位置,
     键盘条因此紧贴导航上沿,省下的高度全部给终端。 */
  padding: 8px 12px calc(56px + env(safe-area-inset-bottom));
  overflow: hidden;
}
/* 键盘弹出时:底部让出键盘的高度,同时不用再给底部导航留位置(它本来就在键盘底下了),
   省下的高度全给终端。--lr-kb-inset 由 useKeyboardInset 写在 :root 上。 */
.terminal-page.kb-open {
  padding-bottom: calc(var(--lr-kb-inset, 0px) + 4px);
}
.term-toolbar {
  flex: none;
  display: flex;
  align-items: center;
  gap: 6px;
  margin-bottom: 6px;
}
.spacer { flex: 1; }
/* 图标钮不留文字的位置,压到方形 */
.term-ico { width: 34px; padding: 0; }
.term-wrap {
  position: relative;
  flex: 1;
  /* min-height 不能设死值,否则小屏上会把键盘条挤出可视区 */
  min-height: 0;
  border-radius: var(--lr-radius);
  border: 1px solid rgba(127,127,127,.2);
  overflow: hidden;
  padding: 4px;
  background: var(--lr-bg);
}
@media (min-width: 768px) {
  /* 桌面端导航在左侧(见 main.css),底部不用留位置 */
  .terminal-page {
    padding-bottom: 8px;
    padding-left: calc(var(--lr-sidebar) + 12px);
  }
}
.term-el { width: 100%; height: 100%; }
/* 空状态:盖在终端上面的一层遮罩(term-wrap 已经是 position:relative)。
   之前是把 .term-el 设成 display:none,那会让 xterm 在没有布局的元素里初始化。 */
.term-mask {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 12px;
  border-radius: var(--lr-radius);
  background: var(--lr-bg);
}
.sess-main { flex: 1; min-width: 0; cursor: pointer; }
/* n-list-item 的 __main 是 flex: 1 但 min-width 仍是 auto,不清零长路径会把 suffix 里的
   结束按钮挤出可视区。 */
.sess-item :deep(.n-list-item__main) { min-width: 0; }
.sess-item :deep(.n-list-item__suffix) { margin-left: 12px; }
/* 结束按钮统一给一层浅色底色:quaternary 默认是无背景纯文字,第一个会话的按钮
   因弹窗打开时被自动聚焦而显出 hover/focus 底色,其余行没有——视觉上就参差不齐。
   显式设相同背景(用 color-mix 让浅/深主题下都跟随 --lr-danger),让每行一致。 */
.sess-item :deep(.sess-kill),
.sess-item :deep(.sess-kill.n-button:hover),
.sess-item :deep(.sess-kill.n-button:focus) {
  background: color-mix(in srgb, var(--lr-danger) 12%, transparent);
}
.sess-name { display: flex; align-items: center; gap: 6px; font-weight: 600; }
.sess-dir {
  color: var(--lr-fg-muted); font-size: 12px;
  font-family: ui-monospace, monospace;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.modal-footer { display: flex; justify-content: flex-end; gap: 8px; }

/* 复制弹窗 */
.dump-modal { width: min(680px, calc(100vw - 20px)); }
.dump-bar { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; margin-bottom: 8px; }
.diag-bar { display: flex; align-items: center; gap: 8px; margin-top: 8px; }
.diag-bar :deep(.n-button) { min-height: 28px; height: 28px; }
.diag { margin-top: 8px; max-height: 40dvh; font-size: 11px; }
/* 全局给 .n-button 定了 44px 触控高度,这排小按钮压回来。 */
.dump-bar :deep(.n-button) { min-height: 28px; height: 28px; }
.dump-hint { font-size: 12px; color: var(--lr-fg-muted); }
.dump {
  margin: 0;
  padding: 10px;
  max-height: 58dvh;
  overflow: auto;
  border-radius: var(--lr-radius);
  border: 1px solid rgba(127, 127, 127, .2);
  background: var(--lr-bg);
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
  line-height: 1.5;
  /* 长行折下来而不是横向滚:手机上横着拖选基本没法用。 */
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  /* 这个弹窗存在的全部意义就是能选中,显式打开,别被别处的 user-select:none 波及。 */
  user-select: text;
  -webkit-user-select: text;
  -webkit-touch-callout: default;
}
</style>