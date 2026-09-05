// 触控设备判定。终端页有两处要用它:键盘条自己决定要不要渲染,工具栏决定要不要给
// 那个显隐开关 —— 两处必须同源,判得不一样就会出现「有开关但没有条」或者反过来。
// 结果在页面生命周期内不变(这不是断点,与窗口宽度无关),取一次即可,不用做成响应式。
export function isTouchDevice(): boolean {
  return 'ontouchstart' in window || navigator.maxTouchPoints > 0
}
