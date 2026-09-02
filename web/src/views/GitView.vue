<script setup lang="ts">
// Git 视图:状态/暂存/分文件 diff/提交/分支切换/选远端推送/提交历史翻页。
// 仓库路径 = 当前工作区。
import { ref, computed, onMounted, watch } from 'vue'
import {
  NButton, NIcon, NInput, NList, NListItem, NEmpty, NSpin, NModal,
  NSelect, NCheckbox, NForm, NFormItem, NTag, NTabs, NTabPane,
  useMessage, useDialog,
} from 'naive-ui'
import {
  GitCompareOutline, GitCommitOutline, GitBranchOutline, CloudUploadOutline, CloudDownloadOutline,
  TrashOutline, CheckmarkOutline, ChevronDownOutline, ChevronForwardOutline,
} from '@vicons/ionicons5'
import {
  api, type GitStatus, type GitEntry, type GitCommit, type GitRepo,
  type GitBranch, type GitBranchList, type GitRemote,
} from '@/api/client'
import { useWorkspaceStore } from '@/stores/workspace'

// 每页提交数;翻页靠 skip 累加已加载条数。
const LOG_PAGE = 30

const message = useMessage()
const dialog = useDialog()
const store = useWorkspaceStore()
const repo = ref<GitRepo | null>(null)
const status = ref<GitStatus | null>(null)
const commits = ref<GitCommit[]>([])
const loading = ref(false)
const repoPath = ref('/')
const logMore = ref(false)
const logLoading = ref(false)

// diff 按文件切块后渲染,每行标出类型用于着色。
interface DiffLine { text: string; kind: 'add' | 'del' | 'hunk' | 'meta' | 'ctx' }
interface DiffBlock { path: string; adds: number; dels: number; lines: DiffLine[]; open: boolean }
const showDiff = ref(false)
const diffScope = ref('worktree')
const diffFile = ref('')
const diffFiles = ref<DiffBlock[]>([])
// 当前在 diff 弹窗里展示的提交(非空 = 在查某个提交的改动,scope 走 commit)。
const diffCommit = ref<GitCommit | null>(null)

const commitMsg = ref('')
const commitModal = ref(false)
// 提交时是否先 git add -A 暂存全部改动(含未跟踪)。默认开,省去先到改动页
// "全部暂存"再回来提交的一步;勾选与否直接决定提交用 addAll。
const commitStageAll = ref(true)

const showBranch = ref(false)
const branches = ref<GitBranchList | null>(null)
const branchName = ref('')
const branchStart = ref('')

const showPush = ref(false)
const remotes = ref<GitRemote[]>([])
const pushRemote = ref<string | null>(null)
const pushBranch = ref('')
const pushUpstream = ref(false)
const pushing = ref(false)
const pulling = ref(false)
async function load() {
  loading.value = true
  try {
    const [r, st, log] = await Promise.all([
      api.gitRepo(repoPath.value),
      api.gitStatus(repoPath.value),
      api.gitLog(repoPath.value, LOG_PAGE),
    ])
    repo.value = r
    status.value = st
    commits.value = log
    logMore.value = log.length >= LOG_PAGE
  } catch (e: any) {
    repo.value = null
    message.error(e?.message || 'Git 加载失败')
  } finally {
    loading.value = false
  }
}

// 刷新状态与第一页提交(仓库信息不会变,不重取)。
async function reload() {
  status.value = await api.gitStatus(repoPath.value)
  const log = await api.gitLog(repoPath.value, LOG_PAGE)
  commits.value = log
  logMore.value = log.length >= LOG_PAGE
}

async function loadMoreLog() {
  logLoading.value = true
  try {
    const more = await api.gitLog(repoPath.value, LOG_PAGE, commits.value.length)
    commits.value = commits.value.concat(more)
    logMore.value = more.length >= LOG_PAGE
  } catch (e: any) {
    message.error(e?.message || '加载失败')
  } finally {
    logLoading.value = false
  }
}

