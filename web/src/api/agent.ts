// Agent 推送通道客户端:/api/agent 的 WS 是服务端单向推送,客户端帧只有
// subscribe/unsubscribe。这里管三件事:自动重连、断线后重订阅、seq 去重交给视图
// (重连后视图用 afterSeq 增量补差,推送与补差重合时按 seq 丢重复)。
import type { AgentEvent } from './client'

export type AgentWSEvent =
  | { type: 'open' } // 连接建立(含重连,视图据此补差)
  | { type: 'close' }
  | { type: 'message'; event: AgentEvent }
  | { type: 'exit'; sessionId: string }
  // 列表观察:任意会话的状态变化(running/idle/dead,title 顺带同步)
  | { type: 'session'; sessionId: string; status: 'running' | 'idle' | 'dead'; title?: string }

const RECONNECT_BASE = 1000
const RECONNECT_MAX = 15000

export class AgentWS {
  private ws: WebSocket | null = null
  private onEvent: (e: AgentWSEvent) => void
  // 订阅集合:连接断开重连后按它重发 subscribe。
  private subs = new Set<string>()
  // 列表观察:连接建立后发 watch,收全部会话的状态变化。
  private watching = false
  private closed = false
  private retries = 0
  private timer: number | null = null

  constructor(onEvent: (e: AgentWSEvent) => void) {
    this.onEvent = onEvent
  }

  get connected(): boolean {
    return this.ws !== null && this.ws.readyState === WebSocket.OPEN
  }

  connect() {
    this.closed = false
    if (this.timer !== null) {
      clearTimeout(this.timer)
      this.timer = null
    }
    this.open()
  }

  private open() {
    const old = this.ws
    this.ws = null
    old?.close()
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
    const ws = new WebSocket(`${proto}//${location.host}/api/agent`)
    this.ws = ws

    ws.onopen = () => {
      if (this.ws !== ws) return
      this.retries = 0
      for (const id of this.subs) this.send({ type: 'subscribe', sessionId: id })
      if (this.watching) this.send({ type: 'watch' })
      this.onEvent({ type: 'open' })
    }
    ws.onclose = () => {
      if (this.ws !== ws) return
      this.ws = null
      this.onEvent({ type: 'close' })
      this.scheduleReconnect()
    }
    ws.onmessage = (ev) => {
      if (typeof ev.data !== 'string') return
      let msg: AgentEvent
      try {
        msg = JSON.parse(ev.data)
      } catch {
        return
      }
      if (!msg || typeof msg.kind !== 'string') return
      // exit 不是落库消息(Manager 在进程退出时直接广播),单独分流。
      if (msg.kind === 'exit') this.onEvent({ type: 'exit', sessionId: msg.sessionId })
      else if (msg.kind === 'session_status') {
        // 会话状态变化(只推给列表观察者):列表据此实时刷新。
        const p = msg.payload as { status?: 'running' | 'idle' | 'dead'; title?: string } | null
        if (p?.status) {
          this.onEvent({ type: 'session', sessionId: msg.sessionId, status: p.status, title: p.title })
        }
      } else this.onEvent({ type: 'message', event: msg })
    }
  }

  private scheduleReconnect() {
    if (this.closed) return
    // 指数退避:弱网/服务重启时别把服务器打爆。
    const delay = Math.min(RECONNECT_BASE * 2 ** this.retries, RECONNECT_MAX)
    this.retries++
    this.timer = window.setTimeout(() => { this.timer = null; this.open() }, delay)
  }

  private send(payload: unknown) {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) return
    this.ws.send(JSON.stringify(payload))
  }

  subscribe(sessionId: string) {
    this.subs.add(sessionId)
    this.send({ type: 'subscribe', sessionId })
  }

  unsubscribe(sessionId: string) {
    this.subs.delete(sessionId)
    this.send({ type: 'unsubscribe', sessionId })
  }

  // 观察全部会话的状态变化(会话列表实时刷新);重连后自动重发。
  watch() {
    this.watching = true
    this.send({ type: 'watch' })
  }

  unwatch() {
    this.watching = false
    this.send({ type: 'unwatch' })
  }

  close() {
    this.closed = true
    if (this.timer !== null) {
      clearTimeout(this.timer)
      this.timer = null
    }
    this.subs.clear()
    this.ws?.close()
    this.ws = null
  }
}
