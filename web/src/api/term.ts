// 终端长连接客户端。封装 /api/term 的帧协议。
// 帧格式:
//   客户端→服务端:纯 JSON 文本帧,无类型前缀({ type: 'create'|'attach'|'input'|'resize'|'kill'|'list'|'detach', ... })
//   服务端→客户端:二进制帧首字节 'o' 输出;文本帧首字节 't'error / 's'session / 'e'exit

export interface TermSummary {
  id: string
  dir: string
  cols: number
  rows: number
  createdAt: string
  dead: boolean
  exitCode: number
}

export type TermEvent =
  | { type: 'open' }
  | { type: 'close' }
  | { type: 'error'; message: string }
  | { type: 'output'; data: Uint8Array }
  | { type: 'session'; session: TermSummary }
  | { type: 'sessionList'; list: TermSummary[] }
  | { type: 'exit'; id: string; exitCode: number }

export class TermClient {
  private ws: WebSocket | null = null
  private onEvent: (e: TermEvent) => void
  private current: string | null = null

  constructor(onEvent: (e: TermEvent) => void) {
    this.onEvent = onEvent
  }

  get connected(): boolean {
    return this.ws !== null && this.ws.readyState === WebSocket.OPEN
  }

  get currentSession(): string | null {
    return this.current
  }

  connect(): Promise<void> {
    return new Promise((resolve, reject) => {
      const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
      const ws = new WebSocket(`${proto}//${location.host}/api/term`)
      // 二进制输出帧以 ArrayBuffer 接收(xterm 需要 Uint8Array)。
      ws.binaryType = 'arraybuffer'
      this.ws = ws

      ws.onopen = () => {
        this.onEvent({ type: 'open' })
        resolve()
      }
      ws.onclose = () => {
        this.current = null
        this.onEvent({ type: 'close' })
      }
      ws.onerror = (ev) => {
        reject(new Error('终端连接失败'))
        this.onEvent({ type: 'error', message: '终端连接失败' })
      }
      ws.onmessage = (ev) => this.handleMessage(ev.data)
    })
  }

  close() {
    this.ws?.close()
    this.ws = null
  }

  // 服务端 readLoop 只接受"纯 JSON 文本帧"(二进制帧直接丢弃,也不吃类型前缀字节),
  // 所以这里必须原样发 JSON 字符串。
  private send(payload: unknown) {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) return
    this.ws.send(JSON.stringify(payload))
  }

  private handleMessage(data: any) {
    // 区分二进制帧(输出)与文本帧(JSON 事件)。
    if (data instanceof ArrayBuffer) {
      const buf = new Uint8Array(data)
      if (buf.length === 0) return
      const type = String.fromCharCode(buf[0])
      if (type === 'o') {
        this.onEvent({ type: 'output', data: buf.slice(1) })
      }
      return
    }
    if (typeof data === 'string') {
      // WebSocket 文本帧:首字节类型 + JSON。
      const type = data[0]
      let payload: any = {}
      try {
        payload = JSON.parse(data.slice(1))
      } catch {
        return
      }
      switch (type) {
        case 't':
          this.onEvent({ type: 'error', message: payload.message })
          break
        case 'e':
          this.onEvent({ type: 'exit', id: payload.id, exitCode: payload.exitCode })
          break
        case 's':
          if (payload.type === 'sessionList') {
            // 兜底成数组:list 缺失时下游 sessions.length / .find 会整页崩掉。
            this.onEvent({ type: 'sessionList', list: payload.list ?? [] })
          } else if (payload.type === 'session') {
            // create 也会回这一帧;服务端已把连接附加到该会话,这里同步 current,
            // 否则 input/resize 因 current 为空而被静默丢弃。
            this.current = payload.id
            this.onEvent({ type: 'session', session: payload as TermSummary })
          }
          break
      }
    }
  }

  createSession(dir: string, shell: string, cols: number, rows: number) {
    this.send({ type: 'create', dir, shell, cols, rows })
  }
  attach(id: string) {
    this.current = id
    this.send({ type: 'attach', sid: id })
  }
  detach() {
    if (this.current) this.send({ type: 'detach' })
    this.current = null
  }
  input(data: string) {
    if (!this.current) return
    this.send({ type: 'input', sid: this.current, data })
  }
  resize(cols: number, rows: number) {
    if (!this.current) return
    this.send({ type: 'resize', sid: this.current, cols, rows })
  }
  kill(id: string) {
    this.send({ type: 'kill', sid: id })
  }
  list() {
    this.send({ type: 'list' })
  }
}
