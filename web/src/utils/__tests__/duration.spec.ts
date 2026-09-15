// 时长 ↔ n-time-picker 时刻值的换算。这层最大的坑是时区:值是"今天 HH:mm"的
// 时间戳,本地时区和 UTC 读出来的时:分不一样 —— 钉住本地时区读法,谁改成
// getUTCHours 这里立刻红。
import { describe, it, expect } from 'vitest'
import { durToPickerMs, pickerMsToDur } from '@/utils/duration'

describe('时长 ↔ 选择器时刻换算', () => {
  it('往返一致:纯分钟、跨小时、满档 23:59', () => {
    for (const min of [1, 30, 45, 60, 75, 615, 1439]) {
      expect(pickerMsToDur(durToPickerMs(min)), `${min} 分钟`).toBe(min)
    }
  })

  it('选择器面板显示的 HH:mm 就是时长(本地时区,不是 UTC)', () => {
    const d = new Date(durToPickerMs(75))
    const hh = String(d.getHours()).padStart(2, '0')
    const mm = String(d.getMinutes()).padStart(2, '0')
    expect(`${hh}:${mm}`).toBe('01:15')
  })

  it('null(清空)归 0 = 不定时', () => {
    expect(pickerMsToDur(null)).toBe(0)
  })
})
