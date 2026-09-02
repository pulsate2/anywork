<script setup lang="ts">
// 二级页面:查看单个文件的 差异 / 全文 视图。
// 一级(GitView 文件列表)点某文件 → 路由到本页,查询参数带 path/scope/file/ref/root。
// 差异 = git diff 该文件的有色行;文件 = fsRead 全文。未跟踪文件无 diff,默认文件视图。
import { ref, computed, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  NButton, NIcon, NSpin, NEmpty, NAlert, NTag,
} from 'naive-ui'
import { ChevronBackOutline, GitCompareOutline, DocumentTextOutline } from '@vicons/ionicons5'
import { api } from '@/api/client'
import { parseDiff, type DiffBlock } from '@/utils/diff'
import { highlightCode } from '@/utils/highlight'

const route = useRoute()
const router = useRouter()

const scope = (route.query.scope as string) || 'worktree'
const file = (route.query.file as string) || ''
const commitHash = (route.query.ref as string) || ''
const root = (route.query.root as string) || ''
const repoPath = (route.query.path as string) || '/'

const title = computed(() => file)

// 视图切换:差异 / 文件。untracked 无 diff,只能看文件。
// 看提交时"文件"取的是该提交里的版本(gitShow),不是工作区版本 —— 后者可能早被改过。
type ViewMode = 'diff' | 'file'
const mode = ref<ViewMode>(scope === 'untracked' ? 'file' : 'diff')

const diffBlock = ref<DiffBlock | null>(null)
const diffLoading = ref(false)
const diffError = ref('')
const fileContent = ref('')
const fileLoading = ref(false)
const fileError = ref('')

async function loadDiff() {
  diffLoading.value = true
  diffError.value = ''
  try {
    const blocks = parseDiff(await api.gitDiff(repoPath, scope, file, commitHash || undefined))
    // 筛出目标文件的那一块;多文件 diff 中若对不上就兜底空块。
    diffBlock.value = blocks.find((b) => b && b.path === file) ?? {
      path: file, adds: 0, dels: 0, lines: [], open: true,
    }
  } catch (e: any) {
    diffError.value = e?.message || '读取差异失败'
    diffBlock.value = null
  } finally {
    diffLoading.value = false
  }
}

async function loadFile() {
  fileLoading.value = true
  fileError.value = ''
  try {
    if (scope === 'commit') {
      fileContent.value = await api.gitShow(repoPath, commitHash, file)
    } else {
      const abs = root ? `${root.replace(/\/$/, '')}/${file}` : `${repoPath}/${file}`
      fileContent.value = await api.fsRead(abs)
    }
  } catch (e: any) {
    fileError.value = `无法读取文件:${e?.message || e || '未知错误'}`
    fileContent.value = ''
  } finally {
    fileLoading.value = false
  }
}

// 语法高亮后的代码 HTML(v-html 渲染)。识别不了的语言降级为纯文本。
const highlighted = computed(() =>
  fileContent.value === '' ? '' : highlightCode(fileContent.value, file))

function switchMode(next: ViewMode) {
  if (mode.value === next) return
  mode.value = next
  if (next === 'diff') loadDiff()
  else loadFile()
}

onMounted(async () => {
  if (scope === 'untracked') {
    loadFile()
  } else {
    loadDiff()
  }
})

const scopeTag = computed(() => {
  switch (scope) {
    case 'staged': return '已暂存'
    case 'commit': return commitHash ? `提交 ${commitHash.slice(0, 7)}` : '提交'
    case 'untracked': return '未跟踪'
    default: return '工作区'
  }
})

function goBack() {
  router.back()
}
</script>

<template>
  <div class="file-view page-content">
    <div class="fv-head">
      <n-button quaternary size="small" title="返回列表" aria-label="返回列表" @click="goBack">
        <template #icon><n-icon :component="ChevronBackOutline" /></template>
      </n-button>
      <div class="fv-title">
        <div class="fv-path" :title="title">{{ title }}</div>
        <n-tag size="tiny" :bordered="false" type="info">{{ scopeTag }}</n-tag>
      </div>
      <div class="fv-tabs">
        <n-button size="small" :type="mode === 'diff' ? 'primary' : 'default'" :disabled="scope === 'untracked'"
          title="差异" aria-label="差异" @click="switchMode('diff')">
          <template #icon><n-icon :component="GitCompareOutline" /></template>
          差异
        </n-button>
        <n-button size="small" :type="mode === 'file' ? 'primary' : 'default'"
          title="文件" aria-label="文件" @click="switchMode('file')">
          <template #icon><n-icon :component="DocumentTextOutline" /></template>
          文件
        </n-button>
      </div>
    </div>

    <!-- 差异视图 -->
    <div v-if="mode === 'diff'" class="fv-diff">
      <n-spin :show="diffLoading">
        <n-alert v-if="diffError" type="error" :bordered="false" :title="diffError" style="margin: 8px 0" />
        <n-empty v-else-if="!diffBlock || !diffBlock.lines.length" description="该文件没有内容差异" style="padding: 24px" />
        <div v-else class="diff-scroll">
          <div class="diff-inner">
            <div v-for="(l, i) in diffBlock.lines" :key="i" class="dl" :class="l.kind">{{ l.text || ' ' }}</div>
          </div>
        </div>
      </n-spin>
    </div>

    <!-- 文件视图 -->
    <div v-else class="fv-file">
      <n-spin :show="fileLoading">
        <n-alert v-if="fileError" type="error" :bordered="false" :title="fileError" style="margin: 8px 0" />
        <div v-else-if="fileContent !== ''" class="file-body">
          <!-- 代码高亮:v-html 渲染 hljs 产出的 HTML。着色令牌色由下方 .hljs-* 规则定义。 -->
          <code v-html="highlighted"></code>
        </div>
      </n-spin>
    </div>
  </div>
