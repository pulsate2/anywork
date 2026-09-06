<script setup lang="ts">
// 连续同类只读工具的聚合组卡(hapi buildVisibleChatBlocks 同款思路):
// 2 张以上连续的"可组"工具合一张卡,一行摘要(数量 + 主意图 + 涉及目标),
// 点开是组内每张工具的单行。写/改类(Edit/Write/Bash/ApplyPatch/CodexDiff)
// 与待审批、TodoWrite、AskUserQuestion 不参与聚合 —— 它们每张都值得单看。
import { computed, ref } from 'vue'
import type { AgentToolCall } from '@/api/client'

const props = defineProps<{ calls: AgentToolCall[] }>()

const open = ref(false)

// 意图(hapi inferGroupedSummaryIntent 的精简版):
// 读文件 / 搜内容 / 开网页 / 跑命令 / 其他工具。
const intentLabel = computed(() => {
  const read = ['Read', 'LS', 'NotebookRead', 'Glob']
  const search = ['Grep', 'WebSearch']
  const web = ['WebFetch']
  const first = props.calls[0]?.tool
  if (read.includes(first)) return '读取文件'
  if (search.includes(first)) return '搜索内容'
  if (web.includes(first)) return '访问网页'
  if (first === 'Bash') return '执行命令'
  return '工具调用'
})

// 摘要目标:组内各工具的 path/pattern/url/command,取前几个拼一行。
const targets = computed(() => {
  const out: string[] = []
  for (const c of props.calls) {
    let v = ''
    try {
      const args = c.args ? JSON.parse(c.args) : null
      if (args && typeof args === 'object') {
        for (const key of ['path', 'file_path', 'pattern', 'url', 'query', 'command']) {
          const t = (args as Record<string, unknown>)[key]
          if (typeof t === 'string' && t) { v = t; break }
        }
      }
    } catch { /* 截断的 JSON:没有目标就跳过 */ }
    if (v) out.push(v.split('/').pop() || v)
  }
  return out
})

// 基名去重后拼摘要:3 个以内全列,更多就列 2 个 + "等 n 项"。
const targetBrief = computed(() => {
  const uniq = [...new Set(targets.value)]
  if (!uniq.length) return ''
  if (uniq.length <= 3) return uniq.join('、')
  return `${uniq.slice(0, 2).join('、')} 等 ${uniq.length} 项`
})

const state = computed(() =>
  props.calls.some((c) => c.state === 'error') ? 'error'
    : props.calls.some((c) => c.state === 'running') ? 'running' : 'ok',
)
const stateLabel = computed(() =>
  state.value === 'running' ? '运行中' : state.value === 'error' ? '出错' : '完成',
)
</script>

<template>
  <div class="group-card" :class="state">
    <button type="button" class="group-head" @click="open = !open">
      <span class="group-dot" />
      <span class="group-title">{{ intentLabel }} · {{ calls.length }} 项</span>
      <span v-if="targetBrief" class="group-brief">{{ targetBrief }}</span>
      <span class="group-state">{{ stateLabel }}</span>
    </button>
    <div v-if="open" class="group-body">
      <div v-for="(c, i) in calls" :key="c.toolUseId || i" class="group-item">
        <span class="item-dot" :class="c.state" />
        <span class="item-name">{{ c.tool }}</span>
        <span v-if="c.result" class="item-lines">{{ c.result.trim().split('\n').length }} 行</span>
        <span class="item-state">{{ c.state === 'error' ? '出错' : c.state === 'running' ? '运行中' : '完成' }}</span>
      </div>
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
.group-dot { flex: none; width: 8px; height: 8px; border-radius: 50%; background: var(--lr-warn); }
.group-card.ok .group-dot { background: var(--lr-ok); }
.group-card.error .group-dot { background: var(--lr-danger); }
.group-card.running .group-dot { animation: group-pulse 1.2s ease-in-out infinite; }
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
.group-item {
  display: flex; align-items: center; gap: 8px;
  padding: 6px 10px; font-size: 12px;
}
.group-item + .group-item { border-top: 1px solid rgba(127, 127, 127, .08); }
.item-dot { flex: none; width: 6px; height: 6px; border-radius: 50%; background: var(--lr-ok); }
.item-dot.error { background: var(--lr-danger); }
.item-dot.running { background: var(--lr-warn); animation: group-pulse 1.2s ease-in-out infinite; }
.item-name { flex: none; font-family: ui-monospace, monospace; color: var(--lr-fg); }
.item-lines { min-width: 0; flex: 1; color: var(--lr-fg-muted); }
.item-state { flex: none; color: var(--lr-fg-muted); font-size: 11px; }
</style>
