<script setup lang="ts">
// 移动端终端键盘层:手机虚拟键盘不送 Ctrl/Alt/Esc/Tab/方向键,
// 这里用粘滞键 + 底部工具条补全。物理键盘场景自动隐藏。
import { ref, onMounted, onUnmounted } from 'vue'
import { NIcon } from 'naive-ui'
import { BackspaceOutline, ReturnDownForwardOutline } from '@vicons/ionicons5'

const props = defineProps<{
  onKey: (key: string) => void
}>()

// 粘滞修饰符:Ctrl / Alt / Shift。点一下激活,点下一个键后自动清除。
const stickyCtrl = ref(false)
const stickyAlt = ref(false)
const stickyShift = ref(false)

function send(seq: string) {
  props.onKey(applySticky(seq))
}

// ---- 按键手势 ----
// 所有键都走 pointerdown + preventDefault,一个 click 都不用。理由有三条,缺一不可:
//
// 1. click 是在手指抬起时按「抬起位置命中的元素」派发的。软键盘一开一合会让整页重排
//    (WebView 那类浏览器直接把 layout viewport 变矮),键盘条在按下和抬起之间移了位置,
//    click 就落到别的元素上 —— 表现正是「点快捷键没反应,键盘还闪一下」。
//    pointerdown 在按下那一刻就发,重排还没发生,不会丢。
// 2. preventDefault 掉 pointerdown 会连带取消后面的 mousedown,焦点因此不会从 xterm 的
//    隐藏输入框上跑掉:软键盘不收起,粘滞的 Ctrl/Alt 还能和软键盘上的字母组合(Ctrl+C)。
// 3. 焦点从头到尾没动过,也就不存在「焦点回来时输入法又被顶起来」那一下闪动。
//    所以这里不再嗅探软键盘是开是关(那个判断只在 Chrome 的 resizes-visual 行为下成立,
//    在 WebView 上永远算出 0,反而每次触摸都去 blur,才是这条路上真正的 bug)。
//
// 代价是 :active 那套原生按下反馈没了(它跟着被取消的鼠标事件走),自己加 is-down 类补上。
function mark(e: PointerEvent, on: boolean) {
  (e.currentTarget as HTMLElement | null)?.classList.toggle('is-down', on)
}

// key 发一次按键;hold=true 的键(方向键)按住连发。
function key(e: PointerEvent, seq: string, hold = false) {
  mark(e, true)
  if (hold) holdStart(seq)
  else send(seq)
}

// tap 给不发字符、只切状态的键(Ctrl/Alt/Shift/符号面板)。
function tap(e: PointerEvent, fn: () => void) {
  mark(e, true)
  fn()
}

// ---- 方向键长按连发 ----
// 方向键在均分网格里只有一格大,靠长按补偿:首次立即发,300ms 后按 140ms 连发。
// 连发复用首次算好的序列,这样 Ctrl+← 会一直按词跳而不是第二下退化成单字符移动。
let holdTimer: ReturnType<typeof setTimeout> | null = null
let holdRepeat: ReturnType<typeof setInterval> | null = null

function holdStart(code: string) {
  holdStop()
  const seq = applySticky(code)
  props.onKey(seq)
  holdTimer = setTimeout(() => {
    holdTimer = null
    holdRepeat = setInterval(() => props.onKey(seq), 140)
  }, 300)
}

function holdStop() {
  if (holdTimer !== null) { clearTimeout(holdTimer); holdTimer = null }
  if (holdRepeat !== null) { clearInterval(holdRepeat); holdRepeat = null }
}

// 可带修饰符参数的 CSI 序列终止字符:↑↓→← / Home / End。
const CSI_FINALS = 'ABCDHF'

function modMask(): number {
  return (stickyShift.value ? 1 : 0) | (stickyAlt.value ? 2 : 0) | (stickyCtrl.value ? 4 : 0)
}