</template>

<style scoped>
.file-view { display: flex; flex-direction: column; height: 100%; }
.fv-head { display: flex; align-items: center; gap: 6px; margin-bottom: 8px; }
.fv-title {
  flex: 1; min-width: 0;
  display: flex; align-items: center; gap: 6px;
}
.fv-path {
  font-family: ui-monospace, monospace; font-size: 13px; font-weight: 600;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.fv-tabs { display: flex; gap: 4px; flex: none; }
.fv-diff, .fv-file { flex: 1; min-height: 0; }
/* 差异:独立上下 + 水平滚动容器;整行背景用 .diff-inner 撑宽。 */
.diff-scroll {
  overflow: auto; max-height: calc(100%);
  font-family: ui-monospace, monospace; font-size: 12px; line-height: 1.5;
}
.diff-inner { width: max-content; min-width: 100%; }
.dl { white-space: pre; padding: 0 10px; }
.dl.add { background: rgba(22, 163, 74, 0.16); }
.dl.del { background: rgba(220, 38, 38, 0.16); }
.dl.hunk { color: var(--lr-accent); background: rgba(127, 127, 127, 0.1); }
.dl.meta { color: var(--lr-fg-muted); }
/* 文件内容:等宽可滚读;外层 .file-body 管滚动,内层 code 管代码字体与换行。
   hljs 多数输出<span class="hljs-*">,块级 code 保证每个令牌行内衔接且背景铺满。 */
.file-body {
  margin: 0; max-height: 100%; overflow: auto;
  padding: 8px 0;
}
.file-body code {
  display: block;
  font-family: ui-monospace, monospace; font-size: 12px; line-height: 1.5;
  color: var(--lr-fg);
  white-space: pre; word-break: break-word;
}
</style>

<!-- 语法高亮令牌色放在非 scoped 块:v-html 注入的 <span class="hljs-*"> 不带组件
     scope 属性,scoped 选择器永远匹配不上,必须用全局(非 scoped)样式才能着色。
     .file-body 前缀限定作用域,映射到 --lr-* 令牌,浅/深主题自动适配。 -->
<style>
.file-body :where(.hljs) {
  color: var(--lr-fg);
}
.file-body .hljs-attr, .file-body .hljs-selector-tag, .file-body .hljs-name { color: #c26; }
.file-body .hljs-comment, .file-body .hljs-quote { color: var(--lr-fg-muted); font-style: italic; }
.file-body .hljs-keyword, .file-body .hljs-selector-class, .file-body .hljs-meta {
  color: #a626a4;
}
.file-body .hljs-string, .file-body .hljs-regexp, .file-body .hljs-addition {
  color: #3d8c3e;
}
.file-body .hljs-number, .file-body .hljs-literal, .file-body .hljs-deletion {
  color: #b76b01;
}
.file-body .hljs-title, .file-body .hljs-title.function_, .file-body .hljs-section,
.file-body .hljs-built_in {
  color: #286983;
}
.file-body .hljs-type, .file-body .hljs-class, .file-body .hljs-selector-id {
  color: #4078f2;
}
.file-body .hljs-variable, .file-body .hljs-params, .file-body .hljs-property {
  color: var(--lr-fg);
}
.file-body .hljs-bullet, .file-body .hljs-emphasis, .file-body .hljs-strong {
  color: var(--lr-fg);
  font-weight: 600;
}
/* 深浅主题:dark 下色板要够亮,覆盖上面偏暗的 token。 */
@media (prefers-color-scheme: dark) {
  .file-body .hljs-attr, .file-body .hljs-selector-tag, .file-body .hljs-name { color: #f7797b; }
  .file-body .hljs-keyword, .file-body .hljs-selector-class, .file-body .hljs-meta { color: #d88cf6; }
  .file-body .hljs-string, .file-body .hljs-regexp, .file-body .hljs-addition { color: #8fcc7a; }
  .file-body .hljs-number, .file-body .hljs-literal, .file-body .hljs-deletion { color: #ffc37a; }
  .file-body .hljs-title, .file-body .hljs-title.function_, .file-body .hljs-section,
  .file-body .hljs-built_in { color: #7bd5ff; }
  .file-body .hljs-type, .file-body .hljs-class, .file-body .hljs-selector-id { color: #9bb8ff; }
}
</style>
