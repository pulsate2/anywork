<script setup lang="ts">
// 输入框上方的状态行(hapi StatusBar / claude code 风格):左侧状态点+文案
// (思考中随机动词 + ✻ 转动),旁边上下文用量条(点击弹详情);右侧模型、
// 思考强度、权限模式。回合状态从聊天头挪到这里。
import { computed, ref, watch } from 'vue'
import { NPopover } from 'naive-ui'
import type { AgentApp, AgentUsage } from '@/api/client'

const props = defineProps<{
  state: 'running' | 'idle' | 'dead'
  pendingApproval?: boolean
  // 压缩进行中(claude code 底部 Compacting… 同款):优先于思考动词显示。
  compacting?: boolean
  usage?: AgentUsage | null
  app?: AgentApp
  model?: string
  effort?: string
  permissionMode?: string
}>()

// claude code 同款思考动词:进入 running 时随机挑一个,回合内不再跳变。
const VERBS = [
  'Brewing', 'Pondering', 'Cooking', 'Vibing', 'Mulling', 'Scheming',
  'Tinkering', 'Percolating', 'Conjuring', 'Wrangling', 'Simmering',
  'Crunching', 'Divining', 'Marinating', 'Wizarding', 'Noodling',
]
const verb = ref(VERBS[0])
watch(() => props.state, (s) => {
  if (s === 'running') verb.value = VERBS[Math.floor(Math.random() * VERBS.length)]
})

const status = computed(() => {
  if (props.pendingApproval) return { text: '等待审批', tone: 'warn', pulse: true }
  // 压缩进行中:回合其实还在跑,但值得单独说出来(claude code 的 Compacting…)
  if (props.compacting) return { text: '压缩上下文…', tone: 'busy', pulse: true }
  if (props.state === 'running') return { text: `${verb.value}…`, tone: 'busy', pulse: true }
  // hapi 同款:进程活着就"在线"(绿),进程没了"离线"(灰,发消息可复活)
  if (props.state === 'dead') return { text: '离线', tone: 'dead', pulse: false }
  return { text: '在线', tone: 'idle', pulse: false }
})

// ---- 上下文用量 ----
// 模型上下文窗口启发式(hapi modelConfig 精简版):服务端没给显式窗口时兜底。
// claude 默认 200k;[1m] 后缀 / fable 是 1M;codex app-server 报 258,400。
function contextWindow(app?: string, model?: string): number {
  if (app === 'codex') return 258_400
  if (model && (/\[1m\]/i.test(model) || /fable/i.test(model))) return 1_000_000
  return 200_000
}

function fmtTokens(v: number): string {
  if (v >= 1_000_000) return `${(v / 1_000_000).toFixed(1)}M`
  if (v >= 1_000) return `${Math.round(v / 1_000)}k`
  return String(v)
}

const ctx = computed(() => {
  const u = props.usage
  if (!u || !u.context) return null
  const win = contextWindow(props.app, u.model || props.model)
  const pct = Math.min(100, Math.max(0, Math.round((u.context / win) * 100)))
  const tone = pct >= 90 ? 'danger' : pct >= 70 ? 'warn' : 'idle'
  return {
    pct,
    tone,
    label: `${pct}% · ${fmtTokens(u.context)}/${fmtTokens(win)}`,
    details: {
      used: fmtTokens(u.context),
      usedPct: pct,
      remaining: fmtTokens(Math.max(0, win - u.context)),
      remainingPct: 100 - pct,
      cacheRead: u.cacheRead ? fmtTokens(u.cacheRead) : '',
    },
  }
})

// ---- 右侧:思考强度 / 权限模式 ----
const effortLabel = computed(() => {
  const v = props.effort
  if (!v || v === 'none') return ''
  return `思考:${{ low: '低', medium: '中', high: '高' }[v] || v}`
})

const PERM_LABELS: Record<string, { text: string; tone: string }> = {
  plan: { text: '只读规划', tone: 'info' },
  ask: { text: '每次询问', tone: 'idle' },
  edits: { text: '放行编辑', tone: 'warn' },
  accept: { text: '全部放行', tone: 'danger' },
}
const perm = computed(() => PERM_LABELS[props.permissionMode || ''])
</script>

