<script setup lang="ts">
// 工具调用详情弹窗(全局唯一,挂在 AgentView 层):单卡头部、组卡行点击打开。
// 挂在视图层而不是各卡片内部,是因为实时回合里卡片会重组:审批请求到达时
// 卡片从组里拆成单卡,答复后又并回去,卡片一卸载它内部的弹窗就没了 ——
// 表现为「点开就关/点不开」。视图层的弹窗不随卡片重组卸载;父组件按
// toolUseId 传最新的调用对象进来,结果晚到弹窗里也能接着看到。
// 详情内容本体在 ToolDetailBody(与子 agent 过程弹窗共用)。
import { computed } from 'vue'
import { NModal } from 'naive-ui'
import type { AgentToolCall } from '@/api/client'
import ToolDetailBody from './ToolDetailBody.vue'

const props = defineProps<{
  // null = 关。调用对象由父组件解析成"活"引用(重组/回填后仍是最新那份)。
  call: AgentToolCall | null
  // 会话 id:tool_result 的 image 块经 /api/agent/sessions/{id}/files/{name} 取。
  sessionId?: string
}>()

const emit = defineEmits<{ (e: 'close'): void }>()

const show = computed(() => !!props.call)
</script>

<template>
  <n-modal
    :show="show" preset="card" :title="call?.tool || '工具'" class="tool-modal"
    @update:show="(v: boolean) => { if (!v) emit('close') }"
  >
    <ToolDetailBody :call="call" :session-id="sessionId" />
  </n-modal>
</template>
