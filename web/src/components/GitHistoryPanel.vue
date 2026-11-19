<script setup lang="ts">
import { computed, nextTick, onUnmounted, ref, watch } from 'vue'
import { NButton, NEmpty, NIcon, NInput, NSelect, NSpin } from 'naive-ui'
import {
  SearchOutline, GitBranchOutline, GitCommitOutline, RefreshOutline,
  ArrowUndoOutline, CopyOutline, CheckmarkOutline,
} from '@vicons/ionicons5'
import {
  api, type GitCommit, type GitCommitAuthor, type GitCommitDetail, type GitCommitFile,
  type GitBranchList,
} from '@/api/client'
import { buildCommitGraph, commitDate, GIT_LOG_PAGE, type GitHistoryState } from '@/utils/gitHistory'
import { fileIcon } from '@/utils/fileIcon'

const props = defineProps<{
  repoPath: string
  currentBranch?: string
  revision: number
  initialState?: GitHistoryState
}>()
const emit = defineEmits<{
  'open-file': [file: string, commit: GitCommit, state: GitHistoryState]
  revert: [commit: GitCommit]
}>()

const search = ref(props.initialState?.search || '')
const branch = ref(props.initialState?.branch || '')
const author = ref(props.initialState?.author || '')
const appliedSearch = ref(search.value.trim())
const appliedAuthor = ref(author.value)
const commits = ref<GitCommit[]>([])
const selectedHash = ref('')
const detail = ref<GitCommitDetail | null>(null)
const branches = ref<GitBranchList | null>(null)
const authors = ref<GitCommitAuthor[]>([])
const logLoading = ref(false)
const moreLoading = ref(false)
const detailLoading = ref(false)
const hasMore = ref(false)
const logError = ref('')
const detailError = ref('')
const choicesError = ref(false)
const copied = ref(false)
const copyError = ref('')
const scrollEl = ref<HTMLElement | null>(null)
const listEl = ref<HTMLElement | null>(null)
let logRequest = 0
let detailRequest = 0
let choicesRequest = 0
let searchTimer: ReturnType<typeof setTimeout> | undefined
let copyTimer: ReturnType<typeof setTimeout> | undefined
let preferredHash = props.initialState?.selectedHash || ''
let restoreScroll = Math.max(0, props.initialState?.scrollTop || 0)
const initialLimit = Math.max(GIT_LOG_PAGE, props.initialState?.limit || GIT_LOG_PAGE)

const hasFilters = computed(() => !!(search.value.trim() || branch.value || author.value))
const graphFiltered = computed(() => !!(appliedSearch.value || appliedAuthor.value))
const graph = computed(() => buildCommitGraph(commits.value, !graphFiltered.value))
const selectedCommit = computed(() => detail.value || commits.value.find(c => c.hash === selectedHash.value))
const selectedIndex = computed(() => commits.value.findIndex(c => c.hash === selectedHash.value))
const totals = computed(() => (detail.value?.files || []).reduce(
  (sum, file) => ({ add: sum.add + Math.max(0, file.add), del: sum.del + Math.max(0, file.del) }),
  { add: 0, del: 0 },
))

// 接口读取失败时仍能从已加载的行补一些选项,且保留返回文件页前选中的筛选值。
const branchOptions = computed(() => {
  const names = new Set([
    ...(branches.value?.local || []).map(b => b.name),
    ...(branches.value?.remote || []).map(b => b.name),
    ...commits.value.flatMap(c => (c.refs || []).filter(r => !r.startsWith('tag: '))),
    ...(branch.value ? [branch.value] : []),
  ])
  return [{ label: '全部分支', value: '' }, ...[...names].map(name => ({ label: name, value: name }))]
})
const authorOptions = computed(() => {
  const byEmail = new Map(authors.value.map(a => [a.email, a.name]))
  commits.value.forEach(c => { if (c.email && !byEmail.has(c.email)) byEmail.set(c.email, c.author || c.email) })
  if (author.value && !byEmail.has(author.value)) byEmail.set(author.value, author.value)
  const names = [...byEmail.values()]
  return [
    { label: '全部作者', value: '' },
    ...[...byEmail].map(([email, name]) => ({
      label: names.filter(n => n === name).length > 1 ? name + ' (' + email + ')' : name,
      value: email,
    })),
  ]
})