async function stageFiles(entries: GitEntry[], reset = false) {
  try {
    await api.gitStage(repoPath.value, entries.map(e => e.path), reset)
    await reload()
  } catch (e: any) {
    message.error(e?.message || '操作失败')
  }
}
const diffTitle = computed(() => {
  if (diffCommit.value) return `${diffCommit.value.short} · ${diffCommit.value.subject}`
  const scope = diffScope.value === 'staged' ? '已暂存' : '工作区'
  return diffFile.value ? `${scope} · ${diffFile.value}` : `${scope}改动`
})

async function viewDiff(scope: string, file?: string) {
  diffScope.value = scope
  diffFile.value = file || ''
  diffCommit.value = null
  try {
    diffFiles.value = parseDiff(await api.gitDiff(repoPath.value, scope, file))
    showDiff.value = true
  } catch (e: any) {
    message.error(e?.message || '读取 diff 失败')
  }
}

// 查看某个提交做了什么改动:复用同一个 diff 弹窗,scope 传 commit + 提交哈希。
async function viewCommit(c: GitCommit) {
  diffCommit.value = c
  diffScope.value = 'commit'
  diffFile.value = ''
  try {
    diffFiles.value = parseDiff(await api.gitDiff(repoPath.value, 'commit', undefined, c.hash))
    showDiff.value = true
  } catch (e: any) {
    diffCommit.value = null
    message.error(e?.message || '读取提交改动失败')
  }
}

// git 的文件头行。只在 hunk 之前匹配,所以不会误伤以 --- / +++ 开头的内容行。
const diffHeaderRe = /^(index |--- |\+\+\+ |old mode |new mode |new file mode |deleted file mode |similarity index |dissimilarity index |rename |copy )/

// 把 unified diff 拆成每文件一块,每行标出类型用于着色。
function parseDiff(text: string): DiffBlock[] {
  const files: DiffBlock[] = []
  let cur: DiffBlock | null = null
  let inHunk = false
  for (const raw of text.split('\n')) {
    const line = raw.replace(/\r$/, '')
    if (line.startsWith('diff --git ')) {
      cur = { path: headerPath(line), adds: 0, dels: 0, lines: [], open: true }
      inHunk = false
      files.push(cur)
      continue
    }
    if (!cur) continue
    if (line.startsWith('@@')) {
      inHunk = true
      cur.lines.push({ text: line, kind: 'hunk' })
    } else if (!inHunk) {
      // 文件头丢掉:路径和增删数已经在块标题里了。但不能把 hunk 之前的行一律丢掉——
      // 二进制文件("Binary files ... differ")和子模块("Subproject commit ...")
      // 整块只有这类说明行,丢了就成空 diff。
      if (!diffHeaderRe.test(line) && line !== '') cur.lines.push({ text: line, kind: 'meta' })
    } else if (line.startsWith('+')) {
      cur.adds++
      cur.lines.push({ text: line, kind: 'add' })
    } else if (line.startsWith('-')) {
      cur.dels++
      cur.lines.push({ text: line, kind: 'del' })
    } else {
      cur.lines.push({ text: line, kind: 'ctx' })
    }
  }
  // 文件多时默认折叠,免得一次渲染上万行卡住手机。
  if (files.length > 3) files.forEach((f) => (f.open = false))
  return files
}

// `diff --git a/x b/x` → 取 b/ 之后的新路径(重命名时正是要看的那个)。
function headerPath(l: string): string {
  const s = l.slice('diff --git '.length)
  const i = s.indexOf(' b/')
  return (i >= 0 ? s.slice(i + 3) : s).replace(/^"(.*)"$/, '$1')
}
async function doCommit() {
  if (!commitMsg.value.trim()) return
  try {
    await api.gitCommit(repoPath.value, commitMsg.value, commitStageAll.value)
    commitMsg.value = ''
    commitModal.value = false
    message.success('已提交')
    await reload()
  } catch (e: any) {
    message.error(e?.message || '提交失败')
  }
}

