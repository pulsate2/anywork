<script setup lang="ts">
import { computed } from 'vue'
import AttachmentTag from './AttachmentTag.vue'
import { splitAgentAttachments } from '@/utils/agentAttachments'

const props = defineProps<{ text: string; sessionId?: string }>()
const parts = computed(() => splitAgentAttachments(props.text, props.sessionId))
const attachments = computed(() => parts.value.flatMap((part) => part.attachment ? [part.attachment] : []))
const body = computed(() => {
  const text = parts.value.filter((part) => !part.attachment).map((part) => part.text).join('')
  return attachments.value.length ? text.trim() : text
})
</script>

<template>
  <div class="user-message">
    <div v-if="attachments.length" class="user-message-attachments">
      <AttachmentTag v-for="(attachment, index) in attachments" :key="`${attachment.path}-${index}`"
        :name="attachment.name" :session-id="sessionId" />
    </div>
    <div v-if="body" class="user-message-body">{{ body }}</div>
  </div>
</template>

<style scoped>
.user-message { display: flex; flex-direction: column; gap: 6px; min-width: 0; white-space: normal; }
.user-message-attachments { display: flex; flex-wrap: wrap; gap: 6px; min-width: 0; }
.user-message-body { white-space: pre-wrap; overflow-wrap: anywhere; }
</style>
