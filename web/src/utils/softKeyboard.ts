// 软键盘状态。两类浏览器行为差得很远,这里把差异吃掉:
//
// - Chrome(Android)默认 `interactive-widget=resizes-visual`:键盘弹出只压缩 visual
//   viewport,layout viewport(也就是 innerHeight、100dvh、position:fixed 的参照)一动不动。
//   页面因此完全不知道键盘存在,底部的东西全被压在键盘下面 —— 终端页的键盘条就是这么没了。
//   它还会把 visual viewport 往下挪(offsetTop)去露出焦点元素,挪到底时 layout viewport
//   的下沿正好落在键盘上方 —— 底部那条 position: fixed 的导航就是这么「被键盘顶起来」的:
//   它其实一直没动,是视口挪到了它跟前。
// - Via 之类基于系统 WebView 的浏览器:宿主用 adjustResize 把整个 WebView 变矮,
//   layout viewport 跟着缩,100dvh 自动就对了,但底部固定的导航同样会贴着键盘上沿冒出来。
//   这种浏览器的 visual viewport 和 layout viewport 一样高,上面那套测量全是 0。
//
// 所以对外给两个量,它们不是一回事,来源也不同:
// - open:键盘是不是开着,给「键盘开着就干脆把底部导航收掉」用。两类浏览器都要覆盖:
//   Chrome 看两个 viewport 的高度差(不管挪没挪),WebView 只能看整页比「没键盘时」矮了
//   多少 —— 后者是估算,所以卡得紧一些,且只在触控设备上启用(桌面没有软键盘,
//   拖窗口改高度不该被当成键盘)。
// - inset:layout viewport 下沿被键盘遮住的高度,给「把底部内容顶到键盘上沿」用
//   (终端页的键盘条)。只有 Chrome 那条路会算出非零值 —— WebView 已经整页变矮了,
//   下沿本来就在键盘上面,再补一次就是补两遍。
import { ref, onMounted, onUnmounted } from 'vue'
import { isTouchDevice } from './touch'

// 被键盘遮住的高度(CSS 像素)。同时写进 :root 的 --lr-kb-inset,方便纯 CSS 里用。
const inset = ref(0)
// 键盘是否弹起。
const open = ref(false)

// 地址栏收缩、浏览器 UI 抖动都在几十像素这个量级(Chrome 的地址栏 56px 上下),
// 低于阈值当作没有键盘。
const MIN_GAP = 80
// 整页变矮那条路(WebView)的判定。键盘一般吃掉屏幕三到五成,而浏览器自己的地址栏
// 加底栏也就一成半。取「120px 或两成高度,谁大听谁的」:横屏时屏幕矮、键盘也矮,
// 靠绝对值兜底;竖屏靠比例兜底。
const MIN_SHRINK = 120
const MIN_SHRINK_RATIO = 0.2

const touch = isTouchDevice()
// 「没有键盘时」的 layout viewport 高度。只往大的方向更新:键盘压出来的矮值因此永远
// 不会被当成基线,而地址栏收起让页面变高时基线会跟着涨。宽度一变(转屏、桌面拖窗口)
// 说明这个基线不再可比,作废重来。
let baseH = 0
let baseW = 0

let users = 0

function apply(v: number, on: boolean) {
  open.value = on
  if (inset.value === v) return
  inset.value = v
  document.documentElement.style.setProperty('--lr-kb-inset', `${v}px`)
}

function measure() {
  const vv = window.visualViewport
  const h = window.innerHeight
  if (window.innerWidth !== baseW) {
    baseW = window.innerWidth
    baseH = 0
  }
  if (h > baseH) baseH = h
  // 捏合缩放同样会让 visual viewport 变小,而那不是键盘 —— 下面两个量都会被它污染,
  // 整条路作废,只留 WebView 那条(它不看 visual viewport)。
  const zoom = !vv || (vv.scale || 1) > 1.05
  // visual viewport 比 layout viewport 矮多少 = 键盘 + 浏览器 UI 占掉的部分。
  const gap = zoom ? 0 : h - vv.height
  // 其中真正遮住 layout viewport 下沿的只有 gap 减去视口下移的那段:浏览器把视口
  // 往下挪(offsetTop)时,挪掉的部分露的是页面上方,不是键盘占的,补进来底部会被顶多。
  const hidden = zoom ? 0 : gap - vv.offsetTop
  // 整页比「没键盘时」矮了多少(WebView 那条路唯一的信号)。
  const shrink = baseH - h
  const on = gap > MIN_GAP ||
    (touch && shrink >= Math.max(MIN_SHRINK, baseH * MIN_SHRINK_RATIO))
  // 遮挡高度只在确认键盘开着之后才认:那时任何正的遮挡都值得补(不再过 MIN_GAP 这道
  // 阈值 —— 视口挪了一部分下去的中间状态遮挡可能只剩几十像素,却照样压着键盘条)。
  apply(on && hidden > 0 ? Math.round(hidden) : 0, on)
}

// useSoftKeyboard 返回跟随键盘变化的 { inset, open }。多个组件同时用只会挂一套监听。
export function useSoftKeyboard() {
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
      apply(0, false)
    }
  })
  return { inset, open }
}