async function openPush() {
  showPush.value = true
  pushBranch.value = status.value?.branch || repo.value?.branch || ''
  // 没有上游时默认带 -u,否则首次推送会被 git 拒绝。
  pushUpstream.value = !status.value?.upstream
  try {
    remotes.value = await api.gitRemotes(repoPath.value)
    if (!pushRemote.value) {
      pushRemote.value = remotes.value.find((r) => r.name === 'origin')?.name
        ?? remotes.value[0]?.name ?? null
    }
  } catch (e: any) {
    message.error(e?.message || '读取远端失败')
  }
}

async function doPush() {
  pushing.value = true
  try {
    const res = await api.gitPush(repoPath.value, {
      remote: pushRemote.value || undefined,
      branch: pushBranch.value.trim() || undefined,
      setUpstream: pushUpstream.value,
    })
    showPush.value = false
    message.success('已推送')
    // git push 的进度走 stderr,out 常常是空的。
    if (res.out.trim()) dialog.info({ title: '推送结果', content: res.out })
    await reload()
  } catch (e: any) {
    message.error(e?.message || '推送失败')
  } finally {
    pushing.value = false
  }
}
async function doPull() {
  pulling.value = true
  try {
    const res = await api.gitPull(repoPath.value)
    message.success('已拉取')
    if (res.out.trim()) dialog.info({ title: '拉取结果', content: res.out })
    await load()
  } catch (e: any) {
    message.error(e?.message || '拉取失败')
  } finally {
    pulling.value = false
  }
}
async function openBranch() {
  showBranch.value = true
  try {
    branches.value = await api.gitBranches(repoPath.value)
  } catch (e: any) {
    message.error(e?.message || '读取分支失败')
  }
}

async function switchBranch(b: GitBranch) {
  if (b.current) return
  let op = 'switch'
  let target = b.name
  if (b.remote) {
    const local = b.name.slice(b.name.indexOf('/') + 1)
    if (branches.value?.local.some((l) => l.name === local)) {
      target = local
    } else {
      // 本地还没有对应分支:--track 建一个同名分支跟踪它。
      op = 'track'
    }
  }
  try {
    await api.gitBranch(repoPath.value, op, target)
    showBranch.value = false
    message.success('已切换分支')
    await load()
  } catch (e: any) {
    message.error(e?.message || '切换失败')
  }
}

async function createBranch() {
  const name = branchName.value.trim()
  if (!name) return
  try {
    await api.gitBranch(repoPath.value, 'create', name, branchStart.value.trim() || undefined)
    branchName.value = ''
    branchStart.value = ''
    message.success('已创建分支')
    branches.value = await api.gitBranches(repoPath.value)
  } catch (e: any) {
    message.error(e?.message || '创建失败')
  }
}

function deleteBranch(b: GitBranch) {
  dialog.warning({
    title: '删除分支',
    content: `确定删除本地分支 ${b.name}?未合并的提交会丢失。`,
    positiveText: '删除',
    negativeText: '取消',
    onPositiveClick: async () => {
      try {
        await api.gitBranch(repoPath.value, 'delete', b.name)
        message.success('已删除')
        branches.value = await api.gitBranches(repoPath.value)
        await reload()
      } catch (e: any) {
        message.error(e?.message || '删除失败')
      }
    },
  })
}

onMounted(async () => {
  await store.ensure()
  repoPath.value = store.currentPath
  load()
})
// 跟随工作区切换。
watch(() => store.currentPath, (p) => {
  repoPath.value = p
  load()
})
</script>

