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

// 框线字符(含双线制):ASCII 示意图的特征。
const ART_CHARS = /[─│┌┐└┘├┤┬┴┼═║╔╗╚╝╠╣╦╩╬]/

// renderPreviewMarkdown AskUserQuestion 选项 preview 专用:preview 常见形态是
// 裸 ASCII 框线(没有 markdown 围栏),直接渲染会被当段落 —— 连续行并成一行、
// 行首空格折叠,框图糊掉。这里检测"无围栏但含框线字符"就整体包成代码块,
// 换行与空格在 <pre> 里原样保留。代价:这类内容里混的 markdown 标记会照字面
// 显示,可接受(示意图像素对齐优先于富文本)。
export function renderPreviewMarkdown(src: string): string {
  if (!/(```|~~~)/.test(src) && ART_CHARS.test(src)) {
    return md.render('```\n' + src + '\n```')
  }
  return md.render(src)
}
