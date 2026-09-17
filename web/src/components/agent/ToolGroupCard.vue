<script setup lang="ts">
// 连续工具卡的聚合组卡(hapi buildVisibleChatBlocks + groupedPresentation 同款):
// 2 张以上连续的可组工具(混排不限同类)合一张卡。标题按主导意图起
// (查看文件/搜索内容/修改文件/运行命令/打开网页),能落到具体目标就用
// 「查看 foo.ts」「搜索 pattern」这样的标题;混排时摘要行列出各类型计数。
// 组内单行可再点:打开 AgentView 层的详情弹窗(ToolDetailModal)看该次的
// 参数与结果。弹窗不挂组卡内部 —— 实时回合里审批事件会把卡片拆出组又并
// 回来,组卡一卸载弹窗就没了(表现为点开就关)。
import { computed, ref } from 'vue'
import type { AgentToolCall } from '@/api/client'
import { ToolStatusIcon, hasToolCategory, toolCategoryIcon } from './toolIcons'

const props = defineProps<{
  calls: AgentToolCall[]
}>()

const emit = defineEmits<{ (e: 'detail', call: AgentToolCall): void }>()

const open = ref(false)

// ---- 意图分类(hapi getToolGroupActionKind / inferGroupedSummaryIntent 精简版) ----
type Kind = 'read' | 'search' | 'command' | 'mutation' | 'web' | 'other'

const KIND_LABEL: Record<Kind, string> = {
  read: '查看文件',
  search: '搜索内容',
  command: '运行命令',
  mutation: '修改文件',
  web: '打开网页',
  other: '工具调用',
}

function actionKind(tool: string): Kind {
  if (tool === 'Read' || tool === 'NotebookRead' || tool === 'LS' || tool === 'view_image') return 'read'
  if (tool === 'Grep' || tool === 'Glob') return 'search'
  if (tool === 'Bash' || tool === 'CodexBash' || tool === 'shell_command' || tool === 'run_shell_command') return 'command'
  if (['Edit', 'MultiEdit', 'Write', 'NotebookEdit', 'CodexPatch', 'CodexDiff', 'ApplyPatch'].includes(tool)) return 'mutation'
  if (tool === 'WebFetch' || tool === 'WebSearch') return 'web'
  return 'other'
}

// 参数 JSON 解析一次,组内多处用;截断的非法 JSON 解析失败返回 null。
function parseArgs(c: AgentToolCall): Record<string, unknown> | null {
  try {
    const v = c.args ? JSON.parse(c.args) : null
    return v && typeof v === 'object' ? v as Record<string, unknown> : null
  } catch { return null }
}

function argString(args: Record<string, unknown> | null, keys: string[]): string | null {
  if (!args) return null
  for (const k of keys) {
    const v = args[k]
    if (typeof v === 'string' && v) return v
  }
  return null
}

// 标签只做空白归一 —— 不过滤、不截断、不省略。自用工具,折叠卡里的目标是认出
// 「这一步干了什么」的唯一线索,藏了等于没有;值长就靠行横向滚动看全。
function label(v: string | null): string | null {
  if (!v) return null
  return v.replace(/\s+/g, ' ').trim() || null
}

// 能当一行摘要的字符串:第一个非空字符串值。不做长度/换行过滤 —— 值就是值,
// 行本身是横向可滚的单行,长就滚着看,不藏。
function summarizing(v: unknown): string | null {
  return typeof v === 'string' && v.trim() ? v : null
}

// 参数键形形色色(每个工具、每个 MCP 各不同),硬编码的键列表必然漏 —— 漏了整行
// 就没有目标,只剩图标 +「N 行」,点开也认不出是什么(实测 PushNotification、
// CronCreate 就是这样)。取不到已知键就退到第一个像摘要的字符串值。
function argLabel(args: Record<string, unknown> | null, keys: string[]): string | null {
  const hit = label(argString(args, keys))
  if (hit) return hit
  if (!args) return null
  for (const v of Object.values(args)) {
    const s = summarizing(v)
    if (s) return s
  }
  return null
}

// ApplyPatch/CodexPatch 的路径在 changes[].path 里(没有 file_path 这种平键)。
function patchPaths(args: Record<string, unknown> | null): string | null {
  if (!args || !Array.isArray(args.changes)) return null
  const out: string[] = []
  for (const e of args.changes as unknown[]) {
    if (!e || typeof e !== 'object') continue
    const o = e as Record<string, unknown>
    const v = o.path ?? o.file ?? o.filePath ?? o.file_path
    if (typeof v === 'string' && v) out.push(v)
  }
  return out.length ? out.join('、') : null
}

