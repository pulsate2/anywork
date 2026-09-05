<script setup lang="ts">
// 终端视图:多窗口 PTY + 服务端滚动缓冲 + 移动键盘层。
import { ref, onMounted, onUnmounted, nextTick, watch, computed } from 'vue'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import {
  NButton, NIcon, NModal, NList, NListItem,
  NTag, NEmpty, useMessage, NSelect, NForm, NFormItem, NInput, NInputNumber, NSwitch, NSlider,
} from 'naive-ui'
import { useRoute, useRouter } from 'vue-router'
import { TerminalOutline, AddOutline, PlayOutline, CopyOutline, ClipboardOutline, StarOutline, ExpandOutline, ContractOutline, KeypadOutline } from '@vicons/ionicons5'
import { TermClient, type TermSummary } from '@/api/term'
import { api, type Workspace, type TermLimitSupport } from '@/api/client'
import { useWorkspaceStore } from '@/stores/workspace'
import { useCommandStore } from '@/stores/commands'
import { copyText, selectAllIn } from '@/utils/clipboard'
import { useSoftKeyboard } from '@/utils/softKeyboard'
import { isTouchDevice } from '@/utils/touch'
import TerminalKeyboard from '@/mobile/TerminalKeyboard.vue'
import CommandFavorites from '@/components/CommandFavorites.vue'

const message = useMessage()
const store = useWorkspaceStore()
const cmds = useCommandStore()
const route = useRoute()
const router = useRouter()
// 软键盘状态,两个量分工不同(见 utils/softKeyboard.ts):
// - kbOpen:键盘开着。底部导航这时会被 BottomNav 收掉,终端页也就不必再给它留那 56px,
//   省下的高度全给终端。
// - kbInset:layout viewport 下沿被键盘遮住的高度。Chrome 不会因为键盘压缩 layout
//   viewport,100dvh 的页面完全感知不到键盘,底部的键盘条被压在键盘下面 —— 拿这个值当
//   额外的底部内边距,页面自己变矮,键盘条回到键盘上沿,终端跟着 fit。Via 那类会整页
//   resize 的浏览器算出来是 0(它已经变矮了),不会重复补偿。
const { inset: kbInset, open: kbOpen } = useSoftKeyboard()
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

// ---- 新会话的资源限制 ----
// 能限什么由服务端说(Linux cgroup v2 / Windows Job 对象),前端只负责按它显示输入框:
// 悄悄忽略用户填的上限比不提供这个功能更糟。上次填的值记在本地,常用配置不用重填。
const LIM_KEY = 'lr.term.limit.'
const BASH_KEY = 'lr.term.bash'
const limitSupport = ref<TermLimitSupport | null>(null)
const limitOn = ref(localStorage.getItem(LIM_KEY + 'on') === '1')
const limitMem = ref<number | null>(Number(localStorage.getItem(LIM_KEY + 'mem')) || 512)
// 滑块的值不会是 null(输入框可以清空,滑块不行),类型上就不留这个口子。
const limitCPU = ref<number>(Number(localStorage.getItem(LIM_KEY + 'cpu')) || 50)
// 以 bash 启动。默认 $SHELL 常常是 sh/dash,补全和 [[ ]] 都没有;这是个偏好,记在本地。
const useBash = ref(localStorage.getItem(BASH_KEY) === '1')
watch(useBash, v => localStorage.setItem(BASH_KEY, v ? '1' : '0'))
// 不支持的项只说一句话。原理那段(detail)太长,挪到 title 上,想看再看。
const limitNote = computed(() => {
  const s = limitSupport.value
  if (!s) return ''
  if (s.mode === 'none') return '本机不支持资源限制'
  if (s.mode === 'rlimit') return '只能用 ulimit 限内存,CPU 不可用'
  if (!s.memory) return '本机不支持内存限制'
  if (!s.cpu) return '本机不支持 CPU 限制'
  return ''
})
// CPU 填的是"占整机百分比",换成核数好判断 —— 8 核机器上填 25 就是 2 个核。
const limitCoreHint = computed(() => {
  const cores = limitSupport.value?.cores || 0
  const pct = limitCPU.value || 0
  if (cores <= 0 || pct <= 0) return ''
  return `≈ ${(cores * pct / 100).toFixed(1)} / ${cores} 核`
})

