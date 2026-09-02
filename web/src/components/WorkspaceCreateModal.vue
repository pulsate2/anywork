<script setup lang="ts">
import { ref } from 'vue'
import { NModal, NForm, NFormItem, NInput, NButton, NSwitch, useMessage } from 'naive-ui'
import { api } from '@/api/client'
import { useWorkspaceStore } from '@/stores/workspace'
import DirTreePicker from './DirTreePicker.vue'

const show = defineModel<boolean>('show', { required: true })
const emit = defineEmits<{ created: [] }>()

const message = useMessage()
const store = useWorkspaceStore()
const name = ref('')
const path = ref('')
const favorite = ref(false)
const loading = ref(false)

async function submit() {
  if (!name.value.trim() || !path.value.trim()) {
    message.warning('名称和路径不能为空')
    return
  }
  loading.value = true
  try {
    await api.createWorkspace({ name: name.value.trim(), path: path.value.trim(), favorite: favorite.value })
    message.success('已创建')
    name.value = ''
    path.value = ''
    favorite.value = false
    emit('created')
  } catch (e: any) {
    message.error(e?.message || '创建失败')
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <n-modal v-model:show="show" preset="card" title="新建工作区" style="width: 90%; max-width: 420px">
    <n-form label-placement="top">
      <n-form-item label="名称">
        <n-input v-model:value="name" placeholder="如:我的项目" />
      </n-form-item>
      <n-form-item label="目录路径">
        <dir-tree-picker v-model="path"
          :placeholder="`绝对路径或相对 ${store.root},如 ${store.root}projects`" />
      </n-form-item>
      <n-form-item label="收藏">
        <n-switch v-model:value="favorite" />
      </n-form-item>
    </n-form>
    <template #footer>
      <div class="modal-footer">
        <n-button @click="show = false">取消</n-button>
        <n-button type="primary" :loading="loading" @click="submit">创建</n-button>
      </div>
    </template>
  </n-modal>
</template>

<style scoped>
.modal-footer {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
</style>