// CodexDiff 的路径藏在 unified_diff 的 "+++ b/xxx" 头里。
function diffPath(args: Record<string, unknown> | null): string | null {
  const t = args?.unified_diff
  if (typeof t !== 'string') return null
  const m = t.match(/^\+\+\+ (?:b\/)?([^\t\n]+)/m)
  return m ? m[1].trim() : null
}

function basename(v: string): string {
  return v.replace(/\\/g, '/').split('/').filter(Boolean).pop() || v
}

// 一条工具的目标摘要:按意图取对应字段,取不到退到兜底。
function callTarget(c: AgentToolCall): string | null {
  const args = parseArgs(c)
  const kind = actionKind(c.tool)
  if (kind === 'read' || kind === 'mutation') {
    return label(argString(args, ['file_path', 'path', 'file', 'filePath', 'notebook_path']))
      ?? patchPaths(args) ?? diffPath(args) ?? argLabel(args, [])
  }
  if (kind === 'search') return argLabel(args, ['pattern', 'query'])
  if (kind === 'command') return argLabel(args, ['command', 'cmd'])
  if (kind === 'web') return argLabel(args, ['url', 'query'])
  // other 覆盖子 agent(description/prompt)、MCP 工具、定时任务等。
  return argLabel(args, ['description', 'prompt', 'message', 'file_path', 'path', 'pattern', 'query', 'command', 'url', 'task_id', 'name', 'target'])
}

// 主导意图:组内出现最多的类型(平票先到先得,hapi getPrimaryIntent 同款)。
const primaryKind = computed<Kind>(() => {
  const counts = new Map<Kind, number>()
  for (const c of props.calls) {
    const k = actionKind(c.tool)
    counts.set(k, (counts.get(k) ?? 0) + 1)
  }
  let best: Kind = 'other'
  let max = -1
  for (const [k, n] of counts) {
    if (n > max) { best = k; max = n }
  }
  return best
})

// 标题(hapi formatSpecificIntentTitle 精简版):首个 description → 主导意图
// 的具体目标(查看/编辑 xxx、搜索 pattern)→ 主导意图标签。
const title = computed(() => {
  for (const c of props.calls) {
    const d = label(argString(parseArgs(c), ['description']))
    if (d) return d
  }
  const matching = props.calls.filter((c) => actionKind(c.tool) === primaryKind.value)
  for (const c of matching) {
    const target = callTarget(c)
    if (!target) continue
    const k = primaryKind.value
    if (k === 'read') return `查看 ${basename(target)}`
    if (k === 'mutation') return `编辑 ${basename(target)}`
    if (k === 'search') return `搜索 ${target}`
    if (k === 'command') return `运行 ${target}`
    if (k === 'web') return target
  }
  return KIND_LABEL[primaryKind.value]
})

// 摘要行:单一类型时列全部目标(不去基名、不"等 N 项"—— 折叠状态下这行是
// 唯一能看出组里干了什么的地方,排不下就横向滚),混排时列各类型计数。
const brief = computed(() => {
  const kinds = new Set(props.calls.map((c) => actionKind(c.tool)))
  if (kinds.size > 1) {
    const order: Kind[] = ['command', 'search', 'read', 'mutation', 'web', 'other']
    const parts: string[] = []
    for (const k of order) {
      const n = props.calls.filter((c) => actionKind(c.tool) === k).length
      if (n > 0) parts.push(`${KIND_LABEL[k]} ${n}`)
    }
    return parts.join(' · ')
  }
  const uniq = [...new Set(props.calls.map((c) => callTarget(c)).filter((v): v is string => !!v))]
  return uniq.join('、')
})

const state = computed(() =>
  props.calls.some((c) => c.state === 'error') ? 'error'
    : props.calls.some((c) => c.state === 'running') ? 'running' : 'ok',
)

// 组内单行:分类图标已表意(眼/终端/折角文件),不再重复"查看文件/运行
// 命令"文字;兜底类(子 agent/MCP)没有专属图标,保留工具名。
// 行目标与单卡 brief 同语义给完整值(不再截基名):Read/Edit 给全路径,
// 基名认不出目录、多文件同名时更分不清;Grep/Glob 给「模式 · 目录」——
// 单卡 brief 的键序里 path 在 pattern 前,只显 path,但组内多条搜索往往
// 同目录,得靠模式区分,两个都给。
function rowTarget(c: AgentToolCall): string {
  if (actionKind(c.tool) === 'search') {
    const args = parseArgs(c)
    const pattern = argLabel(args, ['pattern', 'query'])
    const path = label(argString(args, ['path', 'file_path']))
    return [pattern, path].filter(Boolean).join(' · ')
  }
  return callTarget(c) || ''
}

</script>