watch(limitOn, v => localStorage.setItem(LIM_KEY + 'on', v ? '1' : '0'))
watch(limitMem, v => { if (v) localStorage.setItem(LIM_KEY + 'mem', String(v)) })
watch(limitCPU, v => { if (v) localStorage.setItem(LIM_KEY + 'cpu', String(v)) })

// 只在第一次打开新建弹窗时问一次:没用过这个功能的人不该多付一次请求。
async function ensureLimitSupport() {
  if (limitSupport.value) return
  try {
    limitSupport.value = await api.termLimits()
  } catch { /* 问不到就当不支持,不显示输入框 */ }
}

// 会话卡片上的限额标签。
function limitTags(s: TermSummary): string[] {
  const out: string[] = []
  if (s.memoryMB) out.push(`内存 ${s.memoryMB} MB`)
  if (s.cpuPercent) out.push(`CPU ${s.cpuPercent}%`)
  return out
}

// 复制弹窗:手机上没法在 xterm 画布里拖选,只能把缓冲区文本倒进一个普通
// DOM 元素,交给系统的长按选取。
const showCopyModal = ref(false)
const dumpText = ref('')
const dumpSelection = ref('')
const dumpEl = ref<HTMLPreElement>()

// 终端尝试 resize 用的信号。
const fitSignal = ref(0)

// ---- 快捷键条显隐 ----
// 键盘条本身占掉七八十像素高。手机上看长输出(日志、diff)时那几行比快捷键值钱,
// 所以给一个显隐开关。开关只在触控设备上出现:桌面根本没有这条(见 TerminalKeyboard
// 的 isTouch 判定),画一个点了什么都不变的按钮比不画更糟。
const KBD_KEY = 'lr.term.kbd'
const isTouch = isTouchDevice()
// 缺省显示:第一次进来的用户得先看见有这么个东西,才知道可以收。
const showKbd = ref(localStorage.getItem(KBD_KEY) !== '0')
watch(showKbd, (v) => {
  localStorage.setItem(KBD_KEY, v ? '1' : '0')
  // 条是 flex:none 的兄弟节点,它一进一出终端的可用高度就跟着变 —— 和软键盘顶上来是
  // 同一类事,得走同一套稳定化(见 scheduleSettle)。terminalWrap 上的 ResizeObserver
  // 本来也会响,这里显式再排一次:settle 是幂等的,重复一次的代价远小于漏掉一次
  // (漏掉的表现是终端行数不对、视口停在空白处)。
  scheduleSettle()
})

// ---- 终端窗口全屏 ----
// 把终端这一页提成一层铺满视口的定位层:底部导航、页面留白、顶栏(见下面那段)全让出来,
// 终端多出六七行。不走浏览器原生的 Fullscreen API(那是「网页全屏」),它会附带两件
// 我们不想要的事:① 全屏期间 Escape 被浏览器留着退出全屏,根本不派发给页面 —— 而 Esc 是
// 终端里最要紧的键之一(vim、readline 的 vi 模式全指着它),要救回来得动 Keyboard Lock,
// 还只有 Chromium 有;② iOS Safari 至今只给 <video> 全屏,手机上最需要这个功能的地方
// 恰好没有。铺满视口这条路在哪个浏览器上都成立,Esc 也照常进终端,按钮不需要能力探测。
//
// 全屏状态记在地址栏的 full 参数上,不是一个普通的 ref —— 为的是「按返回键退出全屏」:
// 进全屏时往历史里推一条 /terminal?full=1,系统返回键(Android 手势返回、浏览器后退)
// 弹掉它就等于退出全屏,而不是把人从终端页整页退走。同一条路径只换 query,组件不重建、
// WebSocket 不重连、滚动位置也不会被拽走(见 router 的 scrollBehavior)。
// 另一条出路是长按键盘条上的 Esc(见 TerminalKeyboard 的 onEscHold)。
const zoomed = computed(() => route.query.full === '1')
// 那条 full=1 是不是我们自己推进去的。带着 full=1 直接进来也是可能的(刷新页面、
// PWA 从上次的地址恢复、书签),那时历史里没有可弹的东西,退出得改成把 query 抹掉。
let pushedZoom = false

