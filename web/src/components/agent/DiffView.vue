<script setup lang="ts">
// 行级 diff 视图(对齐 hapi DiffView 的核心观感):+绿/-红行、双侧行号、
// +n/-n 统计徽章。Edit/Write/MultiEdit 工具卡展开时替换原始 JSON 展示。
import { computed } from 'vue'
import { diffLines } from 'diff'

const props = defineProps<{
  old: string
  new: string
  filePath?: string
}>()

interface DiffRow { left: string; right: string; prefix: string; text: string; kind: 'same' | 'add' | 'del' }

const rows = computed<DiffRow[]>(() => {
  const out: DiffRow[] = []
  let oldNo = 1
  let newNo = 1
  for (const part of diffLines(props.old, props.new)) {
    let lines = part.value.split('\n')
    if (lines.length && lines[lines.length - 1] === '') lines.pop()
    for (const line of lines) {
      if (part.added) {
        out.push({ left: '', right: String(newNo++), prefix: '+', text: line, kind: 'add' })
      } else if (part.removed) {
        out.push({ left: String(oldNo++), right: '', prefix: '-', text: line, kind: 'del' })
      } else {
        out.push({ left: String(oldNo++), right: String(newNo++), prefix: ' ', text: line, kind: 'same' })
      }
    }
  }
  return out
})

const additions = computed(() => rows.value.filter((r) => r.kind === 'add').length)
const deletions = computed(() => rows.value.filter((r) => r.kind === 'del').length)
</script>

<template>
  <div class="diff-view">
    <div class="diff-head">
      <span class="diff-path">{{ filePath || 'Diff' }}</span>
      <span v-if="additions || deletions" class="diff-stats">
        <span v-if="additions" class="diff-badge add">+{{ additions }}</span>
        <span v-if="deletions" class="diff-badge del">-{{ deletions }}</span>
      </span>
    </div>
    <div class="diff-body">
      <div v-for="(r, i) in rows" :key="i" class="diff-row" :class="r.kind">
        <span class="diff-no">{{ r.left }}</span>
        <span class="diff-no">{{ r.right }}</span>
        <span class="diff-line"><span class="diff-sign">{{ r.prefix }}</span>{{ r.text }}</span>
      </div>
      <div v-if="!rows.length" class="diff-empty">没有变化</div>
    </div>
  </div>
</template>

<style scoped>
.diff-view {
  border-top: 1px solid rgba(127, 127, 127, .14);
  overflow: hidden;
}
.diff-head {
  display: flex; align-items: center; justify-content: space-between; gap: 8px;
  padding: 6px 10px;
  background: rgba(127, 127, 127, .07);
  font-family: ui-monospace, monospace; font-size: 11px; color: var(--lr-fg-muted);
}
.diff-path { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
/* flex: none + nowrap:路径很长时统计徽章不许被压缩成"挤压扁"的一团 */
.diff-stats { display: flex; gap: 6px; flex: none; }
.diff-badge { padding: 0 6px; border-radius: 999px; font-size: 10px; line-height: 16px; white-space: nowrap; }
.diff-badge.add { background: rgba(34, 197, 94, .16); color: var(--lr-ok); }
.diff-badge.del { background: rgba(220, 38, 38, .14); color: var(--lr-danger); }

.diff-body {
  max-height: 300px; overflow: auto;
  font-family: ui-monospace, monospace; font-size: 12px; line-height: 1.6;
}
.diff-row { display: flex; align-items: baseline; }
.diff-no { flex: none; width: 3.5ch; text-align: right; padding-right: 4px; font-size: 10px; color: var(--lr-fg-muted); opacity: .7; }
.diff-line { flex: 1; min-width: 0; white-space: pre-wrap; overflow-wrap: anywhere; }
.diff-sign { display: inline-block; width: 1.5ch; }
.diff-row.add .diff-line { background: rgba(34, 197, 94, .12); color: var(--lr-fg); }
.diff-row.del .diff-line { background: rgba(220, 38, 38, .1); color: var(--lr-fg); }
.diff-empty { padding: 10px; font-size: 12px; color: var(--lr-fg-muted); }
</style>