async function loadChoices() {
  const request = ++choicesRequest
  const path = props.repoPath
  const [branchResult, authorResult] = await Promise.allSettled([api.gitBranches(path), api.gitAuthors(path)])
  if (request !== choicesRequest) return
  branches.value = branchResult.status === 'fulfilled' ? branchResult.value : null
  authors.value = authorResult.status === 'fulfilled' ? authorResult.value : []
  choicesError.value = branchResult.status === 'rejected' || authorResult.status === 'rejected'
}

async function loadLog(reset = true, preserve = false, count = GIT_LOG_PAGE) {
  if (!reset && (logLoading.value || moreLoading.value || !hasMore.value)) return
  const request = ++logRequest
  const path = props.repoPath
  const options = { ref: branch.value, search: search.value.trim(), author: author.value }
  const skip = reset ? 0 : commits.value.length
  const keepHash = preferredHash || (preserve ? selectedHash.value : '')
  logError.value = ''
  if (reset) {
    logLoading.value = true
    moreLoading.value = false
    appliedSearch.value = options.search
    appliedAuthor.value = options.author
    if (!preserve && !preferredHash) {
      commits.value = []
      selectedHash.value = ''
      hasMore.value = false
    }
  } else {
    moreLoading.value = true
  }
  try {
    const rows = await api.gitLog(path, count, skip, options)
    if (request !== logRequest) return
    if (reset) {
      commits.value = rows
      selectedHash.value = rows.length ? (rows.some(c => c.hash === keepHash) ? keepHash : rows[0].hash) : ''
      preferredHash = ''
    } else {
      const existing = new Set(commits.value.map(c => c.hash))
      commits.value.push(...rows.filter(c => !existing.has(c.hash)))
    }
    hasMore.value = rows.length >= count
    await nextTick()
    if (reset && scrollEl.value) {
      scrollEl.value.scrollTop = preserve ? restoreScroll : 0
      restoreScroll = 0
    }
  } catch (e: any) {
    if (request === logRequest) logError.value = e?.message || '读取提交历史失败'
  } finally {
    if (request === logRequest) {
      logLoading.value = false
      moreLoading.value = false
    }
  }
}

function applyFilters() {
  clearTimeout(searchTimer)
  preferredHash = ''
  loadLog()
}
function updateSearch(value: string) {
  search.value = value
  clearTimeout(searchTimer)
  if (!value.trim()) applyFilters()
  else searchTimer = setTimeout(applyFilters, 280)
}
function updateBranch(value: string) { branch.value = value; applyFilters() }
function updateAuthor(value: string) { author.value = value; applyFilters() }
function clearFilters() {
  search.value = ''
  branch.value = ''
  author.value = ''
  applyFilters()
}
function refresh() {
  clearTimeout(searchTimer)
  restoreScroll = scrollEl.value?.scrollTop || 0
  loadLog(true, true, Math.max(GIT_LOG_PAGE, commits.value.length))
  loadChoices()
}

async function loadDetail() {
  const request = ++detailRequest
  const hash = selectedHash.value
  detail.value = null
  detailError.value = ''
  copyError.value = ''
  copied.value = false
  detailLoading.value = !!hash
  if (!hash) return
  try {
    const result = await api.gitCommitDetail(props.repoPath, hash)
    if (request === detailRequest) detail.value = result
  } catch (e: any) {
    if (request === detailRequest) detailError.value = e?.message || '读取提交详情失败'
  } finally {
    if (request === detailRequest) detailLoading.value = false
  }
}