function enterZoom() {
  if (zoomed.value) return
  pushedZoom = true
  router.push({ path: route.path, query: { ...route.query, full: '1' } })
  // 全屏之后顶栏一起收起,屏幕上不再有任何「怎么出去」的线索,这句提示就是唯一的说明书。
  // 长按 Esc 那条只有键盘条在的时候才提 —— 条被收起来时它确实按不到。
  message.info(isTouch && showKbd.value ? '已全屏 · 返回键或长按 Esc 退出' : '已全屏 · 按返回键退出',
    { duration: 2500 })
}

function exitZoom() {
  if (!zoomed.value) return
  // 自己推的那条就弹掉,而不是 replace 把 query 抹掉:replace 会把这条记录留在历史里,
  // 再按一次返回键又回到全屏。反过来,不是自己推的就只能 replace —— 那时 back() 会把人
  // 弹出这个站点(或者什么都不做),而用户按的只是「退出全屏」。
  if (pushedZoom) {
    pushedZoom = false
    router.back()
    return
  }
  const q = { ...route.query }
  delete q.full
  router.replace({ path: route.path, query: q })
}

// 顶栏在全屏时整条收起:它加上留白有五十来像素,又是三行终端,而全屏本来就是为了那几行。
// 收起了也不用再拉出来 —— 出去的两条路(返回键、长按 Esc)都不需要它。
watch(zoomed, (on) => {
  // 出了全屏就把标记清掉:这一轮推进去的那条记录已经不在栈顶了。之后再进全屏会重新推。
  if (!on) pushedZoom = false
  // 这一层的尺寸变了(导航、留白和顶栏都让出来),和软键盘顶上来是同一类问题,
  // 统一走稳定化,不做同步 fit(见 scheduleSettle)。
  scheduleSettle()
})

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

// ── 断线与重连 ────────────────────────────────────────────────────────────────
// 手机切后台会被系统断网,回来时这条 WS 多半已经废了,而且常常是「半开」的:
// readyState 还停在 OPEN,send() 也不报错,但帧再也到不了对端 —— 屏幕上就是
// 敲什么都没反应。所以判活不看 readyState,只看发出去有没有回音(TermClient.probe)。
// PTY 在服务端是独立于连接活着的(还带 1MB 回放缓冲),重连后 attach 回去屏幕会整屏补齐。
// 一进页面就在连了(onMounted 里),初值给 connecting,免得先绿一下再变。
const netState = ref<'ok' | 'connecting' | 'down'>('connecting')
const netLabel = computed(() =>
  netState.value === 'ok' ? '已连接' : netState.value === 'connecting' ? '连接中' : '已断开')
let reconnectTimer: number | undefined
let reconnectTries = 0
// 连接尝试的代号。慢网下上一次 connect 可能还挂着(WS 建连能卡半分钟),回前台又发起
// 了新的一次;用它把迟到的旧结果丢掉,免得旧的失败把新连上的状态又改回断开。
let connEpoch = 0
// 重连后先拿列表再决定 attach 谁:目标会话可能在断线那会儿已经退出并被回收。
let resumeTarget: string | null = null
// 组件已卸载:不再重连,也不再碰已经 dispose 的 term。
let disposed = false

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
      // 重连回来的那一份:目标会话还活着就接回去,没了就退到空状态并说一声,
      // 不能顺手把用户塞进别的会话(那正是 autoAttachDone 要防的事)。
      if (resumeTarget) {
        const want = resumeTarget
        resumeTarget = null
        if ((e.list as TermSummary[]).some((s) => s.id === want && !s.dead)) attachById(want)
        else {
          releaseActive()
          message.warning('原会话已结束')
        }
        break
      }
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
      // activeId 有意保留:屏幕上还是那个会话的内容,重连后 attach 回去接着用。
      if (disposed) break
      netState.value = 'down'
      scheduleReconnect()
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

