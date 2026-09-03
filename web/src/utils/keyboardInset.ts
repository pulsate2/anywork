// 软键盘遮挡高度。两种浏览器行为差得很远,这里把差异吃掉:
//
// - Chrome(Android)默认 `interactive-widget=resizes-visual`:键盘弹出只压缩 visual
//   viewport,layout viewport(也就是 innerHeight、100dvh、position:fixed 的参照)一动不动。
//   页面因此完全不知道键盘存在,底部的东西全被压在键盘下面 —— 终端页的键盘条就是这么没了。
// - Via 之类基于系统 WebView 的浏览器:宿主用 adjustResize 把整个 WebView 变矮,
//   layout viewport 跟着缩,100dvh 自动就对了。
//
// 所以只测「layout viewport 底部到 visual viewport 底部的距离」:
// Chrome 下它等于键盘高度,WebView 下它天然是 0 —— 同一份代码在两边都对,
// 不用去嗅探浏览器,也不用改 meta viewport(改了会让 Chrome 也进入整页 resize 那条路)。
import { ref, onMounted, onUnmounted } from 'vue'

// 遮挡高度(CSS 像素)。同时写进 :root 的 --lr-kb-inset,方便纯 CSS 里用。
const inset = ref(0)

// 地址栏收缩、浏览器 UI 抖动都在几十像素这个量级,低于阈值当作没有键盘。
const MIN_INSET = 80

let users = 0

function apply(v: number) {
  if (inset.value === v) return
  inset.value = v
  document.documentElement.style.setProperty('--lr-kb-inset', `${v}px`)
}

function measure() {
  const vv = window.visualViewport
  if (!vv) return apply(0)
  // offsetTop:键盘弹出时浏览器可能把 visual viewport 往下挪来露出焦点元素,
  // 那部分不是键盘占的,要减掉,否则底部会被顶多。
  const raw = window.innerHeight - vv.height - vv.offsetTop
  apply(raw > MIN_INSET ? Math.round(raw) : 0)
}

// useKeyboardInset 返回一个跟随键盘变化的 ref。多个组件同时用只会挂一套监听。
export function useKeyboardInset() {
  onMounted(() => {
    if (users++ === 0) {
      window.visualViewport?.addEventListener('resize', measure)
      window.visualViewport?.addEventListener('scroll', measure)
      window.addEventListener('resize', measure)
    }
    measure()
  })
  onUnmounted(() => {
    if (--users === 0) {
      window.visualViewport?.removeEventListener('resize', measure)
      window.visualViewport?.removeEventListener('scroll', measure)
      window.removeEventListener('resize', measure)
      apply(0)
    }
  })
  return inset
}
