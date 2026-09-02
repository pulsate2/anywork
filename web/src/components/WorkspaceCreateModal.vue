<script setup lang="ts">
import { ref, computed } from 'vue'
import {
  NModal, NCard, NForm, NFormItem, NInput, NButton, NSwitch, NTree, NIcon,
  useMessage, type TreeOption,
} from 'naive-ui'
import { FolderOpenOutline } from '@vicons/ionicons5'
import { api } from '@/api/client'
import { useWorkspaceStore } from '@/stores/workspace'

const show = defineModel<boolean>('show', { required: true })
const emit = defineEmits<{ created: [] }>()

const message = useMessage()
const store = useWorkspaceStore()
const name = ref('')
const path = ref('')
const favorite = ref(false)
const loading = ref(false)

// ---- 目录选择器 ----
// 手输和点选共用 path 一个来源:选中态由 path 反推,点选写回 path。
// 所以手输的路径不会被树覆盖,点完也还能接着手改。
const treeOpen = ref(false)
const treeData = ref<TreeOption[]>([])
const treeLoading = ref(false)
const expanded = ref<string[]>([])

const selectedKeys = computed(() => (path.value ? [path.value] : []))

// 只要目录。fsList 返回的 path 已经是 root 内的绝对路径(正斜杠),直接当节点 key,
// 选中后原样交给后端即可。
async function listDirs(dir: string): Promise<TreeOption[]> {
  const items = await api.fsList(dir)
  return items.filter((e) => e.dir).map((e) => ({ key: e.path, label: e.name, isLeaf: false }))
}

async function toggleTree() {
  if (treeOpen.value) {
    treeOpen.value = false
    return
  }
  treeOpen.value = true
  if (treeData.value.length) return
  const key = store.root.length > 1 ? store.root.replace(/\/+$/, '') : store.root || '/'
  treeLoading.value = true
  try {
    treeData.value = [{ key, label: key, isLeaf: false, children: await listDirs(key) }]
    expanded.value = [key]
  } catch (e: any) {
    treeOpen.value = false
    message.error(e?.message || '读取目录失败')
  } finally {
    treeLoading.value = false
  }
}

// remote 懒加载:展开时才拉这一层。失败也要落个空数组,否则该节点会一直停在加载态。
async function loadNode(node: TreeOption) {
  try {
    node.children = await listDirs(node.key as string)
  } catch (e: any) {
    node.children = []
    message.error(e?.message || '读取目录失败')
  }
}

// 再点一次已选中的节点会传空数组,这时保持原值,别把输入框清空。
function onSelect(keys: Array<string | number>) {
  if (keys.length) path.value = String(keys[0])
}

function onExpand(keys: Array<string | number>) {
  expanded.value = keys.map(String)
}

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
        <div class="dir-field">
          <div class="dir-row">
            <n-input v-model:value="path" :placeholder="`绝对路径或相对 ${store.root},如 ${store.root}projects`" />
            <n-button class="dir-browse" :type="treeOpen ? 'primary' : 'default'" :loading="treeLoading"
              title="浏览目录" aria-label="浏览目录" @click="toggleTree">
              <template #icon><n-icon :component="FolderOpenOutline" /></template>
            </n-button>
          </div>
          <!-- expand-on-click + block-line:移动端点一下整行既展开也选中,不用去戳小箭头。 -->
          <div v-if="treeOpen" class="dir-tree">
            <n-tree remote block-line expand-on-click :data="treeData" :on-load="loadNode"
              :selected-keys="selectedKeys" :expanded-keys="expanded"
              @update:selected-keys="onSelect" @update:expanded-keys="onExpand" />
          </div>
        </div>
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
.dir-field { width: 100%; }
.dir-row { display: flex; gap: 6px; }
/* 覆盖全局 .n-button 的 44px 触控下限,否则会比输入框高一截 */
.dir-browse { flex: none; min-height: 34px; }
.dir-tree {
  margin-top: 8px;
  max-height: 240px;
  overflow: auto;
  padding: 4px;
  border: 1px solid rgba(127, 127, 127, 0.2);
  border-radius: var(--lr-radius);
}
</style>
