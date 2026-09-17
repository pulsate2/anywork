<script setup lang="ts">
// 子 agent 过程弹窗:左右两栏 —— 左栏步骤列表(紧凑:工具步 = 状态图标
// + 分类图标 + 摘要;文本步 = 气泡图标 + 首行预览),右栏选中步骤的
// 详情(工具步走 ToolDetailBody:diff/参数/结果;文本步按 markdown 渲染正文,
// hapi trace 的 Message 同款)。替代原先嵌在 Task 卡里上下堆叠的过程列表
// (每条两行,太占位置)。
// 窄屏(<768px,与 main.css 断点一致)左右放不下,换排上下:列表在顶(限高
// 内滚),详情接在下面。
import { computed, ref, watch } from 'vue'
import { NModal } from 'naive-ui'
import type { AgentToolCall } from '@/api/client'
import { renderMarkdown } from '@/utils/markdown'
import ToolDetailBody from './ToolDetailBody.vue'
import { ToolStatusIcon, hasToolCategory, toolCategoryIcon, MessageIcon } from './toolIcons'

const props = defineProps<{
  show: boolean
  steps: AgentToolCall[]
  // 弹窗标题:传任务描述,认得出是哪个子 agent;没有就"子代理过程"。
  title?: string
  // 会话 id:结果图片经 /api/agent/sessions/{id}/files/{name} 取。
  sessionId?: string
}>()

const emit = defineEmits<{ (e: 'update:show', v: boolean): void }>()

// 选中步骤按 toolUseId 记:过程事件回填会替换步骤对象(新的 result/state),
// 按 id 找才能拿到最新那份;父组件传进来的 steps 也是重组后的新数组。
const selectedId = ref<string | null>(null)

// 打开时的默认选中:优先第一个出错(过程弹窗多半是来查为什么挂的),
// 否则选最后一条(看最新进展)。immediate:挂载时就 show=true 也要选上。
watch(() => props.show, (v) => {
  if (!v) return
  const err = props.steps.find((s) => s.state === 'error' && s.toolUseId)
  selectedId.value = err?.toolUseId ?? props.steps[props.steps.length - 1]?.toolUseId ?? null
}, { immediate: true })
// 打开时还没有步骤(极端时序)的兜底:步骤一到就选上。
watch(() => props.steps.length, () => {
  if (selectedId.value || !props.show) return
  selectedId.value = props.steps[props.steps.length - 1]?.toolUseId ?? null
})

const selected = computed(() =>
  props.steps.find((s) => s.toolUseId && s.toolUseId === selectedId.value) ?? null)

// 步骤单行的目标摘要:文本步取首行,工具步取关键参数(Read 的 file_path、
// Bash 的 command…)。
function stepBrief(c: AgentToolCall): string {
  if (c.text) return c.text.trim().split('\n')[0] ?? ''
  try {
    const args = c.args ? JSON.parse(c.args) : null
    if (args && typeof args === 'object') {
      for (const key of ['command', 'file_path', 'path', 'pattern', 'url', 'query', 'description']) {
        const v = (args as Record<string, unknown>)[key]
        if (typeof v === 'string' && v) return v
      }
    }
  } catch { /* 截断的 args:留空 */ }
  return ''
}
</script>

<template>
  <n-modal
    :show="show" preset="card" :title="title || '子代理过程'" class="tool-modal steps-modal"
    @update:show="(v: boolean) => emit('update:show', v)"
  >
    <div class="steps-md">
      <!-- 左栏:步骤列表,单行一条,内部滚动。文本步(气泡图标 + 首行预览)与
           工具步(状态图标 + 分类图标 + 摘要)混排,按时间线顺序还原子 agent 干了什么 -->
      <div class="steps-md-list">
        <button
          v-for="(c, i) in steps" :key="c.toolUseId || i" type="button"
          class="steps-md-item" :class="[c.text ? 'msg' : c.state, { active: !!c.toolUseId && c.toolUseId === selectedId }]"
          @click="selectedId = c.toolUseId ?? null"
        >
          <template v-if="c.text">
            <span class="steps-md-icon"><MessageIcon /></span>
          </template>
          <template v-else>
            <span class="steps-md-state" :class="c.state"><ToolStatusIcon :state="c.state || 'ok'" /></span>
            <span class="steps-md-icon"><component :is="toolCategoryIcon(c.tool)" /></span>
            <span v-if="!hasToolCategory(c.tool)" class="steps-md-name">{{ c.tool || '工具' }}</span>
          </template>
          <span v-if="stepBrief(c)" class="steps-md-brief">{{ stepBrief(c) }}</span>
        </button>
      </div>
      <!-- 右栏:选中步骤的详情 —— 文本步按 markdown 渲染,工具步参数与结果 -->
      <div class="steps-md-detail">
        <div v-if="selected?.text" class="steps-md-text agent-md-body" v-html="renderMarkdown(selected.text)" />
        <ToolDetailBody v-else-if="selected" :call="selected" :session-id="sessionId" />
        <div v-else class="steps-md-empty">点左侧步骤查看详情</div>
      </div>
    </div>
  </n-modal>