// 建立连接。首次进页面和之后每一次重连都走这里,区别只在失败要不要弹提示:
// 刚进页面得说明终端为什么是空的,后台自动重连失败则不该一遍遍弹 toast。
async function connect(notify = false) {
  if (disposed) return
  clearReconnect()
  const epoch = ++connEpoch
  netState.value = 'connecting'
  reconnectTries++
  try {
    await client.connect()
  } catch (e: any) {
    if (disposed || epoch !== connEpoch) return
    netState.value = 'down'
    if (notify) message.error(e?.message || '连接失败')
    scheduleReconnect()
    return
  }
  if (disposed || epoch !== connEpoch) return
  reconnectTries = 0
  netState.value = 'ok'
  // 不直接 attach 回 activeId:那个会话可能在断线期间已经退出并被回收,
  // attach 失败只会回一句笼统的 error,分不清是哪种情况。先要列表,在 handleEvent 里判。
  resumeTarget = activeId.value
  client.list()
}

function clearReconnect() {
  if (reconnectTimer !== undefined) window.clearTimeout(reconnectTimer)
  reconnectTimer = undefined
}

// 退避重连:0.5s 起翻倍,封顶 8s。页面不可见时不排 —— 手机后台里定时器本来就被冻着,
// 网也是断的,白试;回到前台由 onResume 立刻重连一次。
function scheduleReconnect() {
  if (disposed || reconnectTimer !== undefined || document.hidden) return
  const wait = Math.min(8000, 500 * 2 ** Math.min(reconnectTries, 4))
  reconnectTimer = window.setTimeout(() => {
    reconnectTimer = undefined
    connect()
  }, wait)
}

// 回到前台 / 系统报网络恢复。两件事都得做:
//  ① 连接:后台那会儿多半被断了,可能还是半开的,所以用 probe 探一次而不是看 readyState;
//  ② 重绘:页面不可见时 xterm 的 IntersectionObserver 会把渲染整个暂停,回来后要逼它
//     整屏重画一次,否则画面就停在切走那一刻(见 unstickRenderer 的注释)。
// 有些 WebView / iOS 从后台回来只报 focus 不报 visibilitychange,所以两个事件都听;
// 1 秒内只当一次,免得桌面上来回切窗口把探针刷起来。
let resumeAt = 0
async function onResume() {
  if (disposed || document.hidden) return
  const now = Date.now()
  if (now - resumeAt < 1000) return
  resumeAt = now
  scheduleSettle()
  const ok = await client.probe()
  if (disposed || document.hidden) return
  if (!ok) {
    connect()
    return
  }
  // 连接本来就没断(短暂切走):服务端的附加关系还在,不用重新 attach,刷一下列表就行。
  netState.value = 'ok'
  client.list()
}

// 断开期间的按键提示。三秒内只说一次:一次断线用户可能连按十几下。
let offlineWarnAt = 0
function warnOffline() {
  const now = Date.now()
  if (now - offlineWarnAt < 3000) return
  offlineWarnAt = now
  message.warning('连接已断开,正在重连')
}

// 按键时顺手探一次活。半开连接下 send 不报错、readyState 也是 OPEN,只有"发了没回音"
// 能证明它废了 —— 而用户正盯着屏幕等回显,这时候发现最有用。最多 10s 一次。
let keyProbeAt = 0
function probeAfterKey() {
  const now = Date.now()
  if (now - keyProbeAt < 10000) return
  keyProbeAt = now
  client.probe().then((ok) => {
    if (ok || disposed || netState.value === 'connecting') return
    warnOffline()
    connect()
  })
}

// 会话列表:断线时列表是断线前的旧数据,顺手重连一次(状态圆点变红后用户多半会点这里)。
function openSessions() {
  showSessionList.value = true
  if (!client.connected && netState.value !== 'connecting') connect()
}