<template>
  <div class="statusbar">
    <div class="sb-left">
      <span class="sb-dot" :class="[status.tone, { pulse: status.pulse }]" />
      <span v-if="status.tone === 'busy'" class="sb-verb"><span class="sb-spin">✻</span>{{ status.text }}</span>
      <span v-else class="sb-text" :class="status.tone">{{ status.text }}</span>
      <!-- 上下文用量:条 + 百分比 + token 数,点击弹明细(hapi 同款) -->
      <n-popover v-if="ctx" trigger="click" placement="top-start" :show-arrow="false">
        <template #trigger>
          <button type="button" class="sb-ctx" :class="ctx.tone">
            <span class="sb-ctx-bar"><span class="sb-ctx-fill" :style="{ width: ctx.pct + '%' }" /></span>
            <span class="sb-ctx-label">{{ ctx.label }}</span>
          </button>
        </template>
        <div class="sb-ctx-detail">
          <div v-if="ctx.details.cacheRead">缓存读取 {{ ctx.details.cacheRead }}</div>
          <div>已用 {{ ctx.details.used }}({{ ctx.details.usedPct }}%)</div>
          <div>剩余 {{ ctx.details.remaining }}({{ ctx.details.remainingPct }}%)</div>
        </div>
      </n-popover>
    </div>
    <div class="sb-right">
      <span v-if="model" class="sb-model">{{ model }}</span>
      <span v-if="effortLabel" class="sb-effort">{{ effortLabel }}</span>
      <span v-if="perm" class="sb-perm" :class="perm.tone">{{ perm.text }}</span>
    </div>
  </div>
</template>

<style scoped>
.statusbar {
  display: flex; align-items: center; justify-content: space-between; gap: 8px;
  padding: 0 2px;
  min-height: 22px;
  font-size: 12px; line-height: 1.4;
}
.sb-left, .sb-right { display: flex; align-items: center; gap: 8px; min-width: 0; }
.sb-right { flex: none; flex-wrap: wrap; justify-content: flex-end; }
.sb-dot { flex: none; width: 7px; height: 7px; border-radius: 50%; }
.sb-dot.idle { background: var(--lr-ok); }
.sb-dot.busy { background: #3b82f6; }
.sb-dot.warn { background: var(--lr-warn); }
.sb-dot.dead { background: var(--lr-fg-muted); }
.sb-dot.pulse { animation: sb-pulse 1.4s ease-in-out infinite; }
@keyframes sb-pulse { 50% { opacity: .35; } }

.sb-text { white-space: nowrap; }
.sb-text.idle { color: var(--lr-fg-muted); }
.sb-text.warn { color: var(--lr-warn); }
.sb-text.dead { color: var(--lr-fg-muted); }
/* 思考中:claude code 同款 ✻ 转动 + 动词 */
.sb-verb { white-space: nowrap; color: #3b82f6; display: inline-flex; align-items: center; gap: 4px; }
.sb-spin { display: inline-block; animation: sb-rotate 2.4s linear infinite; font-size: 11px; }
@keyframes sb-rotate { to { transform: rotate(360deg); } }

/* 上下文用量按钮:透明按钮,条+文字;>70% 黄、>90% 红 */
.sb-ctx {
  display: inline-flex; align-items: center; gap: 6px;
  appearance: none; border: 0; background: transparent;
  font: inherit; font-size: 11px; padding: 2px 0; min-height: 24px;
  cursor: pointer; color: var(--lr-fg-muted);
  -webkit-tap-highlight-color: transparent;
}
.sb-ctx.warn { color: var(--lr-warn); }
.sb-ctx.danger { color: var(--lr-danger); }
.sb-ctx-bar {
  flex: none; width: 44px; height: 3px; border-radius: 2px;
  background: rgba(127, 127, 127, .25); overflow: hidden;
}
.sb-ctx-fill { display: block; height: 100%; border-radius: 2px; background: currentColor; }
.sb-ctx-label { white-space: nowrap; font-variant-numeric: tabular-nums; }

.sb-model { white-space: nowrap; font-size: 11px; color: var(--lr-fg-muted); max-width: 180px; overflow: hidden; text-overflow: ellipsis; }
.sb-effort { white-space: nowrap; font-size: 11px; color: var(--lr-fg-muted); }
.sb-perm { white-space: nowrap; font-size: 11px; }
.sb-perm.idle { color: var(--lr-fg-muted); }
.sb-perm.info { color: #3b82f6; }
.sb-perm.warn { color: var(--lr-warn); }
.sb-perm.danger { color: var(--lr-danger); }
</style>

<!-- popover 渲染到 body,scoped 不作用于内容,放非 scoped 块 -->
<style>
.sb-ctx-detail { display: flex; flex-direction: column; gap: 2px; font-size: 12px; }
</style>
