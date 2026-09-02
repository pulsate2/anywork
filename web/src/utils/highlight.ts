// 基于 highlight.js 的轻量语法高亮工具。
// 按需注册常用语言(控制打包体积),扩展名 → 语言映射,返回安全 HTML(v-html)。
// 主题色不引 hljs 自带 CSS,而是由组件里按 --lr-* 令牌自定义(浅/深主题皆适配)。

import hljs from 'highlight.js/lib/core'
// LanguageFn 类型只在顶层包导出,lib/core 只 re-export 默认值。
import type { LanguageFn } from 'highlight.js'

// 注册最常用的语言;新增语言在这里 append 并同步注册即可。
import javascript from 'highlight.js/lib/languages/javascript'
import typescript from 'highlight.js/lib/languages/typescript'
import json from 'highlight.js/lib/languages/json'
import xml from 'highlight.js/lib/languages/xml'
import css from 'highlight.js/lib/languages/css'
import scss from 'highlight.js/lib/languages/scss'
import bash from 'highlight.js/lib/languages/bash'
import shell from 'highlight.js/lib/languages/shell'
import go from 'highlight.js/lib/languages/go'
import python from 'highlight.js/lib/languages/python'
import java from 'highlight.js/lib/languages/java'
import markdown from 'highlight.js/lib/languages/markdown'
import yaml from 'highlight.js/lib/languages/yaml'
import ini from 'highlight.js/lib/languages/ini' // 也覆盖 TOML(v11 无独立 toml 语言)
import rust from 'highlight.js/lib/languages/rust'
import c from 'highlight.js/lib/languages/c'
import cpp from 'highlight.js/lib/languages/cpp'
import csharp from 'highlight.js/lib/languages/csharp'
import sql from 'highlight.js/lib/languages/sql'
import php from 'highlight.js/lib/languages/php'
import ruby from 'highlight.js/lib/languages/ruby'
import dockerfile from 'highlight.js/lib/languages/dockerfile'
import diff from 'highlight.js/lib/languages/diff'
import plaintext from 'highlight.js/lib/languages/plaintext'

const langs: Record<string, LanguageFn> = {
  javascript, typescript, json, xml, css, scss, bash, shell, go, python,
  java, markdown, yaml, ini, rust, c, cpp, csharp, sql, php, ruby,
  dockerfile, diff, plaintext,
}
for (const name of Object.keys(langs)) {
  hljs.registerLanguage(name, langs[name])
}

// 扩展名/文件名 → hljs 语言名。命中不了回退空(hljs 自动探测)。
const extMap: Record<string, string> = {
  'js': 'javascript', 'mjs': 'javascript', 'cjs': 'javascript', 'jsx': 'javascript',
  'ts': 'typescript', 'mts': 'typescript', 'cts': 'typescript', 'tsx': 'typescript',
  'json': 'json', 'jsonc': 'json',
  'html': 'xml', 'htm': 'xml', 'xml': 'xml', 'svg': 'xml', 'vue': 'xml',
  'css': 'css', 'scss': 'scss', 'sass': 'scss', 'less': 'css',
  'sh': 'bash', 'bash': 'bash', 'zsh': 'bash',
  'go': 'go',
  'py': 'python', 'pyw': 'python',
  'java': 'java',
  'md': 'markdown', 'markdown': 'markdown',
  'yml': 'yaml', 'yaml': 'yaml',
  'toml': 'ini',
  'ini': 'ini', 'conf': 'ini', 'cfg': 'ini', 'properties': 'ini',
  'rs': 'rust',
  'c': 'c', 'h': 'c',
  'cpp': 'cpp', 'cc': 'cpp', 'cxx': 'cpp', 'hpp': 'cpp', 'hh': 'cpp',
  'cs': 'csharp',
  'sql': 'sql',
  'php': 'php',
  'rb': 'ruby',
  'Dockerfile': 'dockerfile',
  'diff': 'diff', 'patch': 'diff',
}

// 从路径里取语言名;取不到返回 ''(由 hljs 自动探测)。
export function langFromPath(path: string): string {
  const base = path.split('/').pop() || path
  if (extMap[base]) return extMap[base] // 可能是 Dockerfile 这样的无扩展名精确名
  const dot = base.lastIndexOf('.')
  if (dot <= 0) return ''
  const ext = base.slice(dot + 1).toLowerCase()
  return extMap[ext] || ''
}

// 高亮 content 并返回"安全"HTML;失败/空串时返回纯转义文本兜底(v-html 里显示原样)。
export function highlightCode(content: string, path: string): string {
  const lang = langFromPath(path)
  if (content === '') return hljs.highlight('', { language: 'plaintext' }).value
  try {
    const result = lang
      ? hljs.highlight(content, { language: lang })
      : hljs.highlightAuto(content)
    return result.value
  } catch {
    return escapeHtml(content)
  }
}

function escapeHtml(s: string): string {
  return s.replace(/[&<>"']/g, (c) =>
    ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c] || c))
}