async function reveal(hash: string) {
  selectedHash.value = hash
  await nextTick()
  const row = listEl.value?.querySelector<HTMLButtonElement>('[data-hash="' + hash + '"]')
  row?.focus({ preventScroll: true })
  row?.scrollIntoView({ block: 'nearest' })
}
function locateParent(hash: string) {
  if (commits.value.some(c => c.hash === hash)) {
    reveal(hash)
  } else {
    // 父提交可能在尚未加载的页里,用提交号直接定位而不是猜测一条连线。
    search.value = hash
    branch.value = ''
    author.value = ''
    applyFilters()
  }
}
function onListKeydown(event: KeyboardEvent) {
  if (!commits.value.length) return
  const current = Math.max(0, selectedIndex.value)
  let target: number
  switch (event.key) {
    case 'ArrowDown': target = Math.min(commits.value.length - 1, current + 1); break
    case 'ArrowUp': target = Math.max(0, current - 1); break
    case 'Home': target = 0; break
    case 'End': target = commits.value.length - 1; break
    default: return
  }
  event.preventDefault()
  reveal(commits.value[target].hash)
}

function snapshot(): GitHistoryState {
  return {
    search: appliedSearch.value, branch: branch.value, author: appliedAuthor.value, selectedHash: selectedHash.value,
    limit: Math.max(GIT_LOG_PAGE, commits.value.length), scrollTop: scrollEl.value?.scrollTop || 0,
  }
}
function openFile(file: GitCommitFile) {
  if (detail.value) emit('open-file', file.path, detail.value, snapshot())
}
async function copyHash() {
  copyError.value = ''
  try {
    await navigator.clipboard.writeText(selectedCommit.value?.hash || selectedHash.value)
    copied.value = true
    clearTimeout(copyTimer)
    copyTimer = setTimeout(() => { copied.value = false }, 1800)
  } catch {
    copyError.value = '复制失败,可长按或选中提交号手动复制。'
  }
}

const colors = ['#3b82f6', '#16a34a', '#d9468d', '#9b6de3', '#d99022', '#16a6b6']
const avatarColors = ['#da8735', '#538acb', '#8a6bc4', '#489780', '#bf687e']
function avatarColor(commit: GitCommit) {
  const key = commit.email || commit.author || ''
  return avatarColors[[...key].reduce((sum, char) => sum + char.charCodeAt(0), 0) % avatarColors.length]
}
function initial(commit: GitCommit) { return [...(commit.author || '?')][0].toUpperCase() }
function fileName(path: string) { return path.slice(path.lastIndexOf('/') + 1) }
function fileDir(path: string) { return path.slice(0, Math.max(0, path.lastIndexOf('/'))) }
function statBlocks(file: GitCommitFile) {
  const total = file.add + file.del
  if (total <= 0) return []
  const count = Math.min(5, total)
  let adds = Math.round(file.add / total * count)
  if (file.add && file.del) adds = Math.max(1, Math.min(count - 1, adds))
  return Array.from({ length: count }, (_, i) => i < adds ? 'add' : 'del')
}

watch(selectedHash, loadDetail)
watch(() => [props.repoPath, props.revision] as const, (value, old) => {
  clearTimeout(searchTimer)
  if (old && value[0] !== old[0]) {
    search.value = ''
    branch.value = ''
    author.value = ''
    preferredHash = ''
    selectedHash.value = ''
    commits.value = []
    detail.value = null
    branches.value = null
    authors.value = []
    restoreScroll = 0
  } else if (old) {
    restoreScroll = scrollEl.value?.scrollTop || 0
  }
  loadLog(true, true, old ? Math.max(GIT_LOG_PAGE, commits.value.length) : initialLimit)
  loadChoices()
}, { immediate: true })
onUnmounted(() => {
  logRequest++
  detailRequest++
  choicesRequest++
  clearTimeout(searchTimer)
  clearTimeout(copyTimer)
})
</script>