function pickWorkspaceAndCreate() {
  selectedWs.value = store.currentId
  showNewModal.value = true
  ensureLimitSupport()
}

function createSession() {
  // 断线时 create 帧会被静默丢掉,弹窗一关什么也没发生 —— 先把连接找回来。
  if (!client.connected) {
    warnOffline()
    if (netState.value !== 'connecting') connect()
    return
  }
  // 没有工作区也要能开终端:退回 root 目录。
  const ws = workspaces.value.find((w) => w.id === selectedWs.value)
  const dir = ws?.path ?? store.root
  if (ws) store.select(ws.id)
  // 新建了就不再接回原会话。
  resumeTarget = null
  // 切换前整屏重置:新会话从空回放开始,避免上个会话的残留输出盖在新终端上。
  term?.reset()
  sentCols = 0
  sentRows = 0
  resetTypedMark()
  pendingAttachSettle = true
  // 只提交本机真能限的那一项:请求了做不到的限制,服务端会直接拒绝建会话。
  const sup = limitSupport.value
  const use = limitOn.value && sup && sup.mode !== 'none'
  client.createSession(dir, useBash.value ? 'bash' : execShell.value, term?.cols || 80, term?.rows || 24, {
    memoryMB: use && sup!.memory ? (limitMem.value || 0) : 0,
    cpuPercent: use && sup!.cpu ? (limitCPU.value || 0) : 0,
  })
  showNewModal.value = false
  // execShell 是一次性的(填了个别路径就用一次),useBash 是记住的偏好,不清。
  execShell.value = ''
}

function attachSession(sum: TermSummary) {
  if (sum.dead) {
    message.warning('该会话已结束,请新建')
    return
  }
  attachById(sum.id)
  showSessionList.value = false
}

// attachById 只管附加这个动作本身。重连回来时手上只有一个 id(会话对象是断线前的旧数据),
// 活没活着由刚拿到的列表判,这里不再判一遍。
function attachById(id: string) {
  // 用户自己选了会话,重连那份"接回原会话"的意图就作废了,否则列表一到会把他拽回去。
  resumeTarget = null
  // 切换前整屏重置,避免上一个会话的内容残留(clear() 会留下当前提示行)。
  term?.reset()
  activeId.value = id
  sentCols = 0
  sentRows = 0
  resetTypedMark()
  // 回放帧在 session 帧之前就会到,那批输出解析完要再稳定化一次(见 handleEvent)。
  pendingAttachSettle = true
  client.attach(id)
  // 回放是按会话原来的行列生成的,尺寸先对上,程序收到 SIGWINCH 才会照现在的屏幕重画。
  syncPtySize(true)
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
  resumeTarget = null
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
  // 连接断了就别假装收下:这条路上 send() 是静默丢弃的,用户只会看到"敲了没反应"。
  if (!client.connected) {
    warnOffline()
    if (netState.value !== 'connecting') connect()
    return
  }
  probeAfterKey()
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
  // 手机切后台被断网,回来时要靠这几个事件把连接和画面都拉回来(见 onResume)。
  document.addEventListener('visibilitychange', onResume)
  window.addEventListener('focus', onResume)
  window.addEventListener('online', onResume)
  await connect(true)
  loadWorkspaces()
})

onUnmounted(() => {
  disposed = true
  resizeObserver?.disconnect()
  window.visualViewport?.removeEventListener('resize', onVisualViewport)
  window.visualViewport?.removeEventListener('scroll', onVisualViewport)
  document.removeEventListener('visibilitychange', onResume)
  window.removeEventListener('focus', onResume)
  window.removeEventListener('online', onResume)
  clearReconnect()
  clearSettle()
  client.close()
  term?.dispose()
})

// 主题切换时重刷配色。
watch(fitSignal, () => applyTheme())
</script>

