// 全局"当前工作区":文件/终端/Git 三个视图共享,选择结果持久化到 localStorage。
import { ref, computed } from 'vue'
import { defineStore } from 'pinia'
import { api, type Workspace } from '@/api/client'

const STORAGE_KEY = 'lr.currentWorkspace'
// 上次访问的目录用 sessionStorage:关掉标签页即遗忘,切换工作区时主动清除。
const LAST_DIR_KEY = 'lr.lastDir'

export const useWorkspaceStore = defineStore('workspace', () => {
  const list = ref<Workspace[]>([])
  const currentId = ref<string | null>(localStorage.getItem(STORAGE_KEY))
  const root = ref('/')
  const loaded = ref(false)

  const current = computed(() => list.value.find((w) => w.id === currentId.value) ?? null)
  // 视图的默认目录:有工作区用它,否则退回 root。
  const currentPath = computed(() => current.value?.path ?? root.value)

  async function load() {
    const [ws, me] = await Promise.all([api.workspaces(), api.me()])
    list.value = ws
    root.value = me.root || '/'
    // 工作区被删掉后清理失效选择;只有一个工作区时自动选中。
    if (currentId.value && !ws.some((w) => w.id === currentId.value)) select(null)
    if (!currentId.value && ws.length === 1) select(ws[0].id)
    loaded.value = true
  }

  function select(id: string | null) {
    const changed = id !== currentId.value
    currentId.value = id
    if (id) localStorage.setItem(STORAGE_KEY, id)
    else localStorage.removeItem(STORAGE_KEY)
    if (changed) clearLastDir()
  }

  function lastDir(): string | null {
    return sessionStorage.getItem(LAST_DIR_KEY)
  }
  function setLastDir(p: string) {
    sessionStorage.setItem(LAST_DIR_KEY, p)
  }
  function clearLastDir() {
    sessionStorage.removeItem(LAST_DIR_KEY)
  }

  // 视图 onMounted 调用:只在没加载过时拉一次,避免每次切页都打接口。
  async function ensure() {
    if (!loaded.value) await load()
  }

  return {
    list, currentId, current, currentPath, root, loaded,
    load, ensure, select, lastDir, setLastDir, clearLastDir,
  }
})
