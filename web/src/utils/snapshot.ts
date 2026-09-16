// 快照名的解析与展示。名前缀是后端的 backup-YYYYMMDD-HHMMSS.tar.gz,
// 时刻按本地时区写进去的(internal/backup/runner.go 用 time.Now().Format),
// 所以这里也必须按本地时区读回来 —— 用 Date.UTC 或 getUTC* 会整体偏掉一个时差。

/** 拆出快照时刻。名字不符合后端规则(别人放的文件)时返回 null,由调用方决定怎么显示。 */
export function snapDate(name: string): Date | null {
  const m = /^backup-(\d{4})(\d{2})(\d{2})-(\d{2})(\d{2})(\d{2})/.exec(name)
  if (!m) return null
  const d = new Date(+m[1], +m[2] - 1, +m[3], +m[4], +m[5], +m[6])
  return isNaN(d.getTime()) ? null : d
}

/** 显示用时刻。今年的省掉年份:列表里绝大多数都是近期的,省下的宽度留给操作。 */
export function snapTime(name: string, now = new Date()): string {
  const d = snapDate(name)
  if (!d) return name
  const p = (n: number) => String(n).padStart(2, '0')
  const md = `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
  return d.getFullYear() === now.getFullYear() ? md : `${d.getFullYear()}-${md}`
}

/** 相对时间,只在近一个月内有用;再往前"多少天前"不如直接看时刻。 */
export function snapRel(name: string, now = new Date()): string {
  const d = snapDate(name)
  if (!d) return ''
  const min = Math.floor((now.getTime() - d.getTime()) / 60000)
  if (min < 0) return '' // 服务器与客户端的钟差,不硬编成"刚刚"
  if (min < 1) return '刚刚'
  if (min < 60) return `${min} 分钟前`
  const hr = Math.floor(min / 60)
  if (hr < 24) return `${hr} 小时前`
  const day = Math.floor(hr / 24)
  return day < 30 ? `${day} 天前` : ''
}
