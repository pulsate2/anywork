<script setup lang="ts">
// Git 视图:状态/暂存/分文件 diff/提交/分支切换/选远端推送/提交历史翻页。
// 仓库路径 = 当前工作区。
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
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
import { GitAuthClient } from '@/api/gitAuth'
import { useWorkspaceStore } from '@/stores/workspace'
import { parseDiff, type DiffBlock } from '@/utils/diff'
import { useRouter } from 'vue-router'

// 每页提交数;翻页靠 skip 累加已加载条数。
const LOG_PAGE = 30

const message = useMessage()
const dialog = useDialog()
const router = useRouter()
const store = useWorkspaceStore()
const repo = ref<GitRepo | null>(null)
const status = ref<GitStatus | null>(null)
const commits = ref<GitCommit[]>([])
const loading = ref(false)
const repoPath = ref('/')
const logMore = ref(false)
const logLoading = ref(false)

// 一级列表:一次展示某 scope(worktree/staged/commit)的全部文件;点行进二级页。
const showDiff = ref(false)
const diffScope = ref('worktree')
const diffFiles = ref<DiffBlock[]>([])
// 当前在 diff 弹窗里展示的提交(非空 = 在查某个提交的改动,scope 走 commit)。
const diffCommit = ref<GitCommit | null>(null)

// 未跟踪文件没有 diff(git diff 不看它们),点开直接进二级页并默认切到"文件"视图。
function openUntracked(e: GitEntry) {
  goFile('untracked', e.path)
}

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

// git 交互认证:远端要求账号密码时,WS 推 ask 事件 → 打开独立顶层弹窗 →
// 用户填用户名/密码 → POST answer 回填,放行被阻塞的 push/pull。
const askModal = ref(false)
const askToken = ref('')
const askPrompt = ref('')
const askUsername = ref('')
const askPassword = ref('')

const askClient = new GitAuthClient((e) => {
  if (e.type !== 'ask') return
  const ask = e.ask
  askToken.value = ask.token
  askPrompt.value = ask.prompt
  askUsername.value = ''
  askPassword.value = ''
  askModal.value = true
})
async function submitAsk() {
  const token = askToken.value
  askModal.value = false
  try {
    await api.gitAuthAnswer(token, askUsername.value, askPassword.value)
  } catch { /* 服务端幂等,失败也不影响 git 最终报错 */ }
}
async function cancelAsk() {
  askModal.value = false
  try {
    await api.gitAuthAnswer(askToken.value)
  } catch { /* ignore */ }
}
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
  return diffScope.value === 'staged' ? '已暂存改动' : '工作区改动'
})

// 跳二级页看某个文件的差异/全文。scope: worktree|staged|commit|untracked。
function goFile(scope: string, file: string, ref?: string) {
  showDiff.value = false
  router.push({
    path: '/git/file',
    query: {
      path: repoPath.value,
      scope: scope,
      file: file,
      root: repo.value?.root || '',
      ...(ref ? { ref } : {}),
    },
  })
}

// 一级:打开某 scope 的文件列表(仅列表,点某行再进二级页)。
async function viewDiff(scope: string) {
  diffScope.value = scope
  diffCommit.value = null
  try {
    diffFiles.value = parseDiff(await api.gitDiff(repoPath.value, scope))
    showDiff.value = true
  } catch (e: any) {
    message.error(e?.message || '读取 diff 失败')
  }
}

// 查看某个提交做了什么改动:复用同一个列表弹窗,scope 传 commit + 提交哈希。
async function viewCommit(c: GitCommit) {
  diffCommit.value = c
  diffScope.value = 'commit'
  try {
    diffFiles.value = parseDiff(await api.gitDiff(repoPath.value, 'commit', undefined, c.hash))
    showDiff.value = true
  } catch (e: any) {
    diffCommit.value = null
    message.error(e?.message || '读取提交改动失败')
  }
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
  // git 认证长连接:远端要求账号密码时在此收到 ask 事件并弹窗。
  askClient.connect()
})
onUnmounted(() => askClient.close())
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
                  <span class="git-path" :title="e.path" @click="goFile('staged', e.path)">{{ e.path }}</span>
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
                  <span class="git-path" :title="e.path" @click="goFile('worktree', e.path)">{{ e.path }}</span>
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
                  <span class="git-path" :title="e.path" @click="openUntracked(e)">{{ e.path }}</span>
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

    <!-- 一级:文件列表,点某行进二级页看差异/全文 -->
    <n-modal v-model:show="showDiff" preset="card" :title="diffTitle" style="width: 92%; max-width: 900px">
      <n-empty v-if="!diffFiles.length" description="没有差异" style="padding: 24px" />
      <div v-else class="diff-list">
        <div v-for="f in diffFiles" :key="f.path" class="diff-file list-row" role="button" tabindex="0"
          @click="goFile(diffScope, f.path, diffCommit?.hash)" @keydown.enter="goFile(diffScope, f.path, diffCommit?.hash)">
          <span class="diff-path" :title="f.path">{{ f.path }}</span>
          <span class="diff-add">+{{ f.adds }}</span>
          <span class="diff-del">-{{ f.dels }}</span>
          <n-icon class="diff-go" :component="ChevronForwardOutline" />
        </div>
      </div>
      <template #footer>
        <div class="modal-footer">
          <n-button @click="showDiff = false">关闭</n-button>
        </div>
      </template>
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
    <!-- 交互式认证:远端要求账号密码(推送/拉取被 git 阻塞时弹出)。独立顶层弹窗。 -->
    <n-modal v-model:show="askModal" preset="card" title="需要账号密码" :mask-closable="false"
      :closable="false" style="width: 92%; max-width: 460px">
      <div class="git-hint">{{ askPrompt }}请输入远端仓库的用户名和密码。</div>
      <n-form label-placement="top" :show-feedback="false">
        <n-form-item label="用户名">
          <n-input v-model:value="askUsername" placeholder="用户名" autocomplete="username"
            @keydown.enter="submitAsk" />
        </n-form-item>
        <n-form-item label="密码">
          <n-input v-model:value="askPassword" type="password" show-password-on="click"
            placeholder="密码" autocomplete="current-password" @keydown.enter="submitAsk" />
        </n-form-item>
      </n-form>
      <div class="git-hint">凭据仅用于本次操作,不会保存。</div>
      <template #footer>
        <div class="modal-footer">
          <n-button @click="cancelAsk">取消</n-button>
          <n-button type="primary" @click="submitAsk">确认</n-button>
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
/* flex 列容器里每个文件块必须保持自然高度,不能 flex-shrink:
   不然展开的文件一多,各块会被容器(70vh)按比例压缩挤扁,内容被 overflow:hidden
   裁成一条细线——这就是"文件一多只剩线条"的来源。flex: none 让每块按自身内容
   高排布,超出部分交给 .diff-list 自己滚动。 */
.diff-file { flex: none; border: 1px solid rgba(127, 127, 127, 0.2); border-radius: var(--lr-radius); overflow: hidden; }
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
/* 一级文件列表行:点整行进二级页。 */
.diff-file.list-row {
  border: 1px solid rgba(127, 127, 127, 0.2);
  border-radius: var(--lr-radius);
  cursor: pointer;
  display: flex; align-items: center; gap: 8px;
  padding: 9px 10px;
}
.diff-file.list-row:hover, .diff-file.list-row:focus-visible {
  background: rgba(127, 127, 127, 0.08);
  outline: none;
}
.diff-go { flex: none; color: var(--lr-fg-muted); font-size: 14px; }
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
