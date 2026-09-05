<script setup lang="ts">
import { ref, computed } from 'vue'
import { NModal, NForm, NFormItem, NInput, NButton, NSwitch, useMessage, useDialog } from 'naive-ui'
import { api, ApiError, WORKSPACE_DIR_MISSING } from '@/api/client'
import { useWorkspaceStore } from '@/stores/workspace'
import DirTreePicker from './DirTreePicker.vue'

const show = defineModel<boolean>('show', { required: true })
const emit = defineEmits<{ created: [] }>()

const message = useMessage()
const dialog = useDialog()
const store = useWorkspaceStore()
// root 只有在它本身就是 "/" 时才带尾斜杠,直接拼会拼出 /root/anyworkprojects。
const example = computed(() => store.root.replace(/\/+$/, '') + '/projects')
const name = ref('')
const path = ref('')
const favorite = ref(false)
const loading = ref(false)

// create=true 时后端会把不存在的目录一并建出来。第一次提交不带,让后端回 428,
// 问过用户再原样重放 —— 路径敲错一个字母不该凭空多出一个空目录。
async function submit(create = false) {
  if (!name.value.trim() || !path.value.trim()) {
    message.warning('名称和路径不能为空')
    return
  }
  loading.value = true
  try {
    await api.createWorkspace({ name: name.value.trim(), path: path.value.trim(), favorite: favorite.value, create })
    message.success(create ? '目录已创建,工作区已建好' : '已创建')
    name.value = ''
    path.value = ''
    favorite.value = false
    emit('created')
  } catch (e: any) {
    // 目录不存在:问一句要不要建,填过的表单原样留着,取消就什么都没发生。
    if (e instanceof ApiError && e.status === WORKSPACE_DIR_MISSING) {
      askCreateDir()
      return
    }
    message.error(e?.message || '创建失败')
  } finally {
    loading.value = false
  }
}

function askCreateDir() {
  dialog.warning({
    title: '目录不存在',
    content: `「${path.value.trim()}」还不存在,要现在创建它吗?(缺失的上级目录会一并建出)`,
    positiveText: '创建并继续',
    negativeText: '取消',
    onPositiveClick: () => submit(true),
  })
}
</script>

<template>
  <n-modal v-model:show="show" preset="card" title="新建工作区" style="width: 90%; max-width: 420px">
    <n-form label-placement="top">
      <n-form-item label="名称">
        <n-input v-model:value="name" placeholder="如:我的项目" />
      </n-form-item>
      <n-form-item label="目录路径">
        <dir-tree-picker v-model="path" :placeholder="`绝对路径或相对 ${store.root},如 ${example}`" />
      </n-form-item>
      <n-form-item label="收藏">
        <n-switch v-model:value="favorite" />
      </n-form-item>
    </n-form>
    <template #footer>
      <div class="modal-footer">
        <n-button @click="show = false">取消</n-button>
        <!-- submit() 必须带括号:直接写 @click="submit" 会把 MouseEvent 当成 create 传进去。 -->
        <n-button type="primary" :loading="loading" @click="submit()">创建</n-button>
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
