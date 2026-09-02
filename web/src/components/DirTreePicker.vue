<script setup lang="ts">
// 目录选择器:手输 + 懒加载目录树,两者共用 v-model 一个来源。
// 选中态由值反推,点选写回值,所以手输的路径不会被树覆盖,点完也还能接着手改。
import { ref, computed, onMounted } from 'vue'
import { NInput, NButton, NTree, NIcon, useMessage, type TreeOption } from 'naive-ui'
import { FolderOpenOutline } from '@vicons/ionicons5'
import { api } from '@/api/client'
import { useWorkspaceStore } from '@/stores/workspace'

const path = defineModel<string>({ required: true })
const props = defineProps<{ placeholder?: string; defaultOpen?: boolean }>()

const message = useMessage()
const store = useWorkspaceStore()
const treeOpen = ref(false)
const treeData = ref<TreeOption[]>([])
const treeLoading = ref(false)
const expanded = ref<string[]>([])

const selectedKeys = computed(() => (path.value ? [trimSlash(path.value)] : []))

function trimSlash(p: string) {
  return p.length > 1 ? p.replace(/\/+$/, '') : p
}

// 树根只能是 root:后端 Resolve 不放行边界之外的路径,再往上列也是白列。
function rootKey() {
  return trimSlash(store.root || '/')
}

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
  await openTree()
}

async function openTree() {
  treeOpen.value = true
  if (treeData.value.length) return
  const key = rootKey()
  treeLoading.value = true
  try {
    treeData.value = [{ key, label: key, isLeaf: false, children: await listDirs(key) }]
    expanded.value = [key]
    await expandTo(path.value)
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

// 已经有值就逐级展开到它,省掉每次从根一层层点下去。
// 某一段不存在(手输的、或已被删)就停在那,前面展开的部分保留。
async function expandTo(target: string) {
  const base = rootKey()
  const t = trimSlash(target || '')
  if (!t || (t !== base && !t.startsWith(base === '/' ? '/' : base + '/'))) return
  const keys = [base]
  let node = treeData.value[0]
  let acc = base
  for (const seg of t.slice(base.length).split('/').filter(Boolean)) {
    acc = (acc === '/' ? '' : acc) + '/' + seg
    const next = (node.children as TreeOption[] | undefined)?.find((c) => c.key === acc)
    if (!next) break
    if (!next.children) await loadNode(next)
    node = next
    keys.push(acc)
  }
  expanded.value = keys
}

// 再点一次已选中的节点会传空数组,这时保持原值,别把输入框清空。
function onSelect(keys: Array<string | number>) {
  if (keys.length) path.value = String(keys[0])
}

function onExpand(keys: Array<string | number>) {
  expanded.value = keys.map(String)
}

onMounted(() => {
  if (props.defaultOpen) openTree()
})
</script>

<template>
  <div class="dir-field">
    <div class="dir-row">
      <n-input v-model:value="path" :placeholder="placeholder || '目录路径'" />
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
</template>

<style scoped>
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