<template>
  <div class="group-card" :class="state">
    <button type="button" class="group-head" @click="open = !open">
      <!-- 折叠开关用箭头:收起 ›,展开旋转 90° 朝下;状态用图标(勾/叉/spinner) -->
      <span class="group-caret" :class="{ open }" />
      <span class="group-title">{{ title }} · {{ calls.length }} 项</span>
      <span v-if="brief" class="group-brief">{{ brief }}</span>
      <span class="group-state" :class="state"><ToolStatusIcon :state="state" /></span>
    </button>
    <div v-if="open" class="group-body">
      <button v-for="(c, i) in calls" :key="c.toolUseId || i" type="button" class="group-item" @click="emit('detail', c)">
        <span class="item-state" :class="c.state"><ToolStatusIcon :state="c.state || 'ok'" /></span>
        <span class="item-icon"><component :is="toolCategoryIcon(c.tool)" /></span>
        <!-- 拿不到目标(参数缺失/事件被分页截断)时把工具名补上:不能只留一枚
             图标 +「N 行」——那行就认不出来了 -->
        <span v-if="!hasToolCategory(c.tool) || !rowTarget(c)" class="item-name">{{ c.tool || '工具' }}</span>
        <span v-if="rowTarget(c)" class="item-target">{{ rowTarget(c) }}</span>
        <span v-if="c.result" class="item-lines">{{ c.result.trim().split('\n').length }} 行</span>
      </button>
    </div>

  </div>
</template>

<style scoped>
.group-card {
  border: 1px solid rgba(127, 127, 127, .18);
  border-radius: var(--lr-radius);
  background: var(--lr-bg-elevated);
  overflow: hidden;
}
.group-card.error { border-color: rgba(220, 38, 38, .45); }
.group-head {
  display: flex; align-items: center; gap: 8px;
  width: 100%; min-height: 38px; padding: 6px 10px;
  appearance: none; border: 0; background: transparent;
  font: inherit; font-size: 13px; color: var(--lr-fg);
  cursor: pointer; text-align: left;
  -webkit-tap-highlight-color: transparent;
}
/* 折叠箭头:收起 ›,展开旋转 90° 朝下 */
.group-caret {
  flex: none; width: 1em; text-align: center;
  opacity: .7; transition: transform .15s ease;
}
.group-caret::before { content: '›'; font-weight: 600; }
.group-caret.open { transform: rotate(90deg); }
/* 状态图标:完成=绿圈勾,出错=红圈叉,运行中=spinner(hapi 同款) */
.group-state { flex: none; width: 14px; height: 14px; color: var(--lr-fg-muted); }
.group-state svg, .item-state svg, .item-icon svg { width: 100%; height: 100%; display: block; }
.group-state.ok { color: var(--lr-ok); }
.group-state.error { color: var(--lr-danger); }
/* 标题是组里第一条工具的 description 原文,不截断:长了就换行,别撑破卡头 */
.group-title { flex: 0 1 auto; min-width: 0; overflow-wrap: anywhere; font-weight: 600; font-size: 12px; }
.group-brief {
  min-width: 0; flex: 1;
  white-space: nowrap;
  overflow-x: auto; overflow-y: hidden;
  scrollbar-width: none;
  font-size: 12px; color: var(--lr-fg-muted);
  font-family: ui-monospace, monospace;
}
.group-brief::-webkit-scrollbar { display: none; }
.group-body { border-top: 1px solid rgba(127, 127, 127, .14); }
/* 单条从 div 换成 button(点开详情):补上按钮语义的归零样式 */
.group-item {
  display: flex; align-items: center; gap: 8px;
  width: 100%; padding: 6px 10px; font-size: 12px;
  appearance: none; border: 0; background: transparent;
  font: inherit; color: inherit; text-align: left; cursor: pointer;
  -webkit-tap-highlight-color: transparent;
  min-height: 38px;
}
.group-item + .group-item { border-top: 1px solid rgba(127, 127, 127, .08); }
/* 行内:状态图标 + 分类图标 + 标签(图标系两枚 12px,间距收紧) */
.item-state { flex: none; width: 12px; height: 12px; color: var(--lr-ok); }
.item-state.error { color: var(--lr-danger); }
.item-state.running { color: var(--lr-fg-muted); }
.item-icon { flex: none; width: 12px; height: 12px; color: var(--lr-fg-muted); }
.item-name { flex: none; color: var(--lr-fg); }
.item-target {
  min-width: 0; flex: 1;
  white-space: nowrap; overflow-x: auto; overflow-y: hidden; scrollbar-width: none;
  font-family: ui-monospace, monospace; font-size: 12px; color: var(--lr-fg-muted);
}
.item-target::-webkit-scrollbar { display: none; }
.item-lines { flex: none; color: var(--lr-fg-muted); }
</style>
