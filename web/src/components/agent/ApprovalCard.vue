<script setup lang="ts">
// 审批卡片:一行摘要 + 三个决定:拒绝 / 允许 / 本会话允许
// (后者对同类请求整个会话放行,不再逐次问)。resolved 态只展示结论,
// 不再给按钮 —— 防止对着已经答复过的请求重复点。
// compact = 内嵌在工具卡里:命令摘要那一行省掉(工具卡头部本来就显示着)。
import { computed } from 'vue'
import { NButton } from 'naive-ui'
import type { AgentPermissionReq } from '@/api/client'

const props = defineProps<{
  req: AgentPermissionReq
  resolved?: { allow: boolean; session?: boolean } | null
  busy?: boolean
  compact?: boolean
}>()

const emit = defineEmits<{ (e: 'decide', allow: boolean, session: boolean): void }>()

// 摘要只取"命令/路径"这一个关键值,不整段贴参数原文 ——
// 和 ToolCallCard 的 brief 同一算法。
const brief = computed(() => {
  try {
    const args = props.req.args ? JSON.parse(props.req.args) : {}
    for (const key of ['command', 'file_path', 'path', 'pattern', 'url', 'query']) {
      if (typeof args[key] === 'string' && (args[key] as string)) return args[key] as string
    }
    const first = Object.values(args)[0]
    if (typeof first === 'string' && first) return first
  } catch { /* 截断的非法 JSON:落到原文,单行省略 */ }
  return props.req.args || ''
})

// 结论文案:区分"允许过一次"和"本会话允许"(同类不再问)。
const verdict = computed(() => {
  if (!props.resolved) return ''
  if (!props.resolved.allow) return '已拒绝'
  return props.resolved.session ? '本会话已允许' : '已允许'
})
</script>

<template>
  <div class="appr" :class="{ done: !!resolved }">
    <div class="appr-top">
      <span class="appr-tool">{{ req.tool }}</span>
      <span v-if="resolved" class="appr-verdict" :class="resolved.allow ? 'ok' : 'no'">
        {{ verdict }}
      </span>
      <span v-else class="appr-wait">等待你的决定</span>
    </div>
    <div v-if="brief && !compact" class="appr-args" :title="brief">{{ brief }}</div>
    <div v-if="!resolved" class="appr-ops">
      <n-button size="small" type="error" secondary :loading="busy" @click="emit('decide', false, false)">拒绝</n-button>
      <n-button size="small" :loading="busy" @click="emit('decide', true, false)">允许</n-button>
      <n-button size="small" type="primary" secondary :loading="busy" @click="emit('decide', true, true)">本会话允许</n-button>
    </div>
  </div>
</template>

<style scoped>
.appr {
  border: 1px solid var(--lr-warn);
  border-radius: var(--lr-radius);
  background: var(--lr-bg-elevated);
  padding: 10px 12px;
}
.appr.done { border-color: rgba(127, 127, 127, .3); }
.appr-top { display: flex; align-items: center; gap: 8px; }
.appr-tool { font-weight: 600; font-size: 13px; font-family: ui-monospace, monospace; margin-right: auto; }
.appr-wait { font-size: 12px; color: var(--lr-warn); }
.appr-verdict { font-size: 12px; }
.appr-verdict.ok { color: var(--lr-ok); }
.appr-verdict.no { color: var(--lr-danger); }
/* 摘要一行:可左右滑动看命令尾,不省略;完整参数在工具卡详情弹窗里 */
.appr-args {
  margin-top: 6px; padding: 6px 10px;
  border-radius: 6px; background: rgba(127, 127, 127, .1);
  font-family: ui-monospace, monospace; font-size: 12px; line-height: 1.5;
  white-space: nowrap;
  overflow-x: auto; overflow-y: hidden;
  scrollbar-width: none;
  color: var(--lr-fg);
}
.appr-args::-webkit-scrollbar { display: none; }
/* 三键:拒绝在左(破坏性),允许/本会话允许在右;允许=次样式,本会话=主样式 */
.appr-ops { display: flex; justify-content: flex-end; gap: 8px; margin-top: 10px; }
.appr-ops > :first-child { margin-right: auto; }
</style>
