// Markdown 渲染:只读预览用。
// html: false 让内联 HTML 被转义,配合 markdown-it 内置的 validateLink
// (拦掉 javascript:/vbscript:/file:/data: 链接),不需要再引 sanitizer。
import MarkdownIt from 'markdown-it'
import { highlightLang } from './highlight'

const md = new MarkdownIt({
  html: false,
  linkify: true,
  typographer: false,
  // 返回值以 <pre 开头时 markdown-it 不再套自己的外壳。
  highlight: (code, lang) => `<pre class="md-pre"><code>${highlightLang(code, lang)}</code></pre>`,
})

// 外链开新标签:预览里点链接不该把整个 SPA 导航走。
const linkOpen = md.renderer.rules.link_open
md.renderer.rules.link_open = (tokens, idx, options, env, self) => {
  tokens[idx].attrSet('target', '_blank')
  tokens[idx].attrSet('rel', 'noopener noreferrer')
  return linkOpen ? linkOpen(tokens, idx, options, env, self) : self.renderToken(tokens, idx, options)
}

export function renderMarkdown(src: string): string {
  return md.render(src)
}