<template>
  <section class="history-panel" aria-label="Git 日志" :style="{ '--history-graph-width': graph.width + 'px' }">
    <div class="history-toolbar">
      <n-input class="history-search" size="small" :value="search" clearable
        placeholder="按说明或提交号筛选" aria-label="搜索提交说明或提交号"
        @update:value="updateSearch" @keydown.enter="applyFilters">
        <template #prefix><n-icon :component="SearchOutline" /></template>
      </n-input>
      <div class="history-filter history-branch-filter" title="只筛选提交历史,不会切换工作区分支">
        <n-icon :component="GitBranchOutline" />
        <n-select class="history-branch-select" :value="branch" :options="branchOptions" size="small" filterable
          aria-label="筛选分支" @update:value="updateBranch" />
      </div>
      <div class="history-filter history-author-filter">
        <span class="history-filter-label">作者</span>
        <n-select class="history-author-select" :value="author" :options="authorOptions" size="small" filterable
          aria-label="筛选作者" @update:value="updateAuthor" />
      </div>
      <n-button class="history-clear" text size="tiny" :disabled="!hasFilters" @click="clearFilters">清除筛选</n-button>
      <span class="history-count" aria-live="polite">{{ hasMore ? '已加载 ' : '' }}{{ commits.length }} 个提交</span>
      <n-button class="history-refresh" quaternary size="tiny" :disabled="logLoading || moreLoading"
        title="刷新提交历史" aria-label="刷新提交历史" @click="refresh">
        <template #icon><n-icon :component="RefreshOutline" /></template>
      </n-button>
    </div>

    <div class="history-body">
      <div class="history-list-pane">
        <div v-if="logError" class="history-error" role="alert">
          <span>{{ logError }}</span><n-button text size="tiny" @click="refresh">重试</n-button>
        </div>
        <n-spin :show="logLoading" size="small">
          <div ref="scrollEl" class="history-scroll">
            <div v-if="!commits.length" class="history-empty">
              <span v-if="logLoading" class="history-muted">读取提交历史…</span>
              <template v-else>
                <n-empty :description="hasFilters ? '没有匹配的提交' : '还没有提交'" size="small" />
                <n-button v-if="hasFilters" text size="small" @click="clearFilters">清除筛选</n-button>
              </template>
            </div>
            <div v-else ref="listEl" class="history-rows" role="listbox" aria-label="提交列表"
              :tabindex="selectedIndex < 0 ? 0 : -1" @keydown="onListKeydown">
              <button v-for="(commit, index) in commits" :key="commit.hash" type="button" class="history-row"
                :class="{ selected: selectedHash === commit.hash }" :data-hash="commit.hash" role="option"
                :aria-selected="selectedHash === commit.hash" :tabindex="selectedHash === commit.hash ? 0 : -1"
                :title="commit.subject + '\n' + commit.hash" @click="selectedHash = commit.hash">
                <span class="history-graph-cell" aria-hidden="true">
                  <svg class="history-graph" :viewBox="'0 0 ' + graph.width + ' 36'" preserveAspectRatio="none" :width="graph.width">
                    <path v-for="(path, i) in graph.rows[index].paths" :key="i" :d="path.d" :stroke="colors[path.color]"
                      fill="none" stroke-width="1.7" />
                  </svg>
                  <span class="history-node" :class="{ merge: graph.rows[index].merge }"
                    :style="{ left: graph.rows[index].x + 'px', '--graph-color': colors[graph.rows[index].color] }" />
                </span>
                <span class="history-summary">
                  <span class="history-subject-line">
                    <span class="history-subject">{{ commit.subject || '(无提交说明)' }}</span>
                    <span v-if="commit.refs?.length" class="history-refs">
                      <span v-for="name in commit.refs" :key="name" class="history-ref"
                        :class="{ current: name === currentBranch, tag: name.startsWith('tag: ') }" :title="name">
                        {{ name.replace(/^tag: /, '') }}
                      </span>
                    </span>
                  </span>
                  <span class="history-mobile-meta">
                    <span class="history-avatar" :style="{ background: avatarColor(commit) }">{{ initial(commit) }}</span>
                    <span class="history-mobile-author">{{ commit.author || '未知作者' }}</span>
                    <span class="history-mobile-hash">{{ commit.short }}</span>
                    <time :datetime="commit.date">{{ commitDate(commit.date, true) }}</time>
                  </span>
                </span>
                <span class="history-author" :title="commit.author + ' <' + commit.email + '>'">
                  <span class="history-avatar" :style="{ background: avatarColor(commit) }">{{ initial(commit) }}</span>
                  <span>{{ commit.author || '未知作者' }}</span>
                </span>
                <time class="history-date" :datetime="commit.date" :title="commit.date">{{ commitDate(commit.date, true) }}</time>
              </button>
            </div>
          </div>
        </n-spin>
        <div class="history-footer">
          <span v-if="graphFiltered" class="history-muted" title="筛选隐藏了中间提交,因此不绘制跨越隐藏提交的连线">仅连接已显示的父子提交</span>
          <span v-else class="history-muted">{{ choicesError ? '部分筛选项读取失败,可刷新重试' : '分支图 · 按时间与拓扑排序' }}</span>
          <n-button v-if="hasMore" text size="tiny" :loading="moreLoading" :disabled="logLoading"
            @click="loadLog(false)">加载更多</n-button>
          <span v-else-if="commits.length" class="history-muted">全部 {{ commits.length }} 条</span>
        </div>
      </div>

      <aside class="history-detail" aria-label="提交详情" :aria-busy="detailLoading">
        <div v-if="!selectedHash" class="history-detail-empty">
          <n-icon :component="GitCommitOutline" :size="30" />
          <span>选择提交查看详情</span>
        </div>
        <template v-else>
          <h3 class="history-detail-subject">{{ selectedCommit?.subject || selectedHash.slice(0, 7) }}</h3>
          <div v-if="selectedCommit?.refs?.length" class="history-detail-refs">
            <span v-for="name in selectedCommit.refs" :key="name" class="history-ref"
              :class="{ current: name === currentBranch, tag: name.startsWith('tag: ') }">{{ name.replace(/^tag: /, '') }}</span>
          </div>
          <template v-if="selectedCommit">
            <div class="history-detail-author">
              <span class="history-avatar" :style="{ background: avatarColor(selectedCommit) }">{{ initial(selectedCommit) }}</span>
              <span>{{ selectedCommit.author || '未知作者' }}</span>
              <span class="history-muted history-email">{{ selectedCommit.email }}</span>
            </div>
            <time class="history-detail-date" :datetime="selectedCommit.date" :title="selectedCommit.date">
              {{ commitDate(selectedCommit.date) }}
            </time>
          </template>
          <div class="history-detail-meta">
            <span class="history-muted">提交</span>
            <code class="history-full-hash" :title="selectedCommit?.hash || selectedHash">{{ selectedCommit?.hash || selectedHash }}</code>
            <n-button class="history-copy" quaternary size="tiny" :title="copied ? '已复制' : '复制完整提交号'"
              :aria-label="copied ? '已复制' : '复制完整提交号'" @click="copyHash">
              <template #icon><n-icon :component="copied ? CheckmarkOutline : CopyOutline" /></template>
            </n-button>
          </div>
          <div v-if="selectedCommit?.parents?.length" class="history-parents">
            <span class="history-muted">父提交</span>
            <button v-for="parent in selectedCommit.parents" :key="parent" type="button" class="history-parent"
              :title="'查看父提交 ' + parent" @click="locateParent(parent)">{{ parent.slice(0, 7) }}</button>
          </div>
          <div v-if="copyError" class="history-error" role="alert">{{ copyError }}</div>
          <div v-if="detailError" class="history-error" role="alert">
            <span>{{ detailError }}</span><n-button text size="tiny" @click="loadDetail">重试</n-button>
          </div>
          <div v-else-if="detailLoading" class="history-detail-loading"><n-spin size="small" /><span>读取文件改动…</span></div>
          <template v-else-if="detail">
            <p v-if="detail.body" class="history-commit-body">{{ detail.body }}</p>
            <div class="history-files-header">
              <span>更改了 <strong>{{ detail.files.length }}</strong> 个文件</span>
              <span class="history-add">+{{ totals.add }}</span>
              <span class="history-del">−{{ totals.del }}</span>
            </div>
            <p v-if="detail.parents.length > 1" class="history-merge-note">合并提交 · 相对第一父提交的改动</p>
            <div v-if="detail.files.length" class="history-files">
              <button v-for="file in detail.files" :key="file.path" type="button" class="history-file"
                :title="(file.orig ? file.orig + ' → ' : '') + file.path + ' · 查看差异'" @click="openFile(file)">
                <span class="history-file-icon">
                  <n-icon :component="fileIcon(file.path).icon" :color="fileIcon(file.path).color" />
                </span>
                <span class="history-file-path">
                  <span class="history-file-name">{{ fileName(file.path) }}</span>
                  <span v-if="file.orig" class="history-rename">R</span>
                  <span class="history-file-dir">{{ fileDir(file.path) }}</span>
                </span>
                <span v-if="file.binary" class="history-binary">二进制</span>
                <span v-else class="history-file-stats">
                  <span class="history-add">+{{ file.add }}</span><span class="history-del">−{{ file.del }}</span>
                  <span class="history-stat-bar" aria-hidden="true">
                    <i v-for="(kind, i) in statBlocks(file)" :key="i" :class="kind" />
                  </span>
                </span>
              </button>
            </div>
            <p v-else class="history-muted history-no-files">此提交没有文件改动。</p>
          </template>
          <div class="history-detail-actions">
            <n-button size="small" quaternary :disabled="!selectedCommit" title="生成反向提交,原提交仍留在历史中"
              @click="selectedCommit && emit('revert', selectedCommit)">
              <template #icon><n-icon :component="ArrowUndoOutline" /></template>
              回滚此提交
            </n-button>
            <span class="history-muted">不改写提交历史</span>
          </div>
        </template>
      </aside>
    </div>
  </section>
