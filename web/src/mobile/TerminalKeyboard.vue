<script setup lang="ts">
// 移动端终端键盘层:手机虚拟键盘不送 Ctrl/Alt/Esc/Tab/方向键,
// 这里用粘滞键 + 底部工具条补全。物理键盘场景自动隐藏。
import { ref, onMounted, onUnmounted } from 'vue'
import { NIcon } from 'naive-ui'
import { BackspaceOutline, ReturnDownForwardOutline } from '@vicons/ionicons5'

const props = defineProps<{
  onKey: (key: string) => void
  // 让 xterm 的隐藏输入框失焦。软键盘处于收起状态时点本键盘条要调用它,见 onBarTouch。
  onBlurInput?: () => void
}>()

// 粘滞修饰符:Ctrl / Alt / Shift。点一下激活,点下一个键后自动清除。
const stickyCtrl = ref(false)
const stickyAlt = ref(false)
const stickyShift = ref(false)

function send(seq: string) {
  props.onKey(applySticky(seq))
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

// ---- 软键盘可见性 ----
// Android 上软键盘收起后,xterm 的隐藏 textarea 依然是 document.activeElement。
// 此后任何一次点击手势都会被 Chrome 当作"点在已聚焦的可编辑元素上"而把输入法重新弹出,
// 于是"键盘收起时点 Ctrl 又把键盘顶起来"。mousedown.prevent 拦不住:输入法是随手势
// 弹出的,不是随焦点变化弹出的,而且 mousedown 比手势识别晚。
// 所以在 touchstart(手势识别之前)判断:软键盘已收起就先 blur,让页面上没有可聚焦的
// 编辑元素。软键盘打开时绝不 blur —— 否则粘滞修饰符没法再和软键盘上的字母组合(Ctrl+C)。
const IME_MIN_HEIGHT = 120
const imeOpen = ref(false)

function syncIme() {
  const vv = window.visualViewport
  if (!vv) return
  // 未声明 interactive-widget,Chrome 默认 resizes-visual:键盘只压缩 visual viewport,
  // 与 layout viewport(innerHeight)的差值就是键盘高度。阈值过滤掉地址栏收缩(~56px)。
  imeOpen.value = window.innerHeight - vv.height > IME_MIN_HEIGHT
}

function onBarTouch() {
  if (!imeOpen.value) props.onBlurInput?.()
}

onMounted(() => {
  detectTouch()
  if (isTouch.value) window.visualViewport?.addEventListener('resize', syncIme)
})
onUnmounted(() => {
  holdStop()
  window.visualViewport?.removeEventListener('resize', syncIme)
})

defineExpose({ applySticky })
</script>

<template>
  <!-- mousedown.prevent:阻止按键抢走 xterm 隐藏输入框的焦点,否则一点 Ctrl
       系统软键盘就收起来了,粘滞修饰符也就没法配合软键盘上的字母。
       touchstart:软键盘已收起时反过来主动放掉焦点,免得这一下手势又把输入法顶起来。 -->
  <div v-if="isTouch" class="term-kbd" @mousedown.prevent @touchstart="onBarTouch">
    <div v-if="showSymbols" class="kbd-grid symbols">
      <button v-for="s in symbols" :key="s" class="kbd" @click="send(s)">{{ s }}</button>
    </div>

    <!-- 7 列均分网格,按 DOM 顺序自动填两行。方向键不额外占块:
         ↑ 落在上排第 6 格,←↓→ 落在下排第 5~7 格,倒 T 形由网格位置自然形成。 -->
    <div class="kbd-grid keys">
      <button class="kbd" @click="send('\x1b')">Esc</button>
      <button class="kbd" @click="send('\t')">Tab</button>
      <button class="kbd" @click="showSymbols = !showSymbols">?#</button>
      <button class="kbd" @click="send('\x1b[H')">Home</button>
      <button class="kbd" @click="send('\x1b[F')">End</button>
      <button class="kbd" title="上(长按连发)" @pointerdown.prevent="holdStart('\x1b[A')"
        @pointerup="holdStop" @pointercancel="holdStop" @pointerleave="holdStop">↑</button>
      <button class="kbd" aria-label="Backspace" @click="send('\x7f')">
        <n-icon :component="BackspaceOutline" />
      </button>

      <button class="kbd mod" :class="{ on: stickyCtrl }" @click="stickyCtrl = !stickyCtrl">Ctrl</button>
      <button class="kbd mod" :class="{ on: stickyAlt }" @click="stickyAlt = !stickyAlt">Alt</button>
      <button class="kbd mod" :class="{ on: stickyShift }" @click="stickyShift = !stickyShift">Shift</button>
      <button class="kbd accent" aria-label="Enter" @click="send('\r')">
        <n-icon :component="ReturnDownForwardOutline" />
      </button>
      <button class="kbd" title="左(长按连发)" @pointerdown.prevent="holdStart('\x1b[D')"
        @pointerup="holdStop" @pointercancel="holdStop" @pointerleave="holdStop">←</button>
      <button class="kbd" title="下(长按连发)" @pointerdown.prevent="holdStart('\x1b[B')"
        @pointerup="holdStop" @pointercancel="holdStop" @pointerleave="holdStop">↓</button>
      <button class="kbd" title="右(长按连发)" @pointerdown.prevent="holdStart('\x1b[C')"
        @pointerup="holdStop" @pointercancel="holdStop" @pointerleave="holdStop">→</button>
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
/* 符号面板列更多、键更矮,单独一套尺寸 */
.symbols { grid-template-columns: repeat(10, minmax(0, 1fr)); }
.symbols .kbd { height: 32px; font-size: 12px; }
</style>
