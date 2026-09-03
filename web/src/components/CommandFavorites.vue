<script setup lang="ts">
// 命令收藏面板:收藏 / 历史 两个页签。
// 点一行 = 只填到终端的输入行,不带回车;要真的跑起来得再点一下"执行" ——
// 这是远程机器,误触执行一条 rm 的代价和填错一行完全不是一个量级。
import { computed, ref, watch } from 'vue'
import {
  NModal, NTabs, NTabPane, NInput, NButton, NEmpty, NPopconfirm, NSwitch, NIcon, useMessage,
} from 'naive-ui'
import { Star, StarOutline } from '@vicons/ionicons5'
import { useCommandStore, HIST_MAX, type CommandFavorite } from '@/stores/commands'

const show = defineModel<boolean>('show', { required: true })
const emit = defineEmits<{ fill: [cmd: string]; run: [cmd: string] }>()

const store = useCommandStore()
const message = useMessage()

const tab = ref<'fav' | 'hist'>('fav')
const keyword = ref('')
// 新增和编辑共用一份草稿:editId 非空就是在改那一条。
const editId = ref<string | null>(null)
const draftCmd = ref('')
const draftNote = ref('')

const shownFavorites = computed(() => {
  const k = keyword.value.trim().toLowerCase()
  if (!k) return store.favorites
  return store.favorites.filter((f) => f.cmd.toLowerCase().includes(k) || f.note.toLowerCase().includes(k))
})
const shownHistory = computed(() => {
  const k = keyword.value.trim().toLowerCase()
  if (!k) return store.history
  return store.history.filter((h) => h.toLowerCase().includes(k))
})

function resetDraft() {
  editId.value = null
  draftCmd.value = ''
  draftNote.value = ''
}

function submitDraft() {
  const cmd = draftCmd.value.trim()
  if (!cmd) {
    message.warning('命令不能为空')
    return
  }
  if (editId.value) {
    if (!store.updateFavorite(editId.value, cmd, draftNote.value)) {
      message.warning('收藏里已经有一条一样的命令')
      return
    }
    message.success('已保存')
  } else {
    if (!store.addFavorite(cmd, draftNote.value)) {
      message.info('这条命令已经在收藏里')
      return
    }
    message.success('已收藏')
  }
  resetDraft()
}

function startEdit(f: CommandFavorite) {
  editId.value = f.id
  draftCmd.value = f.cmd
  draftNote.value = f.note
  tab.value = 'fav'
}

function star(cmd: string) {
  if (store.toggleFavorite(cmd)) message.success('已收藏')
  else message.info('已取消收藏')
}

function fill(cmd: string) {
  emit('fill', cmd)
  show.value = false
}

function run(cmd: string) {
  emit('run', cmd)
  show.value = false
}

// 关掉面板时把搜索和编辑态清掉,下次打开是干净的。
watch(show, (v) => {
  if (!v) {
    keyword.value = ''
    resetDraft()
  }
})
</script>

