<script setup lang="ts">
// 移动端终端键盘层:手机虚拟键盘不送 Ctrl/Alt/Esc/Tab/方向键,
// 这里用粘滞键 + 底部工具条补全。物理键盘场景自动隐藏。
import { ref, onMounted, onUnmounted, computed } from 'vue'
import { NIcon } from 'naive-ui'
import { BackspaceOutline, ReturnDownForwardOutline } from '@vicons/ionicons5'

const props = defineProps<{
  onKey: (key: string) => void
}>()

// 粘滞修饰符:Ctrl / Alt。点一下激活,点下一个键后自动清除。
const stickyCtrl = ref(false)
const stickyAlt = ref(false)

// 方向键宏。
const arrows = [
  { sym: '↑', code: '\x1b[A' },
  { sym: '↓', code: '\x1b[B' },
  { sym: '←', code: '\x1b[D' },
  { sym: '→', code: '\x1b[C' },
]

function send(seq: string) {
  props.onKey(applySticky(seq))
}

// applySticky 把已激活的粘滞修饰符作用到一次按键上,然后清除(粘滞键只生效一次)。
// 系统软键盘/物理键盘的按键也要经过这里(见 TerminalView 的 term.onData),否则
// Ctrl 只能和本键盘条上的键组合,而字母全在软键盘上 —— Ctrl+C 这类最常用的组合按不出来。
function applySticky(seq: string): string {
  if (!stickyCtrl.value && !stickyAlt.value) return seq
  let s = seq
  if (stickyCtrl.value) s = ctrlTransform(seq)
  if (stickyAlt.value) s = '\x1b' + s
  stickyCtrl.value = false
  stickyAlt.value = false
  return s
}

// Ctrl 粘滞:对普通字符发控制码(如 c → ^C, m → ^M),方向键/制表符直接透传。
function ctrlTransform(seq: string): string {
  if (seq.length === 1) {
    const c = seq.charCodeAt(0)
    if (c >= 97 && c <= 122) return String.fromCharCode(c - 96) // a-z → 1-26
    if (c >= 65 && c <= 90) return String.fromCharCode(c - 64)
  }
  // 方向键(ESCAPE 开头)或 Tab:直接返回(许多程序已识别)。
  return seq
}

const showSymbols = ref(false)
const symbols = ['~', '!', '@', '#', '$', '%', '^', '&', '*', '(', ')', '_', '+', '-', '=', '[', ']', '{', '}', '|', '\\', ':', ';', '"', "'", '<', '>', ',', '.', '/', '?']

const isTouch = ref(false)
function detectTouch() {
  isTouch.value = 'ontouchstart' in window || navigator.maxTouchPoints > 0
}
onMounted(detectTouch)
onUnmounted(() => {})

defineExpose({ applySticky })
</script>

<template>
  <!-- mousedown.prevent:阻止按键抢走 xterm 隐藏输入框的焦点,否则一点 Ctrl
       系统软键盘就收起来了,粘滞修饰符也就没法配合软键盘上的字母。 -->
  <div v-if="isTouch" class="term-kbd" @mousedown.prevent>
    <!-- 粘滞修饰符指示灯 -->
    <div class="kbd-row mods">
      <button
        class="kbd mod"
        :class="{ on: stickyCtrl }"
        @click="stickyCtrl = !stickyCtrl"
      >Ctrl</button>
      <button
        class="kbd mod"
        :class="{ on: stickyAlt }"
        @click="stickyAlt = !stickyAlt"
      >Alt</button>
      <button class="kbd" @click="send('\x1b')">Esc</button>
      <button class="kbd" @click="send('\t')">Tab</button>
      <button class="kbd wide" @click="showSymbols = !showSymbols">?#</button>
    </div>

    <div v-if="showSymbols" class="kbd-row symbols">
      <button v-for="s in symbols" :key="s" class="kbd" @click="send(s)">{{ s }}</button>
    </div>

    <div class="kbd-row">
      <div class="arrows">
        <button v-for="a in arrows" :key="a.sym" class="kbd" @click="send(a.code)">{{ a.sym }}</button>
      </div>
      <button class="kbd" @click="send('\x7f')"><n-icon :component="BackspaceOutline" /></button>
      <button class="kbd accent" @click="send('\r')"><n-icon :component="ReturnDownForwardOutline" /></button>
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
.kbd-row {
  display: flex;
  gap: 6px;
  align-items: center;
}
.kbd {
  min-width: 44px;
  min-height: 40px;
  border: 1px solid rgba(127,127,127,.3);
  background: var(--lr-bg);
  color: var(--lr-fg);
  border-radius: 8px;
  font-size: 14px;
  padding: 0 10px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
}
.kbd:active { background: var(--lr-accent); color: #fff; }
.kbd.mod.on { background: var(--lr-accent); color: #fff; border-color: var(--lr-accent); }
.kbd.wide { flex: 1; }
.kbd.accent { background: var(--lr-accent); color: #fff; border-color: var(--lr-accent); }
.arrows { display: flex; gap: 6px; flex: 1; }
.arrows .kbd { flex: 1; }
.symbols { flex-wrap: wrap; }
.symbols .kbd { min-width: 34px; min-height: 36px; padding: 0 6px; }
</style>
