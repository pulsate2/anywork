import { api, type AgentToolCall } from '@/api/client'

export function isViewImageTool(tool: string): boolean {
  return /(?:^|[._:/])view_?image$/i.test(tool)
}

// Only local file references are rewritten. External URLs, anchors and app API
// links keep their existing meaning. Never allow javascript:/data: as files.
export function agentLocalPath(href: string): string | null {
  const raw = href.trim()
  let path = raw
  if (/^sandbox:/i.test(raw)) {
    path = raw.slice(8)
    if (path.startsWith('//')) return null
    try { path = decodeURIComponent(path) } catch { return null }
  } else if (/^file:/i.test(raw)) {
    try {
      const url = new URL(raw)
      if (url.hostname && url.hostname.toLowerCase() !== 'localhost') return null
      path = decodeURIComponent(url.pathname)
      if (/^\/[a-z]:\//i.test(path)) path = path.slice(1)
    } catch { return null }
  } else {
    if (raw.startsWith('//') || raw.startsWith('#') || raw.startsWith('?') || raw.startsWith('/api/')) return null
    if (!/^[a-z]:[\\/]/i.test(raw) && /^[a-z][\w+.-]*:/i.test(raw)) return null
    try { path = decodeURIComponent(raw) } catch { return null }
  }
  // Source links often include a line anchor, which is not part of the filename.
  path = path.replace(/#L\d+(?:[-:]L?\d+)?$/i, '')
  return path && !/[\x00\r\n]/.test(path) ? path : null
}

export function agentToolImageUrls(call: AgentToolCall | null, sessionId?: string): string[] {
  if (!call || !sessionId) return []
  if (call.images?.length) return [...new Set(call.images.map(name => api.agentFileUrl(sessionId, name)))]
  if (!isViewImageTool(call.tool) || call.state === 'error') return []
  try {
    const args = JSON.parse(call.args || '{}')
    for (const key of ['path', 'file_path', 'image_path', 'imagePath', 'local_image_path', 'localImagePath']) {
      if (typeof args?.[key] === 'string' && args[key]) {
        return [api.agentArtifactUrl(sessionId, args[key], true)]
      }
    }
  } catch { /* Invalid/truncated arguments cannot identify a local image. */ }
  return []
}