<template>
  <n-modal v-model:show="show" preset="card" title="命令收藏" class="cmd-modal">
    <n-tabs v-model:value="tab" type="line" size="small" animated>
      <n-tab-pane name="fav" :tab="`收藏 ${store.favorites.length}`">
        <!-- 手动新增(也是编辑时的输入区) -->
        <div class="cmd-add">
          <n-input v-model:value="draftCmd" class="mono" clearable
            placeholder="命令,如 docker compose up -d" @keydown.enter="submitDraft" />
          <div class="cmd-add-row">
            <n-input v-model:value="draftNote" placeholder="备注(可选)" />
            <n-button type="primary" @click="submitDraft">{{ editId ? '保存' : '加入收藏' }}</n-button>
            <n-button v-if="editId" quaternary @click="resetDraft">取消</n-button>
          </div>
        </div>

        <n-input v-if="store.favorites.length > 5" v-model:value="keyword" class="cmd-search"
          size="small" clearable placeholder="搜索收藏" />

        <div v-if="shownFavorites.length" class="cmd-list">
          <div v-for="f in shownFavorites" :key="f.id" class="cmd-row">
            <div class="cmd-main" role="button" tabindex="0" title="填到终端输入行(不执行)"
              @click="fill(f.cmd)" @keydown.enter="fill(f.cmd)">
              <div v-if="f.note" class="cmd-note">{{ f.note }}</div>
              <div class="cmd-text">{{ f.cmd }}</div>
            </div>
            <div class="cmd-ops">
              <n-button class="cmd-btn" size="tiny" type="primary" secondary
                title="填入并回车" @click="run(f.cmd)">执行</n-button>
              <n-button class="cmd-btn" size="tiny" quaternary @click="startEdit(f)">编辑</n-button>
              <n-popconfirm @positive-click="store.removeFavorite(f.id)">
                <template #trigger>
                  <n-button class="cmd-btn" size="tiny" quaternary type="error">删除</n-button>
                </template>
                从收藏里删掉这条?
              </n-popconfirm>
            </div>
          </div>
        </div>
        <n-empty v-else :description="keyword ? '没有匹配的收藏' : '还没有收藏的命令'" class="cmd-empty">
          <template #extra>
            <span class="cmd-hint">上面手动加一条,或者去「历史」里把用过的命令收起来</span>
          </template>
        </n-empty>
      </n-tab-pane>

      <n-tab-pane name="hist" :tab="`历史 ${store.history.length}`">
        <div class="cmd-tools">
          <n-switch size="small" :value="store.recording"
            @update:value="store.setRecording">
            <template #checked>记录中</template>
            <template #unchecked>不记录</template>
          </n-switch>
          <n-popconfirm @positive-click="store.clearHistory">
            <template #trigger>
              <n-button class="cmd-btn" size="tiny" quaternary type="error"
                :disabled="!store.history.length">清空</n-button>
            </template>
            清空全部输入历史?收藏不受影响。
          </n-popconfirm>
          <span class="cmd-hint">只留最近 {{ HIST_MAX }} 条</span>
        </div>

        <n-input v-if="store.history.length > 5" v-model:value="keyword" class="cmd-search"
          size="small" clearable placeholder="搜索历史" />

        <div v-if="shownHistory.length" class="cmd-list">
          <div v-for="h in shownHistory" :key="h" class="cmd-row">
            <div class="cmd-main" role="button" tabindex="0" title="填到终端输入行(不执行)"
              @click="fill(h)" @keydown.enter="fill(h)">
              <div class="cmd-text">{{ h }}</div>
            </div>
            <div class="cmd-ops">
              <n-button class="cmd-btn cmd-star" size="tiny" quaternary
                :title="store.isFavorite(h) ? '取消收藏' : '收藏'" @click="star(h)">
                <template #icon>
                  <n-icon :component="store.isFavorite(h) ? Star : StarOutline"
                    :color="store.isFavorite(h) ? '#e3a008' : undefined" />
                </template>
              </n-button>
              <n-button class="cmd-btn" size="tiny" type="primary" secondary
                title="填入并回车" @click="run(h)">执行</n-button>
            </div>
          </div>
        </div>
        <n-empty v-else
          :description="keyword ? '没有匹配的历史' : '在终端里回车执行过的命令会出现在这里'"
          class="cmd-empty" />
      </n-tab-pane>
    </n-tabs>
    <template #footer>
      <span class="cmd-hint">
        点命令 = 填到终端输入行(不执行),要跑起来点「执行」。收藏和历史都只存在这台浏览器里。
      </span>
    </template>
  </n-modal>
</template>

<style scoped>
.cmd-modal { width: min(620px, calc(100vw - 20px)); }
.cmd-add { display: flex; flex-direction: column; gap: 8px; }
.cmd-add-row { display: flex; align-items: center; gap: 8px; }
.cmd-tools { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.cmd-search { margin-top: 10px; }
.mono :deep(input) { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }

.cmd-list {
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin-top: 10px;
  /* 列表自己滚,页签和输入区留在原地 */
  max-height: 46dvh;
  overflow: auto;
}
.cmd-row {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 10px;
  border-radius: var(--lr-radius);
  border: 1px solid rgba(127, 127, 127, .14);
  background: var(--lr-bg-elevated);
}
.cmd-main { flex: 1; min-width: 0; cursor: pointer; }
.cmd-note { font-size: 12px; color: var(--lr-fg-muted); margin-bottom: 2px; }
.cmd-text {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 13px;
  /* 长命令折两行,再长就省略号:一行一条,列表不会被一条命令撑满整屏。 */
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
  overflow-wrap: anywhere;
}
.cmd-ops { flex: none; display: flex; align-items: center; gap: 4px; }
/* 全局给 .n-button 定了 44px 触控高度,列表里这排小按钮压回来。 */
.cmd-btn { min-height: 28px; height: 28px; }
.cmd-star { width: 28px; padding: 0; }
.cmd-empty { padding: 28px 0; }
.cmd-hint { font-size: 12px; color: var(--lr-fg-muted); }
</style>
