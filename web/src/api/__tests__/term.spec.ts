// TermClient 的帧形状:定时关闭的字段名/单位(分钟)是前后端共同的约定,
// 写错名字服务端只会当 0 处理 —— 界面不报错、定时也不生效,只有测试能钉住。
// happy-dom 没有可用的 WebSocket,这里用一个只记录 send 的桩。
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { TermClient, type TermEvent } from '@/api/term'

class FakeWS {
  // TermClient 用裸 WebSocket.OPEN 判连,桩上得有这个常量。
  static readonly OPEN = 1
  static last: FakeWS | null = null
  readyState = FakeWS.OPEN
  sent: string[] = []
  binaryType = ''
  constructor() { FakeWS.last = this }
  send(data: string) { this.sent.push(data) }
  close() { this.readyState = 3 }
  // 手动派发服务端文本帧:首字节类型 + JSON(与真实服务端一致)。
  emit(prefix: string, payload: unknown) {
    this.onmessage?.({ data: prefix + JSON.stringify(payload) } as MessageEvent)
  }
  onmessage: ((ev: MessageEvent) => void) | null = null
  onopen: (() => void) | null = null
  onclose: (() => void) | null = null
  onerror: (() => void) | null = null
}

vi.stubGlobal('WebSocket', FakeWS as unknown as typeof WebSocket)

function connectedClient(): { client: TermClient; ws: FakeWS; events: TermEvent[] } {
  const events: TermEvent[] = []
  const client = new TermClient((e) => events.push(e))
  client.connect()
  FakeWS.last!.onopen!()
  return { client, ws: FakeWS.last!, events }
}

describe('TermClient 定时关闭帧', () => {
  beforeEach(() => { FakeWS.last = null })

  it('createSession 封装:档位随 create 帧发出,分钟为值', () => {
    const { client, ws } = connectedClient()
    client.createSession('/', 'bash', 80, 24, undefined, 30)
    expect(JSON.parse(ws.sent.at(-1)!)).toMatchObject({ type: 'create', autoCloseMin: 30 })
  })

  it('createSession 不给档位时是显式的 0,不是 undefined', () => {
    const { client, ws } = connectedClient()
    client.createSession('/', '', 80, 24)
    expect(JSON.parse(ws.sent.at(-1)!).autoCloseMin).toBe(0)
  })

  it('setAutoClose 发 autoclose 帧,sid + 分钟;0 是取消', () => {
    const { client, ws } = connectedClient()
    client.setAutoClose('s-1', 60)
    expect(JSON.parse(ws.sent.at(-1)!)).toEqual({ type: 'autoclose', sid: 's-1', autoCloseMin: 60 })
    // 0 = 取消,帧里仍然是显式的 0:字段名一旦写错,服务端 omitempty 会把
    // undefined 吞掉、0 却能显式传过去,这里的断言就是防这一层。
    client.setAutoClose('s-1', 0)
    expect(JSON.parse(ws.sent.at(-1)!)).toEqual({ type: 'autoclose', sid: 's-1', autoCloseMin: 0 })
  })

  it('exit 帧的 reason 透传成事件字段', () => {
    const { client, ws, events } = connectedClient()
    client.attach('s-1')
    ws.emit('e', { type: 'exit', id: 's-1', exitCode: 143, reason: 'autoclose' })
    expect(events.at(-1)).toMatchObject({ type: 'exit', id: 's-1', reason: 'autoclose' })
    // 手动结束不带 reason。
    ws.emit('e', { type: 'exit', id: 's-1', exitCode: 0 })
    expect(events.at(-1)).toMatchObject({ type: 'exit', exitCode: 0 })
    expect((events.at(-1) as any).reason).toBeUndefined()
  })

  it('sessionList 里的 autoCloseAt 原样透传', () => {
    const { client, ws, events } = connectedClient()
    client.list()
    ws.emit('s', { type: 'sessionList', list: [{ id: 's-1', autoCloseAt: '2026-09-15T12:00:00Z' }] })
    expect(events.at(-1)).toMatchObject({
      type: 'sessionList',
      list: [{ id: 's-1', autoCloseAt: '2026-09-15T12:00:00Z' }],
    })
  })
})