// applySticky 把已激活的粘滞修饰符作用到一次按键上,然后清除(粘滞键只生效一次)。
// 系统软键盘/物理键盘的按键也要经过这里(见 TerminalView 的 term.onData),否则
// Ctrl 只能和本键盘条上的键组合,而字母全在软键盘上 —— Ctrl+C 这类最常用的组合按不出来。
function applySticky(seq: string): string {
  const mask = modMask()
  if (!mask) return seq
  stickyCtrl.value = false
  stickyAlt.value = false
  stickyShift.value = false
  return transform(seq, mask)
}

// 标准 xterm 修饰符编码:方向键等走 CSI 1 ; <1+mask> <final>,
// 否则才退回控制码 / ESC 前缀。Ctrl+←/→ 的按词跳转依赖前者。
function transform(seq: string, mask: number): string {
  if (seq.length === 3 && seq.startsWith('\x1b[') && CSI_FINALS.includes(seq[2])) {
    return `\x1b[1;${1 + mask}${seq[2]}`
  }
  // Shift+Tab 是独立的 CBT 序列,没有修饰符参数形式。
  if (seq === '\t' && (mask & 1)) return ((mask & 2) ? '\x1b' : '') + '\x1b[Z'
  let s = seq
  if (seq.length === 1) {
    if (mask & 1) s = s.toUpperCase()
    if (mask & 4) s = ctrlTransform(s)
  }
  if (mask & 2) s = '\x1b' + s
  return s
}

// Ctrl 粘滞:对普通字符发控制码(如 c → ^C, m → ^M)。
function ctrlTransform(seq: string): string {
  const c = seq.charCodeAt(0)
  if (c >= 97 && c <= 122) return String.fromCharCode(c - 96) // a-z → 1-26
  if (c >= 65 && c <= 90) return String.fromCharCode(c - 64)
  return seq
}

const showSymbols = ref(false)
const symbols = ['~', '!', '@', '#', '$', '%', '^', '&', '*', '(', ')', '_', '+', '-', '=', '[', ']', '{', '}', '|', '\\', ':', ';', '"', "'", '<', '>', ',', '.', '/', '?']

const isTouch = ref(false)
function detectTouch() {
  isTouch.value = 'ontouchstart' in window || navigator.maxTouchPoints > 0
}

// ---- 松手 ----
// 统一挂在 window 上而不是每个按钮上:手指/鼠标在别处松开时按钮自己收不到 pointerup,
// 那样连发会停不下来、按下态也会留着不消。
const barEl = ref<HTMLDivElement>()

function releaseAll() {
  holdStop()
  barEl.value?.querySelectorAll('.is-down').forEach((el) => el.classList.remove('is-down'))
}

onMounted(() => {
  detectTouch()
  window.addEventListener('pointerup', releaseAll)
  window.addEventListener('pointercancel', releaseAll)
})
onUnmounted(() => {
  holdStop()
  window.removeEventListener('pointerup', releaseAll)
  window.removeEventListener('pointercancel', releaseAll)
})

defineExpose({ applySticky })
</script>

