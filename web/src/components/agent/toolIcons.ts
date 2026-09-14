// hapi ToolCard/icons.tsx 同款的手写 stroke SVG,Vue 函数式组件版:
// 分类图标(读=眼/搜=放大镜/写=折角文件/命令=终端/网页=地球/兜底=扳手)
// 与状态图标(完成=绿圈勾/出错=红圈叉/等审批=挂锁/运行中=spinner)。
// 颜色走 currentColor 跟随文字色,尺寸由使用处的 class 控制(1em 系)。
// spinner 的旋转动画在 assets/main.css(.tool-icon-spin)。
import { h, type FunctionalComponent } from 'vue'

type SVGChild = ReturnType<typeof h>

// 24×24 lucide 风格(hapi createIcon 同参:stroke 1.8、圆角端点)。
const svg24 = (paths: SVGChild[]): FunctionalComponent =>
  ((_props, { attrs }) =>
    h('svg', {
      viewBox: '0 0 24 24', fill: 'none',
      stroke: 'currentColor', 'stroke-width': 1.8,
      'stroke-linecap': 'round', 'stroke-linejoin': 'round',
      'aria-hidden': 'true',
      ...attrs,
    }, paths)) as FunctionalComponent

const p = (d: string) => h('path', { d })
const c = (cx: number, cy: number, r: number) => h('circle', { cx, cy, r })

// 命令:终端窗口 + 提示符
export const TerminalIcon = svg24([
  h('rect', { x: 3, y: 4, width: 18, height: 16, rx: 2 }),
  p('M7 9l3 3-3 3'),
  p('M11 15h6'),
])
// 搜索:放大镜
export const SearchIcon = svg24([c(11, 11, 6), p('M20 20l-3.5-3.5')])
// 读取:眼睛
export const EyeIcon = svg24([
  p('M2.5 12s3.5-7 9.5-7 9.5 7 9.5 7-3.5 7-9.5 7-9.5-7-9.5-7z'),
  c(12, 12, 2.5),
])
// 写入:折角文件 + 横线
export const FileDiffIcon = svg24([
  p('M14 2H7a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V7z'),
  p('M14 2v5h5'),
  p('M9 12h2'),
  p('M9 16h6'),
  p('M13 12h2'),
])
// 网页:地球
export const GlobeIcon = svg24([
  c(12, 12, 9),
  p('M3 12h18'),
  p('M12 3a12 12 0 0 1 0 18'),
  p('M12 3a12 12 0 0 0 0 18'),
])
// 兜底:扳手(未知工具 / 子 agent 等)
export const WrenchIcon = svg24([
  p('M21 7a5 5 0 0 1-7 4L7 18a2 2 0 0 1-3-3l7-7a5 5 0 0 1 6-6l-3 3 4 4 3-3z'),
])

// 16×16 状态图标(hapi ToolStatusIcon 同款):完成=圈勾,出错=圈叉
// (圆圈 + 交叉斜线),等审批=挂锁,运行中=spinner。
export const ToolStatusIcon: FunctionalComponent<{ state: string }> = (props) => {
  const base = { viewBox: '0 0 16 16', fill: 'none', 'aria-hidden': 'true' }
  const s = { stroke: 'currentColor', 'stroke-width': 1.5, 'stroke-linecap': 'round' as const, 'stroke-linejoin': 'round' as const }
  switch (props.state) {
    case 'ok':
      return h('svg', base, [
        h('circle', { cx: 8, cy: 8, r: 6, ...s }),
        h('path', { d: 'M5.2 8.3l1.8 1.8 3.8-4', ...s }),
      ])
    case 'error':
      return h('svg', base, [
        h('circle', { cx: 8, cy: 8, r: 6, ...s }),
        h('path', { d: 'M5.6 5.6l4.8 4.8M10.4 5.6l-4.8 4.8', ...s }),
      ])
    case 'pending':
      return h('svg', base, [
        h('rect', { x: 4.5, y: 7, width: 7, height: 6, rx: 1.5, ...s }),
        h('path', { d: 'M6 7V5.8a2 2 0 0 1 4 0V7', ...s }),
      ])
    default: // running
      return h('svg', { ...base, viewBox: '0 0 24 24', class: 'tool-icon-spin' }, [
        h('circle', { cx: 12, cy: 12, r: 9, stroke: 'currentColor', 'stroke-width': 2.5, opacity: 0.25 }),
        h('path', { d: 'M21 12a9 9 0 0 0-9-9', stroke: 'currentColor', 'stroke-width': 2.5, 'stroke-linecap': 'round', opacity: 0.75 }),
      ])
  }
}

// 工具 → 分类图标(hapi knownTools 注册表精简):读=眼、搜=放大镜、
// 写=折角文件、命令=终端、网页=地球,其余(mcp__*、子 agent、未知)扳手。
export function toolCategoryIcon(tool: string): FunctionalComponent {
  if (tool === 'Read' || tool === 'NotebookRead' || tool === 'Grep' || tool === 'view_image') return EyeIcon
  if (tool === 'Glob' || tool === 'LS') return SearchIcon
  if (['Edit', 'MultiEdit', 'Write', 'NotebookEdit', 'CodexPatch', 'CodexDiff', 'ApplyPatch'].includes(tool)) return FileDiffIcon
  if (tool === 'Bash' || tool === 'CodexBash' || tool === 'shell_command' || tool === 'run_shell_command') return TerminalIcon
  if (tool === 'WebFetch' || tool === 'WebSearch') return GlobeIcon
  return WrenchIcon
}

// 是否有专属分类图标:有 → 卡头只出图标;没有(扳手兜底)→ 图标旁保留
// 工具名,子 agent / MCP / 未知工具光靠扳手认不出来。
export function hasToolCategory(tool: string): boolean {
  return toolCategoryIcon(tool) !== WrenchIcon
}