<template>
  <div class="page-content">
    <div class="git-header">
      <div class="git-title">
        <h2>Git</h2>
        <div class="git-ws">{{ store.current?.name || '根目录' }}</div>
      </div>
      <div class="git-toolbar">
        <n-button quaternary size="small" :disabled="!repo?.repo" title="查看改动" aria-label="查看改动"
          @click="viewDiff('worktree')">
          <template #icon><n-icon :component="GitCompareOutline" /></template>
        </n-button>
        <n-button quaternary size="small" :disabled="!repo?.repo" title="提交" aria-label="提交"
          @click="commitModal = true">
          <template #icon><n-icon :component="GitCommitOutline" /></template>
        </n-button>
        <n-button quaternary size="small" :disabled="!repo?.repo" title="推送" aria-label="推送" @click="openPush">
          <template #icon><n-icon :component="CloudUploadOutline" /></template>
        </n-button>
        <n-button quaternary size="small" :disabled="!repo?.repo || pulling" :loading="pulling" title="拉取"
          aria-label="拉取" @click="doPull">
          <template #icon><n-icon :component="CloudDownloadOutline" /></template>
        </n-button>
        <n-button quaternary size="small" :disabled="!repo?.repo" title="分支" aria-label="分支" @click="openBranch">
          <template #icon><n-icon :component="GitBranchOutline" /></template>
        </n-button>
      </div>
    </div>

    <n-spin :show="loading">
      <n-empty v-if="!repo?.repo" description="当前工作区不是 Git 仓库" style="padding: 40px" />
      <template v-else>
        <div class="git-meta">
          <n-tag size="small" :bordered="false" :type="status?.detached ? 'warning' : 'info'">
            {{ status?.branch || repo?.branch }}
          </n-tag>
          <span class="git-sub">
            {{ status?.upstream ? `↑${status.ahead} ↓${status.behind} · ${status.upstream}` : '无上游' }}
          </span>
          <div class="spacer"></div>
          <span v-if="status?.clean" class="git-sub">干净</span>
        </div>
        <n-tabs type="line" size="small" animated>
          <n-tab-pane name="changes" tab="改动">
            <n-empty v-if="status?.clean" description="没有改动" style="padding: 24px" />
            <template v-else>
              <div v-if="status && status.conflicted.length" class="git-group">
                <div class="git-group-head"><span>冲突 ({{ status.conflicted.length }})</span></div>
                <div v-for="e in status.conflicted" :key="e.path" class="git-file">
                  <span class="git-xy danger">{{ e.x }}{{ e.y }}</span>
                  <span class="git-path" :title="e.path">{{ e.path }}</span>
                </div>
              </div>
              <div v-if="status && status.staged.length" class="git-group">
                <div class="git-group-head">
                  <span>已暂存 ({{ status.staged.length }})</span>
                  <div class="spacer"></div>
                  <n-button size="tiny" quaternary @click="viewDiff('staged')">查看</n-button>
                  <n-button size="tiny" quaternary @click="stageFiles(status.staged, true)">全部取消</n-button>
                </div>
                <div v-for="e in status.staged" :key="e.path" class="git-file">
                  <span class="git-xy add">{{ e.x }}{{ e.y }}</span>
                  <span class="git-path" :title="e.path" @click="viewDiff('staged', e.path)">{{ e.path }}</span>
                  <n-button class="git-btn" size="tiny" quaternary title="取消暂存" aria-label="取消暂存"
                    @click="stageFiles([e], true)">
                    <n-icon :component="TrashOutline" />
                  </n-button>
                </div>
              </div>
              <div v-if="status && status.unstaged.length" class="git-group">
                <div class="git-group-head">
                  <span>未暂存 ({{ status.unstaged.length }})</span>
                  <div class="spacer"></div>
                  <n-button size="tiny" quaternary @click="viewDiff('worktree')">查看</n-button>
                  <n-button size="tiny" quaternary @click="stageFiles(status.unstaged)">全部暂存</n-button>
                </div>
                <div v-for="e in status.unstaged" :key="e.path" class="git-file">
                  <span class="git-xy">{{ e.x }}{{ e.y }}</span>
                  <span class="git-path" :title="e.path" @click="viewDiff('worktree', e.path)">{{ e.path }}</span>
                  <n-button class="git-btn" size="tiny" quaternary title="暂存" aria-label="暂存"
                    @click="stageFiles([e])">
                    <n-icon :component="CheckmarkOutline" />
                  </n-button>
                </div>
              </div>
              <div v-if="status && status.untracked.length" class="git-group">
                <div class="git-group-head">
                  <span>未跟踪 ({{ status.untracked.length }})</span>
                  <div class="spacer"></div>
                  <n-button size="tiny" quaternary @click="stageFiles(status.untracked)">全部暂存</n-button>
                </div>
                <div v-for="e in status.untracked" :key="e.path" class="git-file">
                  <span class="git-xy">??</span>
                  <span class="git-path" :title="e.path">{{ e.path }}</span>
                  <n-button class="git-btn" size="tiny" quaternary title="暂存" aria-label="暂存"
                    @click="stageFiles([e])">
                    <n-icon :component="CheckmarkOutline" />
                  </n-button>
                </div>
              </div>

            </template>
          </n-tab-pane>
          <n-tab-pane name="log" tab="提交历史">
            <n-list v-if="commits.length" hoverable>
              <n-list-item v-for="c in commits" :key="c.hash">
                <div class="log-row" role="button" tabindex="0" @click="viewCommit(c)"
                @keydown.enter="viewCommit(c)" title="查看该提交的改动">
              <span class="log-hash">{{ c.short }}</span>
              <span class="log-subject" :title="c.subject">{{ c.subject }}</span>
            </div>
                <div class="log-meta">
                  <span>{{ c.date }}</span>
                  <n-tag v-for="rf in c.refs" :key="rf" size="tiny" :bordered="false">{{ rf }}</n-tag>
                </div>
              </n-list-item>
            </n-list>
            <n-empty v-else description="没有提交" style="padding: 24px" />
            <div class="log-more">
              <n-button v-if="logMore" size="small" secondary block :loading="logLoading" @click="loadMoreLog">
                加载更多
              </n-button>
              <span v-else-if="commits.length" class="log-end">没有更多了({{ commits.length }} 条)</span>
            </div>
          </n-tab-pane>
        </n-tabs>

      </template>
    </n-spin>

    <!-- 分文件 diff -->
    <n-modal v-model:show="showDiff" preset="card" :title="diffTitle" style="width: 92%; max-width: 900px">
      <n-empty v-if="!diffFiles.length" description="没有差异" style="padding: 24px" />
      <div v-else class="diff-list">
        <div v-for="f in diffFiles" :key="f.path" class="diff-file">
          <div class="diff-head" role="button" tabindex="0" @click="f.open = !f.open"
            @keydown.enter="f.open = !f.open">
            <n-icon :component="f.open ? ChevronDownOutline : ChevronForwardOutline" />
            <span class="diff-path" :title="f.path">{{ f.path }}</span>
            <span class="diff-add">+{{ f.adds }}</span>
            <span class="diff-del">-{{ f.dels }}</span>
          </div>
          <div v-if="f.open" class="diff-body">
            <div class="diff-inner">
              <div v-for="(l, i) in f.lines" :key="i" class="dl" :class="l.kind">{{ l.text || ' ' }}</div>
            </div>
          </div>
        </div>
      </div>
    </n-modal>

    <!-- 提交 -->
    <n-modal v-model:show="commitModal" preset="card" title="提交" style="width: 92%; max-width: 560px">
      <n-input v-model:value="commitMsg" type="textarea" placeholder="提交信息"
        :autosize="{ minRows: 3, maxRows: 8 }" />
      <div class="commit-stage">
        <n-checkbox v-model:checked="commitStageAll">提交前暂存所有修改(含未跟踪文件)</n-checkbox>
      </div>
      <div class="git-hint">
        当前共有 {{ status ? status.unstaged.length + status.untracked.length : 0 }} 个未暂存/未跟踪文件
        会一并提交{{ commitStageAll ? '。取消勾选则只提交已暂存的改动。' : '。' }}
      </div>
      <template #footer>
        <div class="modal-footer">
          <n-button @click="commitModal = false">取消</n-button>
          <n-button type="primary" :disabled="!commitMsg.trim()" @click="doCommit">提交</n-button>
        </div>
      </template>
    </n-modal>
    <!-- 推送:可选远端,而不是固定 origin -->
    <n-modal v-model:show="showPush" preset="card" title="推送" style="width: 92%; max-width: 480px">
      <n-form label-placement="top" :show-feedback="false">
        <n-form-item label="远端">
          <n-select v-model:value="pushRemote" clearable placeholder="默认(当前分支上游)"
            :options="remotes.map((r) => ({ label: `${r.name} — ${r.url}`, value: r.name }))" />
        </n-form-item>
        <n-form-item label="分支">
          <n-input v-model:value="pushBranch" :disabled="!pushRemote" placeholder="当前分支" />
        </n-form-item>
        <n-form-item v-if="pushRemote" :show-label="false">
          <n-checkbox v-model:checked="pushUpstream">设为上游(-u)</n-checkbox>
        </n-form-item>
      </n-form>
      <div class="git-hint">{{ status?.upstream ? `当前上游:${status.upstream}` : '当前分支还没有上游' }}</div>
      <template #footer>
        <div class="modal-footer">
          <n-button @click="showPush = false">取消</n-button>
          <n-button type="primary" :loading="pushing" @click="doPush">推送</n-button>
        </div>
      </template>
    </n-modal>
    <!-- 分支:新建/切换/删除 -->
    <n-modal v-model:show="showBranch" preset="card" title="分支" style="width: 92%; max-width: 560px">
      <div class="branch-new">
        <n-input v-model:value="branchName" size="small" placeholder="新分支名" />
        <n-input v-model:value="branchStart" size="small" placeholder="起点(可选)" />
        <n-button size="small" type="primary" :disabled="!branchName.trim()" @click="createBranch">新建</n-button>
      </div>
      <div v-if="branches" class="branch-sect">
        <div class="branch-title">本地</div>
        <div v-for="b in branches.local" :key="b.name" class="branch-item">
          <div class="branch-main" role="button" tabindex="0" @click="switchBranch(b)"
            @keydown.enter="switchBranch(b)">
            <n-icon v-if="b.current" class="branch-cur" :component="CheckmarkOutline" />
            <span class="branch-name">{{ b.name }}</span>
            <span v-if="b.upstream" class="branch-up">→ {{ b.upstream }}</span>
          </div>
          <n-button class="git-btn" size="tiny" quaternary type="error" :disabled="b.current" title="删除分支"
            aria-label="删除分支" @click="deleteBranch(b)">
            <n-icon :component="TrashOutline" />
          </n-button>
          <div class="branch-sub">{{ b.date }} · {{ b.subject }}</div>
        </div>
        <div class="branch-title">远端</div>
        <n-empty v-if="!branches.remote.length" description="没有远端分支" size="small" style="padding: 8px 0" />
        <div v-for="b in branches.remote" :key="b.name" class="branch-item">
          <div class="branch-main" role="button" tabindex="0" @click="switchBranch(b)"
            @keydown.enter="switchBranch(b)">
            <span class="branch-name">{{ b.name }}</span>
          </div>
          <div class="branch-sub">{{ b.date }} · {{ b.subject }}</div>
        </div>
      </div>
      <div v-else class="branch-loading">加载中…</div>
    </n-modal>


  </div>