<template>
  <div class="page-content terminal-page" :class="{ 'kb-open': kbOpen, zoomed }">
    <!-- 顶部操作栏。全屏时整条收起(off):不是 v-if 拿掉,而是浮起来位移出视口 —— 留在
         DOM 里,回来时不用重新挂载,也不会因为它一进一出改变终端高度(那会牵出 fit /
         缓冲重排 / SIGWINCH 一整串,折行的长命令会被搅乱)。 -->
    <div class="term-toolbar" :class="{ off: zoomed }" :aria-hidden="zoomed || undefined">
      <n-button class="term-sess" size="small" quaternary @click="openSessions">
        <template #icon><n-icon :component="TerminalOutline" /></template>
        会话
        <!-- 连接状态圆点:绿=已连接,黄=连接中,红=已断开。8px 的绿和黄在小屏上不好分辨,
             所以颜色之外还有 title/aria-label,连接中另加一点呼吸动效当第二个信号。 -->
        <span class="net-dot" :class="netState" role="img" :aria-label="netLabel" :title="netLabel"></span>
      </n-button>
      <!-- 收藏 / 快捷键 / 全屏 / 复制 / 粘贴:只留图标。这排横向很挤,带字的话「新建」会被挤出去。
           title + aria-label 补上说明,鼠标悬停和读屏都还知道是什么。 -->
      <n-button class="term-ico" size="small" quaternary title="命令收藏" aria-label="命令收藏"
        @click="showCmdModal = true">
        <template #icon><n-icon :component="StarOutline" /></template>
      </n-button>
      <!-- 快捷键条显隐。只在触控设备上出现:桌面本来就没有那条键盘条,给一个点了
           什么都不变的按钮比不给更糟。 -->
      <n-button v-if="isTouch" class="term-ico" size="small" quaternary
        :type="showKbd ? 'primary' : 'default'"
        :title="showKbd ? '隐藏快捷键条' : '显示快捷键条'"
        :aria-label="showKbd ? '隐藏快捷键条' : '显示快捷键条'"
        @click="showKbd = !showKbd">
        <template #icon><n-icon :component="KeypadOutline" /></template>
      </n-button>
      <!-- 全屏。它在全屏下是够不着的(整条顶栏都收起来了),出去的路是返回键或长按 Esc,
           见 script 里 zoomed 那段。这里仍按开关写:状态由地址栏的 full 参数决定,
           万一以后顶栏在全屏下又露出来,这颗钮的行为是对的。 -->
      <n-button class="term-ico" size="small" quaternary
        :type="zoomed ? 'primary' : 'default'"
        :title="zoomed ? '退出全屏' : '终端全屏'" :aria-label="zoomed ? '退出全屏' : '终端全屏'"
        @click="zoomed ? exitZoom() : enterZoom()">
        <template #icon><n-icon :component="zoomed ? ContractOutline : ExpandOutline" /></template>
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
      <!-- 窄屏(≤340px)下这颗钮的文字会被 CSS 藏掉(见 .term-new),display:none 的
           文字读屏也读不到,所以 title/aria-label 一直挂着补上名字。 -->
      <n-button class="term-new" size="small" type="primary" title="新建会话" aria-label="新建会话"
        @click="pickWorkspaceAndCreate">
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

    <!-- 移动键盘层。全屏时长按它的 Esc 退出全屏:那会儿顶栏是收起的,全屏钮点不到,
         而这条键盘条一直在手边(见 TerminalKeyboard 的 escDown)。不全屏就不给这个回调,
         免得键上挂一句用不上的「长按退出全屏」。 -->
    <TerminalKeyboard ref="kbd" :on-key="onKbdKey" :visible="showKbd"
      :on-esc-hold="zoomed ? exitZoom : undefined" />

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
            <div v-if="limitTags(s).length" class="sess-lim">
              <n-tag v-for="t in limitTags(s)" :key="t" size="tiny" type="warning" :bordered="false">{{ t }}</n-tag>
            </div>
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
          <div class="lim-box">
            <!-- $SHELL 在不少机器上是 sh/dash,补全和 [[ ]] 都没有;开着就固定用 bash。
                 开关优先于下面的输入框,所以顺手把它禁掉,免得填了却不生效。 -->
            <label class="lim-switch">
              <n-switch v-model:value="useBash" size="small" />
              <span>以 bash 启动</span>
            </label>
            <n-input v-model:value="execShell" :disabled="useBash"
              placeholder="Windows 默认 powershell,Unix 默认 $SHELL" />
          </div>
        </n-form-item>
        <!-- 资源限制:本机支持什么由 /api/term/limits 说 -->
        <n-form-item v-if="limitSupport" label="资源限制">
          <div class="lim-box">
            <label class="lim-switch">
              <n-switch v-model:value="limitOn" size="small" :disabled="limitSupport.mode === 'none'" />
              <span>限制这个会话的内存 / CPU</span>
            </label>
            <template v-if="limitOn && limitSupport.mode !== 'none'">
              <div v-if="limitSupport.memory" class="lim-row">
                <span class="lim-label">内存上限</span>
                <n-input-number v-model:value="limitMem" size="small" :min="16" :max="1048576" :step="128"
                  style="flex:1" placeholder="MB">
                  <template #suffix>MB</template>
                </n-input-number>
              </div>
              <div v-if="limitSupport.cpu" class="lim-row">
                <span class="lim-label">CPU 上限</span>
                <n-slider v-model:value="limitCPU" :min="5" :max="100" :step="5" style="flex:1" />
                <span class="lim-val">{{ limitCPU }}%</span>
              </div>
              <div v-if="limitSupport.cpu && limitCoreHint" class="lim-hint">占整机百分比,{{ limitCoreHint }}</div>
            </template>
            <div v-if="limitNote" class="lim-hint" :title="limitSupport.detail">{{ limitNote }}</div>
          </div>
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
/* 键盘弹出时:底部让出键盘遮住的高度,同时不用再给底部导航留位置 —— 键盘一开它就自己
   滑下去了(见 main.css 的 .bottom-nav.kb-open),省下的高度全给终端。
   --lr-kb-inset 由 useSoftKeyboard 写在 :root 上;整页 resize 那类浏览器算出来是 0,
   页面已经变矮了,不需要再补。 */