</template>

<!-- n-modal teleport 到 body,样式放非 scoped 块(与 ToolDetailBody 同规矩)。 -->
<style>
/* 左右两栏:列表定宽、详情自适应;高度封顶,两栏各自内滚,不把弹窗撑穿屏 */
.steps-modal { width: min(880px, 94vw); }
.steps-md { display: flex; align-items: stretch; gap: 10px; height: min(62vh, 540px); }
.steps-md-list {
  flex: none; width: 250px; max-width: 46%;
  overflow-y: auto;
  border: 1px solid rgba(127, 127, 127, .14); border-radius: var(--lr-radius);
}
.steps-md-detail { flex: 1; min-width: 0; overflow-y: auto; }
.steps-md-empty {
  padding: 16px 8px; text-align: center;
  font-size: 12px; color: var(--lr-fg-muted);
}
/* 单条:状态图标 + 分类图标 + 摘要。摘要【不省略】:文件路径/命令换行铺开 ——
   省略号一吃,左栏就认不出这步看的是哪个文件(详情虽在右栏,但左栏是选哪一条
   的依据)。行高随内容长,所以图标改跟首行对齐。 */
.steps-md-item {
  display: flex; align-items: flex-start; gap: 6px;
  width: 100%; min-height: 34px; padding: 5px 8px;
  appearance: none; border: 0; background: transparent;
  font: inherit; font-size: 12px; color: var(--lr-fg);
  cursor: pointer; text-align: left;
  -webkit-tap-highlight-color: transparent;
}
.steps-md-item + .steps-md-item { border-top: 1px solid rgba(127, 127, 127, .08); }
.steps-md-item:hover { background: rgba(127, 127, 127, .06); }
.steps-md-item.active { background: rgba(37, 99, 235, .1); }
/* 图标槽跟摘要首行对齐(摘要可能换行成多行,居中的话图标会飘到中间) */
.steps-md-state { flex: none; width: 12px; height: 12px; margin-top: 2px; color: var(--lr-ok); }
.steps-md-state.error { color: var(--lr-danger); }
.steps-md-state.running { color: var(--lr-fg-muted); }
.steps-md-icon { flex: none; width: 12px; height: 12px; margin-top: 2px; color: var(--lr-fg-muted); }
.steps-md-state svg, .steps-md-icon svg { width: 100%; height: 100%; display: block; }
.steps-md-name { flex: none; font-family: ui-monospace, monospace; font-size: 11px; }
.steps-md-brief {
  min-width: 0; flex: 1;
  overflow-wrap: anywhere; line-height: 1.45;
  font-family: ui-monospace, monospace; font-size: 11px; color: var(--lr-fg-muted);
}
.steps-md-item.active .steps-md-brief { color: var(--lr-fg); }
/* 文本步:叙述不是代码,预览用正文字体;气泡图标跟分类图标同槽 */
.steps-md-item.msg .steps-md-brief { font-family: inherit; }
.steps-md-text { font-size: 13px; padding-right: 4px; }
/* 右栏详情块不再各自限高(55vh):外层 .steps-md-detail 已在滚,免得双层滚动 */
.steps-modal .tool-block { max-height: none; }
/* 手机端(<768px,与 main.css 的导航断点一致):左右结构放不下,换排上下 ——
   列表在顶(限高内滚),详情接在下面占余下高度。 */
@media (max-width: 767px) {
  .steps-md { flex-direction: column; height: 68vh; gap: 8px; }
  .steps-md-list { width: auto; max-width: none; max-height: 32%; flex: none; }
  .steps-md-detail { flex: 1; min-height: 0; }
}
</style>
