<script setup lang="ts">
// 连续同类只读工具的聚合组卡(hapi buildVisibleChatBlocks 同款思路):
// 2 张以上连续的"可组"工具合一张卡,一行摘要(数量 + 主意图 + 涉及目标),
// 点开是组内每张工具的单行。写/改类(Edit/Write/Bash/ApplyPatch/CodexDiff)
// 与待审批、TodoWrite、AskUserQuestion 不参与聚合 —— 它们每张都值得单看。
// 组内单行可再点:弹窗看该次的参数与结果(同 ToolCallCard 详情,组卡版)。
import { computed, ref } from 'vue'
import { NModal } from 'naive-ui'
import { api, type AgentToolCall } from '@/api/client'

const props = defineProps<{
  calls: AgentToolCall[]
  // 会话 id:tool_result 的 image 块经 /api/agent/sessions/{id}/files/{name} 取。
  sessionId?: string
}>()

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

// ---- 组内单条的详情弹窗 ----
// 详情 = 参数 + 结果 + 结果图片,组内工具全是只读类,没有 diff 分支。
const detail = ref<AgentToolCall | null>(null)
const detailOpen = computed({
  get: () => !!detail.value,
  set: (v: boolean) => { if (!v) detail.value = null },
})

// 参数展示:可解析的 JSON 缩进两格(键值一目了然),截断的非法 JSON 原样贴。
const detailArgs = computed(() => {
  const raw = detail.value?.args
  if (!raw) return ''
  try {
    const v = JSON.parse(raw)
    if (v && typeof v === 'object') return JSON.stringify(v, null, 2)
  } catch { /* 截断的 JSON:原样 */ }
  return raw
})

// 该次结果里的图片缩略图地址(有会话 id 且带图才有)。
const detailImages = computed<string[]>(() =>
  props.sessionId && detail.value?.images?.length
    ? detail.value.images.map((n) => api.agentFileUrl(props.sessionId!, n))
    : [],
)
const lightbox = ref<string | null>(null)
const lightboxOpen = computed({
  get: () => !!lightbox.value,
  set: (v: boolean) => { if (!v) lightbox.value = null },
})
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
      <button v-for="(c, i) in calls" :key="c.toolUseId || i" type="button" class="group-item" @click="detail = c">
        <span class="item-dot" :class="c.state" />
        <span class="item-name">{{ c.tool }}</span>
        <span v-if="c.result" class="item-lines">{{ c.result.trim().split('\n').length }} 行</span>
        <span class="item-state">{{ c.state === 'error' ? '出错' : c.state === 'running' ? '运行中' : '完成' }}</span>
      </button>
    </div>

    <!-- 组内单条详情:参数 + 结果 + 图片。弹窗样式(.tool-modal 系列)复用
         ToolCallCard 非 scoped 块的全局规则 —— 两个组件总在 AgentView 一起挂载 -->
    <n-modal v-model:show="detailOpen" preset="card" :title="detail?.tool || '工具'" class="tool-modal">
      <div class="tool-detail">
        <pre v-if="detailArgs" class="tool-block">{{ detailArgs }}</pre>
        <pre v-if="detail?.result" class="tool-block result">{{ detail.result }}</pre>
        <div v-else-if="detail?.state === 'running'" class="tool-wait">等待结果…</div>
        <div v-if="detailImages.length" class="tool-imgs">
          <img v-for="(u, i) in detailImages" :key="u" :src="u" :alt="`图片 ${i + 1}`" loading="lazy" @click="lightbox = u" />
        </div>
      </div>
    </n-modal>
    <!-- 点亮的原图:独立小弹窗,Cookie 认同所以 <img src> 直连 -->
    <n-modal v-model:show="lightboxOpen" preset="card" class="tool-modal" title="图片">
      <img v-if="lightbox" :src="lightbox" class="tool-lightbox" alt="图片" />
    </n-modal>
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
.item-name { flex: none; font-family: ui-monospace, monospace; color: var(--lr-fg); }
.item-lines { min-width: 0; flex: 1; color: var(--lr-fg-muted); }
.item-state { flex: none; color: var(--lr-fg-muted); font-size: 11px; }
</style>
