<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { NButton, NEmpty, NTag, NSpin, useDialog, useMessage } from 'naive-ui'
import { AddOutline, FolderOpenOutline, TrashOutline, CheckmarkCircle } from '@vicons/ionicons5'
import { NIcon } from 'naive-ui'
import { api, type Workspace } from '@/api/client'
import { useWorkspaceStore } from '@/stores/workspace'
import WorkspaceCreateModal from '@/components/WorkspaceCreateModal.vue'

const message = useMessage()
const dialog = useDialog()
const router = useRouter()
const store = useWorkspaceStore()
const workspaces = ref<Workspace[]>([])
const loading = ref(true)
const showCreate = ref(false)

async function load() {
  loading.value = true
  try {
    await store.load()
    workspaces.value = store.list
  } catch (e) {
    message.error('加载工作区失败')
  } finally {
    loading.value = false
  }
}

function onCreated() {
  showCreate.value = false
  load()
}

// 选中即切换:文件/终端/Git 都跟着这个工作区走。
function pick(ws: Workspace) {
  store.select(ws.id)
  message.success(`已切换到「${ws.name}」`)
}

function openFiles(ws: Workspace) {
  store.select(ws.id)
  router.push({ name: 'files' })
}

function remove(ws: Workspace) {
  dialog.warning({
    title: '删除工作区',
    content: `确定删除「${ws.name}」?仅移除书签,不会删除任何文件。`,
    positiveText: '删除',
    negativeText: '取消',
    onPositiveClick: async () => {
      try {
        await api.deleteWorkspace(ws.id)
        message.success('已删除')
        load()
      } catch {
        message.error('删除失败')
      }
    },
  })
}

onMounted(load)
</script>

<template>
  <div class="page-content">
    <div class="home-header">
      <div>
        <h2>工作区</h2>
        <p class="sub">选择一个目录开始工作</p>
      </div>
      <n-button type="primary" @click="showCreate = true">
        <template #icon><n-icon :component="AddOutline" /></template>
        新建
      </n-button>
    </div>

    <n-spin :show="loading">
      <div v-if="workspaces.length" class="ws-list">
        <div
          v-for="ws in workspaces"
          :key="ws.id"
          class="ws-card"
          :class="{ active: ws.id === store.currentId }"
          role="button"
          tabindex="0"
          @click="pick(ws)"
          @keydown.enter="pick(ws)"
        >
          <div class="ws-icon"><n-icon :component="FolderOpenOutline" /></div>
          <div class="ws-main">
            <div class="ws-name">
              <span class="ws-name-text">{{ ws.name }}</span>
              <n-tag v-if="ws.id === store.currentId" size="small" :bordered="false" type="success">
                <template #icon><n-icon :component="CheckmarkCircle" /></template>
                当前
              </n-tag>
              <n-tag v-if="ws.favorite" size="small" :bordered="false" type="warning">收藏</n-tag>
            </div>
            <div class="ws-path" :title="ws.path">{{ ws.path }}</div>
          </div>
          <n-button class="ws-act" quaternary size="small" @click.stop="openFiles(ws)">打开</n-button>
          <n-button class="ws-del" quaternary circle type="error" aria-label="删除工作区" @click.stop="remove(ws)">
            <template #icon><n-icon :component="TrashOutline" /></template>
          </n-button>
        </div>
      </div>
      <n-empty v-else description="还没有工作区" style="padding: 40px 0" />
    </n-spin>

    <workspace-create-modal v-model:show="showCreate" @created="onCreated" />
  </div>
</template>

<style scoped>
.home-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 16px;
}
.home-header h2 { margin: 0; font-size: 20px; }
.sub { color: var(--lr-fg-muted); margin: 4px 0 0; font-size: 13px; }
.ws-list { display: flex; flex-direction: column; gap: 10px; }
.ws-card {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px 14px;
  background: var(--lr-bg-elevated);
  border: 1px solid rgba(127, 127, 127, 0.14);
  border-radius: var(--lr-radius);
  cursor: pointer;
}
.ws-card.active { border-color: var(--lr-accent); box-shadow: 0 0 0 1px var(--lr-accent) inset; }
.ws-icon {
  flex: none;
  width: 38px;
  height: 38px;
  display: grid;
  place-items: center;
  border-radius: 9px;
  background: rgba(37, 99, 235, 0.1);
  color: var(--lr-accent);
  font-size: 20px;
}
.ws-main { flex: 1; min-width: 0; }
.ws-name {
  font-weight: 600;
  font-size: 15px;
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}
.ws-name-text { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.ws-path {
  color: var(--lr-fg-muted);
  font-size: 12px;
  font-family: ui-monospace, monospace;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  margin-top: 2px;
}
/* 覆盖全局 .n-button 的 44px 触控下限,否则圆形按钮会被拉成椭圆 */
.ws-del { flex: none; width: 36px; height: 36px; min-height: 36px; }
.ws-act { flex: none; min-height: 32px; }
</style>
