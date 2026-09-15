// 用 n-time-picker 当"时长选择器"用:它的值本是时刻(时间戳),format="HH:mm"
// 时面板显示的恰好是本地时区的时:分 —— 拿它当时长读,任意分钟就都能选了。
// 换算必须走本地时区(getHours 而不是 getUTCHours):值是"今天 01:15"这种,
// UTC 读出来就是另一个数了。这个坑钉在 __tests__/duration.spec.ts 里。
export function durToPickerMs(minutes: number): number {
  const d = new Date()
  d.setHours(Math.floor(minutes / 60), minutes % 60, 0, 0)
  return d.getTime()
}

// 反向换算;null(清空)归 0 = 不定时。上限 23:59 = 1439 分钟,由选择器天然决定。
export function pickerMsToDur(ms: number | null): number {
  if (ms == null) return 0
  const d = new Date(ms)
  return d.getHours() * 60 + d.getMinutes()
}