</template>

<style scoped>
.git-header { display: flex; align-items: flex-start; justify-content: space-between; margin-bottom: 8px; }
.git-header h2 { margin: 0; font-size: 20px; }
.git-ws { color: var(--lr-fg-muted); font-size: 12px; margin-top: 2px; }
.git-toolbar { display: flex; gap: 2px; }
.git-meta { display: flex; align-items: center; gap: 8px; margin: 4px 0 8px; }
.git-sub {
  color: var(--lr-fg-muted); font-size: 12px;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.spacer { flex: 1; }
.git-group { margin-bottom: 12px; }
.git-group-head { display: flex; align-items: center; gap: 4px; font-size: 13px; font-weight: 600; }
.git-file {
  display: flex; align-items: center; gap: 8px;
  padding: 5px 0; border-bottom: 1px solid rgba(127, 127, 127, 0.14);
}
.git-xy {
  flex: none; width: 20px; white-space: pre;
  font-family: ui-monospace, monospace; font-size: 11px; color: var(--lr-fg-muted);
}
.git-xy.add { color: #16a34a; }
.git-xy.danger { color: var(--lr-danger); }
/* rtl 让长路径优先露出尾部(文件名),溢出省略号落在开头 */
.git-path {
  flex: 1; min-width: 0; cursor: pointer;
  font-family: ui-monospace, monospace; font-size: 12px;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  direction: rtl; text-align: left;
}
/* 覆盖全局 .n-button 的 44px 触控下限,否则行会被撑高 */
.git-btn { min-height: 28px; height: 28px; width: 28px; }
/* 提交弹窗的"暂存所有"开关行:给 checkbox 一点呼吸,不与提示文案挤在一起 */
.commit-stage { margin-top: 10px; }
.git-hint { color: var(--lr-fg-muted); font-size: 12px; margin-top: 8px; }
.log-row { display: flex; align-items: baseline; gap: 8px; }
.log-hash { flex: none; font-family: ui-monospace, monospace; font-size: 12px; color: var(--lr-accent); }
.log-subject { flex: 1; min-width: 0; font-size: 13px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.log-meta {
  display: flex; align-items: center; flex-wrap: wrap; gap: 6px;
  margin-top: 2px; color: var(--lr-fg-muted); font-size: 11px;
}
.log-more { padding: 12px 0; text-align: center; }
.log-end { color: var(--lr-fg-muted); font-size: 12px; }
.diff-list { display: flex; flex-direction: column; gap: 8px; max-height: 70vh; overflow-y: auto; }
.diff-file { border: 1px solid rgba(127, 127, 127, 0.2); border-radius: var(--lr-radius); overflow: hidden; }
.diff-head {
  display: flex; align-items: center; gap: 6px; cursor: pointer;
  padding: 8px 10px; background: rgba(127, 127, 127, 0.08);
}
.diff-path {
  flex: 1; min-width: 0;
  font-family: ui-monospace, monospace; font-size: 12px;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  direction: rtl; text-align: left;
}
.diff-add { flex: none; color: #16a34a; font-size: 12px; }
.diff-del { flex: none; color: var(--lr-danger); font-size: 12px; }
.diff-body {
  /* 每个文件块独立上下 + 水平滚动:滚动容器宽度 = 弹窗给的整行宽
     (固定,不随内容变),这样才有横向滚动的余地,才不会出现"没法右滚"。 */
  overflow: auto;
  max-height: 60vh;
  font-family: ui-monospace, monospace; font-size: 12px; line-height: 1.5;
}
/* 内容宽度层:min-width 100% 兜底不小于容器,width max-content 让可滚动宽度
   由最长一行撑开 → 它成了"整行"的基准。 */
.diff-inner {
  width: max-content;
  min-width: 100%;
}
/* 不用 <pre>:Vue 模板编译器会保留 <pre> 里的模板缩进。
   块级 div 默认 width: 100% 铺满 .diff-inner → 所有行(无论长短)背景都铺满
   同一个整行宽度,连续对齐,右滑时整行背景跟随。 */
.dl {
  white-space: pre; padding: 0 10px;
}
.dl.add { background: rgba(22, 163, 74, 0.16); }
.dl.del { background: rgba(220, 38, 38, 0.16); }
.dl.hunk { color: var(--lr-accent); background: rgba(127, 127, 127, 0.1); }
.dl.meta { color: var(--lr-fg-muted); }
.branch-new { display: flex; flex-wrap: wrap; gap: 6px; }
.branch-new .n-input { flex: 1 1 130px; }
.branch-sect { max-height: 60vh; overflow-y: auto; }
.branch-title { margin: 12px 0 2px; font-size: 12px; font-weight: 600; color: var(--lr-fg-muted); }
.branch-item {
  display: grid; grid-template-columns: 1fr auto; align-items: center; gap: 2px 8px;
  padding: 6px 0; border-bottom: 1px solid rgba(127, 127, 127, 0.14);
}
.branch-main { display: flex; align-items: center; gap: 6px; min-width: 0; cursor: pointer; }
.branch-cur { flex: none; color: #16a34a; }
.branch-name {
  font-family: ui-monospace, monospace; font-size: 13px;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.branch-up { flex: none; color: var(--lr-fg-muted); font-size: 11px; }
.branch-sub {
  grid-column: 1 / -1; color: var(--lr-fg-muted); font-size: 11px;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.branch-loading { padding: 16px 0; text-align: center; color: var(--lr-fg-muted); font-size: 12px; }
.modal-footer { display: flex; justify-content: flex-end; gap: 8px; }

</style>
