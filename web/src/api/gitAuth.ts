// git 交互认证 WS 客户端。封装 /api/git/auth 长连接:
// 服务端在 git push/pull 需要账号密码时,经这条连接推 { token, prompt, field }。
// 前端收到后弹输入框,填完 POST /api/git/auth/answer(见 api.gitAuthAnswer)回填。

export interface GitAskEvent {
  token: string
  prompt: string
  field: 'username' | 'password'
}

export type GitAuthEvent =
  | { type: 'open' }
  | { type: 'close' }
  | { type: 'ask'; ask: GitAskEvent }

export class GitAuthClient {
  private ws: WebSocket | null = null
  private onEvent: (e: GitAuthEvent) => void

  constructor(onEvent: (e: GitAuthEvent) => void) {
    this.onEvent = onEvent
  }

  connect() {
    if (this.ws && this.ws.readyState === WebSocket.OPEN) return
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
    const ws = new WebSocket(`${proto}//${location.host}/api/git/auth`)
    // 精简:明文 JSON 事件,无需二进制。
    this.ws = ws
    ws.onopen = () => this.onEvent({ type: 'open' })
    ws.onclose = () => {
      this.ws = null
      this.onEvent({ type: 'close' })
    }
    ws.onmessage = (ev) => {
      try {
        const data = JSON.parse(ev.data)
        if (data && typeof data.token === 'string') {
          const ask: GitAskEvent = {
            token: data.token,
            prompt: typeof data.prompt === 'string' ? data.prompt : '',
            field: data.field === 'password' ? 'password' : 'username',
          }
          this.onEvent({ type: 'ask', ask })
        }
      } catch { /* 忽略无法解析的帧 */ }
    }
  }

  close() {
    this.ws?.close()
    this.ws = null
  }
}