.terminal-page.kb-open {
  padding-bottom: calc(var(--lr-kb-inset, 0px) + 4px);
}
/* 终端全屏:这一页本来就是 100dvh 的不滚动列,所以「全屏」要让出来的只有底部导航
   (桌面是左侧栏)、页面自己那点留白,以及顶栏(它整条收起,见 .term-toolbar.off)——
   把这一层抬到导航上面盖住,再把留白削到最小,终端多出六七行。
   导航是 position: fixed + z-index: 100 的兄弟节点,不受流影响,只能盖;而 z-index 要
   生效得先定位,于是 relative(不用 fixed:高度仍由那条 100dvh 管着,不去碰移动端
   fixed 元素包含块和 dvh 不一致这摊事)。这个 relative 同时给收起的顶栏当定位参照。
   背景必须显式给:留白处透出去就会看见底下的导航。 */
.terminal-page.zoomed {
  position: relative;
  z-index: 200;
  padding: 6px 8px calc(4px + env(safe-area-inset-bottom));
  background: var(--lr-bg);
}
/* 键盘的补偿要压过上面那行 padding(同特异性、写在后面就会赢),所以单独提一档。 */
.terminal-page.zoomed.kb-open {
  padding-bottom: calc(var(--lr-kb-inset, 0px) + 4px);
}
.term-toolbar {
  flex: none;
  display: flex;
  align-items: center;
  gap: 6px;
  margin-bottom: 6px;
  /* 兜底:实在排不下就折行,而不是把最右边的「新建」切掉一半。naive 的钮是
     flex-shrink: 0,页面又是 overflow: hidden,不留折行余地就是直接裁掉。
     折行会吃掉两行终端,所以下面两档媒体查询先尽量把它压进一行。 */
  flex-wrap: wrap;
}
.spacer { flex: 1; }
/* 全屏时的顶栏:出流浮起来再整条位移出视口。不用 v-if 拿掉它 —— 它一进一出会改变终端
   高度,牵出 fit → 缓冲重排 → SIGWINCH 一整串,折行的长命令会被搅乱;浮起来则一行都
   不用重排。transform 也不触发布局,进出全屏因此只有一次尺寸变化(留白和导航那次)。
   visibility 一并关掉,免得还能 Tab 聚焦到看不见的按钮上(opacity: 0 的元素照样在焦点
   序列里);它按离散规则插值,所以放进 transition 是为了让元素等动画走完再消失。 */
