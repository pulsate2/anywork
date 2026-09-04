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
  // 实际生效的资源上限,缺省/0 = 该项没限。limitMode 是所用机制(cgroup2/rlimit/job)。
  memoryMB?: number
  cpuPercent?: number
  limitMode?: string
}

// TermLimits 新建会话时申请的上限,0 = 不限。
export interface TermLimits {
  memoryMB: number
  cpuPercent: number
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
  // 等着"随便来一帧"的探针回调,见 probe()。
  private probeWaiters = new Set<(ok: boolean) => void>()

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
    // 重连时先把旧 socket 摘掉:它的 onclose 是异步来的,晚一步就会把新连接刚建好的
    // 状态又清成断开。
    this.close()
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
        // 只有还是"当前"那条连接才算数:上一条被替换掉的 socket 关闭与外界无关。
        if (this.ws !== ws) return
        this.ws = null
        this.current = null
        this.flushProbes(false)
        this.onEvent({ type: 'close' })
      }
      ws.onerror = () => {
        reject(new Error('终端连接失败'))
        if (this.ws !== ws) return
        this.onEvent({ type: 'error', message: '终端连接失败' })
      }
      ws.onmessage = (ev) => {
        this.flushProbes(true)
        this.handleMessage(ev.data)
      }
    })
  }

  close() {
    const ws = this.ws
    this.ws = null
    this.current = null
    this.flushProbes(false)
    ws?.close()
  }

  // probe 发一个只读的 list 帧当探针,等任意回帧。
  // 手机切后台被断网后,socket 常常停在半开状态:readyState 还是 OPEN,send() 也不报错,
  // 但帧再也到不了对端 —— 表现就是终端毫无反应。只能靠"发出去有没有回音"来判定。
  // 返回 false 表示这条连接已经不通(或本来就没连上),该重连了。
  probe(timeoutMs = 3000): Promise<boolean> {
    if (!this.connected) return Promise.resolve(false)
    return new Promise<boolean>((resolve) => {
      const waiter = (ok: boolean) => {
        clearTimeout(timer)
        this.probeWaiters.delete(waiter)
        resolve(ok)
      }
      const timer = setTimeout(() => waiter(false), timeoutMs)
      this.probeWaiters.add(waiter)
      this.list()
    })
  }

  private flushProbes(ok: boolean) {
    if (!this.probeWaiters.size) return
    for (const w of [...this.probeWaiters]) w(ok)
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

  createSession(dir: string, shell: string, cols: number, rows: number, limits?: TermLimits) {
    this.send({
      type: 'create',
      dir,
      shell,
      cols,
      rows,
      memoryMB: limits?.memoryMB ?? 0,
      cpuPercent: limits?.cpuPercent ?? 0,
    })
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
