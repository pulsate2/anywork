// 快照名的解析与展示。两处必须钉住:
// 1. 时刻按本地时区读(名字是后端用 time.Now().Format 写的本地时刻),
//    改成 UTC 读法整体偏一个时差 —— 和 duration.spec.ts 是同一类坑;
// 2. 名字不合规(别人放进目录的文件)时原样返回,不能显示成 1970 或 Invalid Date。
import { describe, it, expect } from 'vitest'
import { snapDate, snapTime, snapRel } from '@/utils/snapshot'

describe('快照名 → 时刻', () => {
  it('按本地时区读,不是 UTC', () => {
    const d = snapDate('backup-20260916-203352.tar.gz')!
    expect([d.getFullYear(), d.getMonth() + 1, d.getDate()]).toEqual([2026, 9, 16])
    expect([d.getHours(), d.getMinutes(), d.getSeconds()]).toEqual([20, 33, 52])
  })

  it('今年的省年份,往年的带年份 —— 宽度留给操作', () => {
    const now = new Date(2026, 8, 16, 12, 0, 0)
    expect(snapTime('backup-20260916-203352.tar.gz', now)).toBe('09-16 20:33:52')
    expect(snapTime('backup-20250101-000000.tar.gz', now)).toBe('2025-01-01 00:00:00')
  })

  it('拆不动就原样返回,不猜别人的命名', () => {
    expect(snapTime('说明.txt')).toBe('说明.txt')
    expect(snapDate('backup-2026.tar.gz')).toBeNull()
  })
})

describe('相对时间', () => {
  const now = new Date(2026, 8, 16, 20, 33, 52)

  it('分钟/小时/天三档', () => {
    expect(snapRel('backup-20260916-203351.tar.gz', now)).toBe('刚刚')
    expect(snapRel('backup-20260916-201352.tar.gz', now)).toBe('20 分钟前')
    expect(snapRel('backup-20260916-183352.tar.gz', now)).toBe('2 小时前')
    expect(snapRel('backup-20260914-203352.tar.gz', now)).toBe('2 天前')
  })

  it('超过一个月不显示:天数已经不如上面的日期直观', () => {
    expect(snapRel('backup-20250101-000000.tar.gz', now)).toBe('')
  })

  it('客户端的钟比服务器慢时不编"刚刚"', () => {
    expect(snapRel('backup-20260916-210000.tar.gz', now)).toBe('')
  })
})