.term-toolbar.off {
  position: absolute;
  top: 6px;
  left: 8px;
  right: 8px;
  z-index: 30;
  margin: 0;
  transform: translateY(calc(-100% - 12px));
  opacity: 0;
  visibility: hidden;
  pointer-events: none;
  transition: transform .18s ease, opacity .18s ease, visibility .18s;
}
@media (prefers-reduced-motion: reduce) {
  .term-toolbar.off { transition: none; }
}
/* 图标钮不留文字的位置,压到方形 */
.term-ico { width: 34px; padding: 0; }
/* 这一排原本就挤(见模板里的注释),加了快捷键和全屏两个钮之后默认要 380px 的视口:
   内容 356px(带字的两个钮各 72 = 左右各 10 内边距 + 18 图标 + 6 图标外边距 + 两个字 28,
   五个图标钮各 34,七道 6px 间距)+ 页面左右留白各 12。375px 的 iPhone 正好越线。
   下面两档按屏宽依次收紧,把它压回一行。 */
@media (max-width: 400px) {
  .term-toolbar { gap: 4px; }
  .term-ico { width: 30px; }
  /* 带字的那两个只削内边距,字留着 —— 整排都变成图标就认不出来了。这里点名写
     两个类而不是 :not(.term-ico),好让下面那档能用同等特异性再改「新建」。 */
  .term-sess, .term-new { padding: 0 8px; }
}
/* 320px 那档(iPhone SE 一代之类)还差十几个像素:「新建」也退成图标。加号比「会话」
   两个字好猜,而「会话」钮上还挂着连接状态圆点,留它的文字更值。
   naive 只在 default 插槽为空时才把图标外边距清零,这里的文字是用 CSS 藏的,
   那 6px 还在,得自己抹掉,不然图标在方钮里偏左 3px。 */
@media (max-width: 340px) {
  .term-new { width: 30px; padding: 0; }
  .term-new :deep(.n-button__content) { display: none; }
  .term-new :deep(.n-button__icon) { margin: 0; }
}
/* 连接状态圆点,贴在「会话」钮的右下角。.n-button 本身是 position: relative,
   但别指望组件库的实现细节,这里自己声明一次。 */
.term-sess { position: relative; }
.net-dot {
  position: absolute;
  right: 5px;
  bottom: 5px;
  width: 8px;
  height: 8px;
  border-radius: 50%;
  /* 一圈灰描边:钮被按下/悬停时底色会变,圆点得跟它分开。用中性灰,浅深主题通吃。 */
  box-shadow: 0 0 0 1.5px rgba(127, 127, 127, .25);
  background: var(--lr-fg-muted);
  pointer-events: none;
}
.net-dot.ok { background: var(--lr-ok); }
.net-dot.connecting { background: var(--lr-warn); animation: net-pulse 1.1s ease-in-out infinite; }
.net-dot.down { background: var(--lr-danger); }
@keyframes net-pulse {
  50% { opacity: .3; }
}
@media (prefers-reduced-motion: reduce) {
  .net-dot.connecting { animation: none; }
}
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
.sess-lim { display: flex; gap: 4px; flex-wrap: wrap; margin-top: 3px; }

/* 新建会话里的资源限制 */
.lim-box { display: flex; flex-direction: column; gap: 8px; width: 100%; }
.lim-switch { display: flex; align-items: center; gap: 8px; font-size: 13px; }
.lim-row { display: flex; align-items: center; gap: 8px; }
.lim-label { font-size: 12px; color: var(--lr-fg-muted); width: 58px; flex: none; }
.lim-val { font-size: 12px; width: 38px; text-align: right; font-variant-numeric: tabular-nums; }
.lim-hint { font-size: 11px; line-height: 1.5; color: var(--lr-fg-muted); }

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