<template>
  <!-- touchstart.prevent 在根上兜一道:彻底掐掉这次手势的默认动作(合成 click、
       双击缩放、以及「点一下已聚焦的编辑框就重新弹输入法」那条路)。按键动作本身
       在 pointerdown 里做完了,不依赖 click —— 见 script 里 key() 上面那段。 -->
  <div v-if="isTouch" ref="barEl" class="term-kbd" @touchstart.prevent @mousedown.prevent>
    <div v-if="showSymbols" class="kbd-grid symbols">
      <button v-for="s in symbols" :key="s" class="kbd" @pointerdown.prevent="key($event, s)">{{ s }}</button>
    </div>

    <!-- 7 列均分网格,按 DOM 顺序自动填两行。方向键不额外占块:
         ↑ 落在上排第 6 格,←↓→ 落在下排第 5~7 格,倒 T 形由网格位置自然形成。 -->
    <div class="kbd-grid keys">
      <button class="kbd" @pointerdown.prevent="key($event, '\x1b')">Esc</button>
      <button class="kbd" @pointerdown.prevent="key($event, '\t')">Tab</button>
      <button class="kbd" @pointerdown.prevent="tap($event, () => showSymbols = !showSymbols)">?#</button>
      <button class="kbd" @pointerdown.prevent="key($event, '\x1b[H')">Home</button>
      <button class="kbd" @pointerdown.prevent="key($event, '\x1b[F')">End</button>
      <button class="kbd" title="上(长按连发)" @pointerdown.prevent="key($event, '\x1b[A', true)">↑</button>
      <button class="kbd" aria-label="Backspace" @pointerdown.prevent="key($event, '\x7f')">
        <n-icon :component="BackspaceOutline" />
      </button>

      <button class="kbd mod" :class="{ on: stickyCtrl }"
        @pointerdown.prevent="tap($event, () => stickyCtrl = !stickyCtrl)">Ctrl</button>
      <button class="kbd mod" :class="{ on: stickyAlt }"
        @pointerdown.prevent="tap($event, () => stickyAlt = !stickyAlt)">Alt</button>
      <button class="kbd mod" :class="{ on: stickyShift }"
        @pointerdown.prevent="tap($event, () => stickyShift = !stickyShift)">Shift</button>
      <button class="kbd accent" aria-label="Enter" @pointerdown.prevent="key($event, '\r')">
        <n-icon :component="ReturnDownForwardOutline" />
      </button>
      <button class="kbd" title="左(长按连发)" @pointerdown.prevent="key($event, '\x1b[D', true)">←</button>
      <button class="kbd" title="下(长按连发)" @pointerdown.prevent="key($event, '\x1b[B', true)">↓</button>
      <button class="kbd" title="右(长按连发)" @pointerdown.prevent="key($event, '\x1b[C', true)">→</button>
    </div>
  </div>
</template>

<style scoped>
.term-kbd {
  /* 终端页是不滚动的 flex 列,键盘条固定占位、不参与压缩 */
  flex: none;
  margin-top: 6px;
  background: var(--lr-bg-elevated);
  border-top: 1px solid rgba(127,127,127,.2);
  padding: 6px 0 0;
  display: flex;
  flex-direction: column;
  gap: 6px;
  z-index: 20;
  user-select: none;
  /* 按键动作全在 pointerdown 上,不需要浏览器的手势识别;关掉它顺手免掉
     拖动被当成平移(会发 pointercancel,把方向键的连发掐断)。 */
  touch-action: none;
}
.kbd-grid {
  display: grid;
  gap: 6px;
}
/* 主键区 7 列;窄屏用等分列宽,不给任何键留额外空间 */
.keys { grid-template-columns: repeat(7, minmax(0, 1fr)); }
.kbd {
  min-width: 0;
  height: 38px;
  border: 1px solid rgba(127,127,127,.3);
  background: var(--lr-bg);
  color: var(--lr-fg);
  border-radius: 8px;
  font-size: 13px;
  padding: 0 2px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  white-space: nowrap;
}
.kbd:active { background: var(--lr-accent); color: #fff; }
.kbd.mod.on { background: var(--lr-accent); color: #fff; border-color: var(--lr-accent); }
.kbd.accent { background: var(--lr-accent); color: #fff; border-color: var(--lr-accent); }
/* 按下反馈:pointerdown 被 preventDefault 之后原生 :active 不一定还来,自己标一个类。
   选择器要压得住 .mod.on / .accent,不然已经点亮的修饰键再按看不出反应。 */
.term-kbd .kbd.is-down {
  background: var(--lr-accent);
  color: #fff;
  border-color: var(--lr-accent);
  filter: brightness(1.25);
}
/* 符号面板列更多、键更矮,单独一套尺寸 */
.symbols { grid-template-columns: repeat(10, minmax(0, 1fr)); }
.symbols .kbd { height: 32px; font-size: 12px; }
</style>
