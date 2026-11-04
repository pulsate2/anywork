export interface AgentAttachment {
  path: string
  name: string
}

export interface AgentMessagePart {
  text: string
  attachment?: AgentAttachment
}

export function attachmentMention(path: string): string {
  return `@${/[\s"']/.test(path) ? JSON.stringify(path) : path}`
}

export function composeAgentMessage(body: string, attachments: AgentAttachment[]): string {
  const mentions = attachments.map((attachment) => attachmentMention(attachment.path)).join(' ')
  return [mentions, body].filter(Boolean).join('\n')
}

export function parseAgentMessage(text: string, sessionId?: string): { body: string; attachments: AgentAttachment[] } {
  const parts = splitAgentAttachments(text, sessionId)
  const attachments = parts.flatMap((part) => part.attachment ? [part.attachment] : [])
  let body = parts.filter((part) => !part.attachment).map((part) => part.text).join('')
  // 附件放在首行时,只移除附件与正文之间的换行,保留正文缩进。
  if (parts[0]?.attachment) body = body.replace(/^[ \t]*\r?\n/, '')
  return { body: attachments.length ? body.trimEnd() : body, attachments }
}

// 只识别当前会话的上传目录,普通 @提及和工作区文件路径仍按正文显示。
export function splitAgentAttachments(text: string, sessionId?: string): AgentMessagePart[] {
  if (!sessionId) return [{ text }]
  const parts: AgentMessagePart[] = []
  // 旧版上传把未加引号的路径放在行尾(多个文件用空格 + @ 分隔),
  // 因此也保留文件名中的空格;新版对含空格的路径加引号。
  const mentions = /@(?:"(?:\\.|[^"\\])*"|'[^'\r\n]*'|[^\s"'<>]+(?:[ \t]+(?!@)[^\s"'<>]+)*)/g
  let start = 0
  for (const match of text.matchAll(mentions)) {
    let path = match[0].slice(1)
    if (path.startsWith('"')) {
      try { path = JSON.parse(path) } catch { continue }
    } else if (path.startsWith("'")) {
      path = path.slice(1, -1)
    }
    const normalized = path.replace(/\\/g, '/')
    const prefix = `/agent-files/${sessionId}/`
    const index = normalized.lastIndexOf(prefix)
    if (index < 0) continue
    const name = normalized.slice(index + prefix.length)
    if (!name || name === '.' || name === '..' || name.includes('/')) continue
    if (match.index! > start) parts.push({ text: text.slice(start, match.index) })
    parts.push({ text: name, attachment: { path, name } })
    start = match.index! + match[0].length
  }
  if (start < text.length) parts.push({ text: text.slice(start) })
  return parts.length ? parts : [{ text }]
}
