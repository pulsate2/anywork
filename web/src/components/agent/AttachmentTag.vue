<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { NModal, NTag } from 'naive-ui'
import { api } from '@/api/client'

const props = defineProps<{ name: string; sessionId?: string; closable?: boolean; disabled?: boolean }>()
defineEmits<{ (e: 'close'): void }>()

// 与会话附件读取端点支持的图片格式保持一致,原图通过 Cookie 鉴权读取。
const previewUrl = computed(() => props.sessionId && /\.(png|jpe?g|gif|webp)$/i.test(props.name)
  ? api.agentFileUrl(props.sessionId, props.name) : '')
const previewOpen = ref(false)
watch(previewUrl, () => { previewOpen.value = false })
function openPreview(event: MouseEvent) {
  if (!previewUrl.value) return
  event.stopPropagation() // 图片预览不触发用户气泡的复制入口。
  previewOpen.value = true
}
</script>

<template>
  <n-tag class="attachment-tag" :class="{ previewable: !!previewUrl }" type="info" size="small" :bordered="true"
    :color="{ color: 'color-mix(in srgb, var(--lr-accent) 10%, var(--lr-bg-elevated))', textColor: 'var(--lr-accent)', borderColor: 'var(--lr-accent)' }"
    :closable="closable" :disabled="disabled" :title="name" @close="$emit('close')" @click="openPreview">
    <button v-if="previewUrl" type="button" class="attachment-name attachment-preview-trigger"
      :aria-label="`预览图片 ${name}`" @click="openPreview">{{ name }}</button>
    <span v-else class="attachment-name">{{ name }}</span>
  </n-tag>
  <n-modal v-if="previewUrl" v-model:show="previewOpen" preset="card"
    class="attachment-preview" :title="name" style="width: min(960px, calc(100vw - 24px))">
    <img v-if="previewOpen" :src="previewUrl" :alt="name" class="attachment-preview-image" />
  </n-modal>
</template>

<style scoped>
.attachment-tag {
  max-width: 100%; vertical-align: middle;
  height: auto; min-height: 24px; padding-top: 2px; padding-bottom: 2px;
}
.attachment-name { white-space: normal; overflow-wrap: anywhere; line-height: 1.4; }
.attachment-tag :deep(.n-tag__content) { min-width: 0; }
.attachment-tag.previewable { cursor: zoom-in; }
.attachment-preview-trigger {
  appearance: none; border: 0; background: transparent; padding: 0;
  font: inherit; color: inherit; text-align: left; cursor: zoom-in;
}
.attachment-preview-trigger:hover { text-decoration: underline; }
.attachment-preview-trigger:focus-visible { outline: 2px solid var(--lr-accent); outline-offset: 2px; }
.attachment-preview-image { display: block; max-width: 100%; max-height: 70dvh; margin: 0 auto; object-fit: contain; }
</style>