</template>

<style scoped>
.history-panel {
  border: 1px solid var(--lr-border); border-radius: 6px; overflow: hidden;
  background: var(--lr-bg-elevated); color: var(--lr-fg); font-size: 13px;
  --history-height: clamp(340px, calc(100dvh - 230px), 720px);
}
.history-toolbar {
  display: flex; flex-wrap: wrap; align-items: center; gap: 8px; padding: 9px 10px;
  border-bottom: 1px solid var(--lr-border);
}
.history-search { flex: 1 1 190px; min-width: 170px; min-height: 30px; height: 30px; font-size: 12px; }
.history-search :deep(.n-input__input-el) { font-size: 12px; }
.history-filter { display: flex; align-items: center; gap: 5px; min-width: 0; }
.history-branch-filter { flex: 0 1 160px; }
.history-author-filter { flex: 0 1 150px; }
.history-filter > .n-icon, .history-filter-label { flex: none; color: var(--lr-fg-muted); font-size: 12px; }
.history-filter .n-select { flex: 1; min-width: 0; }
.history-filter :deep(.n-base-selection) { --n-height: 30px !important; --n-font-size: 12px !important; --n-border-radius: 4px !important; }
.history-toolbar .n-button { min-height: 30px; height: 30px; font-size: 12px; }
.history-clear { flex: none; color: var(--lr-accent); }
.history-count { flex: none; margin-left: auto; color: var(--lr-fg-muted); font-size: 12px; font-variant-numeric: tabular-nums; }
.history-refresh { flex: none; width: 28px; }
.history-body { display: grid; grid-template-columns: minmax(0, 1fr) minmax(290px, 34%); }
.history-list-pane { min-width: 0; display: flex; flex-direction: column; }
.history-scroll { height: var(--history-height); overflow: auto; overscroll-behavior: contain; }
.history-rows { min-width: max(460px, calc(var(--history-graph-width) + 360px)); padding: 4px 0; }
.history-row {
  display: grid; grid-template-columns: var(--history-graph-width) minmax(0, 1fr) 104px 100px;
  align-items: center; column-gap: 8px; width: 100%; height: 34px; padding: 0 10px 0 0;
  border: 0; background: transparent; color: inherit; text-align: left; font: inherit; cursor: pointer;
}
.history-row:hover { background: rgba(127, 127, 127, .07); }
.history-row.selected { background: rgba(37, 99, 235, .14); }
.history-row:focus-visible { outline: 1px solid var(--lr-accent); outline-offset: -1px; }
.history-graph-cell { position: relative; display: block; align-self: stretch; }
.history-graph { display: block; height: 100%; }
/* 连线随行高伸展,节点独立绘制,避免手机上较高的行把圆点拉成椭圆。 */
.history-node {
  position: absolute; top: 50%; width: 8px; height: 8px; border-radius: 50%;
  border: 1.5px solid var(--graph-color); background: var(--graph-color); transform: translate(-50%, -50%);
}
.history-node.merge { width: 10px; height: 10px; background: var(--lr-bg-elevated); }
.history-summary { min-width: 0; }
.history-subject-line { display: flex; align-items: center; gap: 6px; min-width: 0; }
.history-subject { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.history-refs { flex: 0 1 auto; display: flex; gap: 3px; max-width: 42%; overflow: hidden; }
.history-ref {
  display: inline-block; max-width: 140px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  padding: 0 5px; border-radius: 3px; background: rgba(37, 99, 235, .1); color: var(--lr-accent);
  font-size: 11px; line-height: 19px; font-weight: 500;
}
.history-refs .history-ref { flex: none; }
.history-ref.current { background: var(--lr-accent); color: #fff; }
.history-ref.tag { color: var(--lr-warn); background: rgba(217, 119, 6, .1); }
.history-author { display: flex; align-items: center; gap: 5px; min-width: 0; color: var(--lr-fg-muted); font-size: 12px; }
.history-author > span:last-child { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.history-avatar {
  flex: none; display: inline-flex; align-items: center; justify-content: center; width: 16px; height: 16px;
  border-radius: 50%; color: #fff; font-size: 10px; line-height: 1; font-weight: 600;
}
.history-date { color: var(--lr-fg-muted); font: 11px ui-monospace, SFMono-Regular, Consolas, monospace; white-space: nowrap; text-align: right; }
.history-mobile-meta { display: none; }
.history-empty { display: flex; height: 100%; align-items: center; justify-content: center; flex-direction: column; gap: 12px; }
.history-footer {
  display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 4px 10px;
  min-height: 36px; padding: 4px 10px; margin-top: auto; border-top: 1px solid var(--lr-border); font-size: 11px;
}
.history-footer .n-button { min-height: 26px; height: 26px; font-size: 12px; }
.history-muted { color: var(--lr-fg-muted); }
.history-detail { min-width: 0; height: calc(var(--history-height) + 36px); overflow-y: auto; padding: 16px; border-left: 1px solid var(--lr-border); }
.history-detail-empty { display: flex; height: 100%; align-items: center; justify-content: center; flex-direction: column; gap: 10px; color: var(--lr-fg-muted); }
.history-detail-subject { margin: 0 0 12px; font-size: 14px; line-height: 1.6; font-weight: 500; overflow-wrap: anywhere; }
.history-detail-refs { display: flex; flex-wrap: wrap; gap: 4px; margin-bottom: 16px; }
.history-detail-author { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; font-size: 13px; line-height: 1.6; }
.history-detail-author .history-avatar { width: 19px; height: 19px; font-size: 11px; }
.history-email { overflow-wrap: anywhere; font-size: 12px; }
.history-detail-date { display: block; margin-top: 7px; font-size: 12px; }
.history-detail-meta { display: flex; align-items: center; gap: 7px; margin-top: 6px; font-size: 12px; }
.history-full-hash { flex: 1; min-width: 0; color: var(--lr-accent); font-size: 11px; overflow-wrap: anywhere; user-select: text; }
.history-copy { flex: none; min-height: 24px; height: 24px; width: 24px; }
.history-parents { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; margin-top: 5px; font-size: 12px; }
.history-parent { padding: 2px; border: 0; background: transparent; color: var(--lr-accent); font: 12px ui-monospace, monospace; cursor: pointer; }
.history-parent:hover { text-decoration: underline; }
.history-parent:focus-visible { outline: 1px solid var(--lr-accent); }
.history-commit-body { margin: 14px 0; white-space: pre-wrap; overflow-wrap: anywhere; font-size: 12px; line-height: 1.7; }
.history-files-header { display: flex; flex-wrap: wrap; align-items: baseline; gap: 7px; margin-top: 16px; padding-top: 14px; border-top: 1px solid var(--lr-border); }
.history-files-header strong { font-weight: 500; }
.history-add { color: var(--lr-diff-add); }
.history-del { color: var(--lr-diff-del); }
.history-merge-note { margin: 5px 0; color: var(--lr-fg-muted); font-size: 11px; }
.history-files { margin-top: 9px; }
.history-file {
  display: flex; align-items: center; gap: 7px; width: 100%; min-height: 34px; padding: 6px 0;
  border: 0; border-radius: 3px; background: transparent; color: inherit; font: inherit; text-align: left; cursor: pointer;
}
.history-file:hover { background: rgba(127, 127, 127, .07); }
.history-file:focus-visible { outline: 1px solid var(--lr-accent); }
.history-file-icon { flex: none; display: inline-flex; align-items: center; justify-content: center; width: 16px; color: #bc7b35; font-size: 16px; }
.history-file-path { display: flex; flex-wrap: wrap; align-items: baseline; gap: 0 4px; flex: 1; min-width: 0; overflow-wrap: anywhere; line-height: 1.6; }
.history-file-name { color: var(--lr-accent); font-size: 12px; }
.history-file-dir { color: var(--lr-fg-muted); font-size: 11px; }
.history-rename { color: var(--lr-warn); font-size: 10px; }
.history-file-stats { display: flex; align-items: center; gap: 3px; flex: none; font-size: 11px; font-variant-numeric: tabular-nums; }
.history-stat-bar { display: flex; gap: 2px; width: 28px; justify-content: flex-end; margin-left: 2px; }
.history-stat-bar i { display: block; width: 4px; height: 7px; border-radius: 1px; }
.history-stat-bar .add { background: var(--lr-diff-add-bg); }
.history-stat-bar .del { background: var(--lr-diff-del-bg); }
.history-binary { color: var(--lr-fg-muted); font-size: 11px; }
.history-no-files { margin: 12px 0; font-size: 12px; }
.history-detail-loading { display: flex; align-items: center; justify-content: center; gap: 10px; min-height: 100px; color: var(--lr-fg-muted); font-size: 12px; }
.history-detail-actions { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; margin-top: 16px; padding-top: 10px; border-top: 1px solid var(--lr-border); }
.history-detail-actions .n-button { min-height: 30px; height: 30px; font-size: 12px; }
.history-detail-actions > span { font-size: 11px; }
.history-error { display: flex; align-items: center; justify-content: space-between; gap: 8px; padding: 8px 10px; color: var(--lr-danger); font-size: 12px; overflow-wrap: anywhere; }
.history-error .n-button { flex: none; min-height: 28px; height: 28px; }
@media (max-width: 999px) {
  .history-body { grid-template-columns: minmax(0, 1fr); }
  .history-panel { --history-height: clamp(250px, 42dvh, 440px); }
  .history-detail { height: auto; min-height: 180px; border-left: 0; border-top: 1px solid var(--lr-border); }
}
@media (max-width: 600px) {
  .history-toolbar { gap: 8px; padding: 9px; }
  .history-search { flex-basis: 100%; height: 40px; min-height: 40px; }
  .history-branch-filter, .history-author-filter { flex: 1 1 calc(50% - 4px); }
  .history-filter :deep(.n-base-selection) { --n-height: 38px !important; }
  .history-toolbar .n-button { min-height: 36px; height: 36px; }
  .history-refresh { width: 36px; }
  .history-count { font-size: 11px; }
  .history-rows { min-width: calc(var(--history-graph-width) + 210px); }
  .history-row { grid-template-columns: var(--history-graph-width) minmax(0, 1fr); height: 50px; column-gap: 5px; padding-right: 8px; }
  .history-subject { font-size: 12px; }
  .history-ref { font-size: 10px; line-height: 17px; max-width: 100px; }
  .history-author, .history-date { display: none; }
  .history-mobile-meta { display: flex; align-items: center; gap: 5px; margin-top: 4px; min-width: 0; color: var(--lr-fg-muted); font-size: 10px; }
  .history-mobile-meta .history-avatar { height: 13px; width: 13px; font-size: 9px; }
  .history-mobile-author { flex: 0 1 auto; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; max-width: 30%; }
  .history-mobile-hash { font-family: ui-monospace, monospace; }
  .history-mobile-meta time { margin-left: auto; flex: none; font-family: ui-monospace, monospace; }
  .history-detail { padding: 14px 12px; }
  .history-file { min-height: 44px; }
  .history-parent { min-height: 32px; display: flex; align-items: center; }
  .history-copy { min-height: 32px; height: 32px; width: 28px; }
  .history-detail-actions .n-button { min-height: 40px; height: 40px; }
}
</style>
