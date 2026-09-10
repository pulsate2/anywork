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

// 敏感内容不进标签(hapi safeGroupedLabelValue:token/密钥形状的值直接弃用)。
const SENSITIVE_TEXT_RE = /(?:bearer\s+\S+|(?:api[_-]?key|token|password|secret)(?:\s*[:=]\s*\S+|\s+\S{12,})|(?:gh[pousr]_|github_pat_|sk-[a-z0-9_-]*|xox[baprs]-)[a-z0-9_-]{12,}|[a-f0-9]{32,}|[a-z0-9_+/=-]{40,})/i
const MAX_LABEL = 72

function safeLabel(v: string | null): string | null {
  if (!v || SENSITIVE_TEXT_RE.test(v)) return null
  const normalized = v.replace(/\s+/g, ' ').trim()
  return normalized.length > MAX_LABEL ? `${normalized.slice(0, MAX_LABEL - 1)}…` : normalized
}

function basename(v: string): string {
  return v.replace(/\\/g, '/').split('/').filter(Boolean).pop() || v
}

// 一条工具的目标摘要:按意图取对应字段,取不到给空。
function callTarget(c: AgentToolCall): string | null {
  const args = parseArgs(c)
  const kind = actionKind(c.tool)
  if (kind === 'read' || kind === 'mutation') {
    return safeLabel(argString(args, ['file_path', 'path', 'file', 'filePath', 'notebook_path']))
  }
  if (kind === 'search') return safeLabel(argString(args, ['pattern', 'query']))
  if (kind === 'command') return safeLabel(argString(args, ['command', 'cmd']))
  if (kind === 'web') return safeLabel(argString(args, ['url', 'query']))
  // other 覆盖子 agent(Agent/Task 的 description)与 TaskOutput(task_id)。
  return safeLabel(argString(args, ['description', 'file_path', 'path', 'pattern', 'query', 'command', 'url', 'task_id', 'name']))
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
    const d = safeLabel(argString(parseArgs(c), ['description']))
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

// 摘要行:单一类型时列目标(基名去重),混排时列各类型计数(hapi 副标题同款)。
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
  if (!uniq.length) return ''
  const named = primaryKind.value === 'read' || primaryKind.value === 'mutation'
    ? uniq.map((v) => basename(v)) : uniq
  const u = [...new Set(named)]
  if (u.length <= 3) return u.join('、')
  return `${u.slice(0, 2).join('、')} 等 ${u.length} 项`
})

const state = computed(() =>
  props.calls.some((c) => c.state === 'error') ? 'error'
    : props.calls.some((c) => c.state === 'running') ? 'running' : 'ok',
)
const stateLabel = computed(() =>
  state.value === 'running' ? '运行中' : state.value === 'error' ? '出错' : '完成',
)

// 组内单行标签:意图名(other 用工具名)+ 目标。
function rowLabel(c: AgentToolCall): string {
  const k = actionKind(c.tool)
  return k === 'other' ? c.tool : KIND_LABEL[k]
}
function rowTarget(c: AgentToolCall): string {
  const t = callTarget(c)
  if (!t) return ''
  return (actionKind(c.tool) === 'read' || actionKind(c.tool) === 'mutation') ? basename(t) : t
}

</script>

<template>
  <div class="group-card" :class="state">
    <button type="button" class="group-head" @click="open = !open">
      <!-- 折叠开关用箭头:收起 ›,展开旋转 90° 朝下(圆点换掉,状态已有右侧
           「完成/运行中/出错」承担,圆点信息重复) -->
      <span class="group-caret" :class="{ open }" />
      <span class="group-title">{{ title }} · {{ calls.length }} 项</span>
      <span v-if="brief" class="group-brief">{{ brief }}</span>
      <span class="group-state">{{ stateLabel }}</span>
    </button>
    <div v-if="open" class="group-body">
      <button v-for="(c, i) in calls" :key="c.toolUseId || i" type="button" class="group-item" @click="emit('detail', c)">
        <span class="item-dot" :class="c.state" />
        <span class="item-name">{{ rowLabel(c) }}</span>
        <span v-if="rowTarget(c)" class="item-target">{{ rowTarget(c) }}</span>
        <span v-if="c.result" class="item-lines">{{ c.result.trim().split('\n').length }} 行</span>
        <span class="item-state">{{ c.state === 'error' ? '出错' : c.state === 'running' ? '运行中' : '完成' }}</span>
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
@keyframes group-pulse { 50% { opacity: .35; } }
.group-title { flex: none; font-weight: 600; font-size: 12px; }
.group-brief {
  min-width: 0; flex: 1;
  white-space: nowrap;
  overflow-x: auto; overflow-y: hidden;
  scrollbar-width: none;
  font-size: 12px; color: var(--lr-fg-muted);
  font-family: ui-monospace, monospace;
}
.group-brief::-webkit-scrollbar { display: none; }
.group-state { flex: none; font-size: 11px; color: var(--lr-fg-muted); }
.group-card.error .group-state { color: var(--lr-danger); }
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
.item-dot { flex: none; width: 6px; height: 6px; border-radius: 50%; background: var(--lr-ok); }
.item-dot.error { background: var(--lr-danger); }
.item-dot.running { background: var(--lr-warn); animation: group-pulse 1.2s ease-in-out infinite; }
.item-name { flex: none; color: var(--lr-fg); }
.item-target {
  min-width: 0; flex: 1;
  white-space: nowrap; overflow-x: auto; overflow-y: hidden; scrollbar-width: none;
  font-family: ui-monospace, monospace; font-size: 12px; color: var(--lr-fg-muted);
}
.item-target::-webkit-scrollbar { display: none; }
.item-lines { flex: none; color: var(--lr-fg-muted); }
.item-state { flex: none; color: var(--lr-fg-muted); font-size: 11px; }
</style>
