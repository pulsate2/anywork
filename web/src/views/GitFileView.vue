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

// 差异行拆成三列:行号 / 符号 / 正文。符号单独占一列,颜色之外再给一道形状线索,
// 正文里那个 +/- 就要去掉,否则每行都多出一个字符、缩进跟着错一位。
const diffRows = computed(() => (diffBlock.value?.lines ?? []).map((l) => ({
  kind: l.kind,
  no: l.no,
  sign: l.kind === 'add' ? '+' : l.kind === 'del' ? '-' : '',
  body: l.kind === 'add' || l.kind === 'del' ? l.text.slice(1) : l.text,
})))

// 行号槽的宽度按本文件最大行号的位数算,不按"万行文件"的最坏情况留死宽 ——
// 数字是右对齐的,槽比数字宽多少,左边就空多少,两位数的文件白扔三个字符的宽度。
// 位数下限取 2,hunk 行那个 @@ 也要放得下。
// 单位用 px 而不是 ch:同一个值还要喂给 .dl-sign 的 left,而那一列字号跟行号列不同,
// ch 会各按自己的 font-size 解析,两列就错开了。0.62 是等宽字数字步进(通常 0.6em)
// 留一点余量,免得字体步进偏宽时把数字挤出槽外。
const NO_FONT_PX = 11
const noWidth = computed(() => {
  let max = 0
  for (const r of diffRows.value) if (r.no && r.no > max) max = r.no
  const digits = Math.max(2, String(max).length)
  return `${Math.ceil(digits * NO_FONT_PX * 0.62) + 6}px`
})

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
        <!-- 文件名在差异视图里由卡头承担;文件视图没有卡头(它的高亮跟深浅主题走,不能
             套在参考稿那张固定浅底的卡上),所以这时候仍留在吸顶条上。 -->
        <div v-if="mode === 'file'" class="fv-path" :title="title">{{ title }}</div>
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
        <div v-else class="diff-card">
          <!-- 卡头(参考稿的 .diff-header):文件名在左,增删计数跟在右边。 -->
          <div class="diff-head">
            <span class="dh-file" :title="title">{{ title }}</span>
            <span class="dh-add">+{{ diffBlock.adds }}</span>
            <span class="dh-del">-{{ diffBlock.dels }}</span>
          </div>
          <div class="diff-scroll" :style="{ '--dl-no-w': noWidth }">
            <div class="diff-inner">
              <div v-for="(l, i) in diffRows" :key="i" class="dl" :class="l.kind">
                <span class="dl-no">{{ l.kind === 'hunk' ? '@@' : (l.no ?? '') }}</span>
                <span class="dl-sign" aria-hidden="true">{{ l.sign }}</span>
                <span class="dl-body">{{ l.body || ' ' }}</span>
              </div>
            </div>
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
.file-view { display: flex; flex-direction: column; }
/* 顶部栏吸顶:整页走文档滚动(App 里没有 overflow 容器),所以 top: 0 就贴在视口顶上。
   长 diff 翻到几百行开外时,返回钮和 差异/文件 切换仍然点得到。
   左右负外边距抵掉 .page-content 的留白、再用等量 padding 补回来:不这么做的话
   底色只覆盖内容宽度,diff 会从吸顶条两侧的缝里透出来往上滚。 */
.fv-head {
  position: sticky;
  top: 0;
  z-index: 20;
  display: flex; align-items: center; gap: 6px;
  margin: 0 calc(-1 * var(--lr-page-pad)) 8px calc(-1 * var(--lr-page-pad-left));
  padding: 8px var(--lr-page-pad);
  padding-left: var(--lr-page-pad-left);
  background: var(--lr-bg);
  box-shadow: 0 1px 0 rgba(127, 127, 127, 0.2);
}
.fv-title {
  flex: 1; min-width: 0;
  display: flex; align-items: center; gap: 6px;
}
.fv-path {
  font-family: ui-monospace, monospace; font-size: 13px; font-weight: 600;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.fv-tabs { display: flex; gap: 4px; flex: none; }
/* 差异:整块是一张卡(参考稿的 .diff-container)。卡负责边框/圆角,overflow: hidden
   让里面的行底被圆角裁掉;横向滚动交给内层 .diff-scroll,纵向交给文档滚动 —— 卡自己
   不能滚,否则吸顶条就没得吸了。
   卡面固定浅底(参考稿的白 + #24292f 深字),不跟深浅主题翻转:红绿严格用参考稿的
   #ccffcc / #ffcccc,那套值本来就是配浅底的,深底上再用就不是那个颜色了。 */
.diff-card {
  background: var(--lr-diff-surface);
  border: 1px solid var(--lr-diff-border);
  border-radius: 6px;
  box-shadow: 0 1px 3px rgba(0, 0, 0, 0.1);
  overflow: hidden;
}
/* 手机屏窄:390px 一屏里,页面左右各 12px 的留白就顶掉七八个字符。卡横向撑到接近满屏
   (左右各留 2px,390px 屏上约 99%)—— 负外边距抵掉 .page-content 的留白,只留那 2px。
   桌面端不改:那边左侧是导航栏,同样的负外边距会把卡塞到栏底下去。
   吸顶条仍留 12px 留白,返回钮和切换钮不该贴着屏幕边缘。 */
@media (max-width: 767px) {
  .diff-card {
    margin-left: calc(2px - var(--lr-page-pad-left));
    margin-right: calc(2px - var(--lr-page-pad));
  }
}
/* 卡头:参考稿的 .diff-header。文件名占满剩余宽度、太长时从左边省略(尾部的文件名
   比开头的目录名重要),计数被 flex: none 顶在右侧。 */
.diff-head {
  display: flex; align-items: center; gap: 8px;
  padding: 10px 12px;
  background: var(--lr-diff-head);
  border-bottom: 1px solid var(--lr-diff-border);
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
  font-size: 12px; font-weight: 600;
}
.dh-file {
  flex: 1; min-width: 0;
  color: var(--lr-diff-code);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap; direction: rtl;
  text-align: left;
}
.dh-add { flex: none; color: #1a7f37; }
.dh-del { flex: none; color: var(--lr-diff-del-fg); }
.diff-scroll {
  overflow-x: auto;
  color: var(--lr-diff-code);
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
  font-size: 13px; line-height: 1.5;
}
/* 整行背景必须铺到最宽那行的末尾:width: max-content 让内层跟着最长行走,
   min-width: 100% 保证短行也铺满可见宽度。 */
.diff-inner { width: max-content; min-width: 100%; }
.dl { display: flex; }
/* 行号槽 + 符号列都横向 sticky 贴左:长行右滚时这两列不跑掉,始终知道看的是第几行、
   是加还是删。两列各自带背景,否则正文会从它们底下透过去。
   槽宽是 --dl-no-w,按本文件最大行号的位数在脚本里算(见 noWidth)—— 死宽会按最坏情况
   预留,数字右对齐,左边就白空一片。左内边距只留 2px 贴边。
   .dl-sign 的 left 必须等于槽宽,否则右滚时两列会叠在一起。 */
.dl-no {
  position: sticky; left: 0; z-index: 1;
  flex: none; width: var(--dl-no-w, 42px);
  padding: 0 6px 0 2px;
  text-align: right;
  color: var(--lr-diff-no);
  background: var(--lr-diff-surface);
  font-size: 11px;
  user-select: none;
}
/* +/- 符号单列:颜色之外的第二道线索,色觉障碍或黑白打印时靠它分加删。
   从正文里摘出来单独放,缩进才不会整体错一位 —— 三种行的正文都从同一列起头。 */
.dl-sign {
  position: sticky; left: var(--dl-no-w, 42px); z-index: 1;
  flex: none; width: 12px;
  text-align: center; font-weight: bold;
  background: var(--lr-diff-surface);
  user-select: none;
}
.dl-body { flex: 1; white-space: pre; padding: 0 8px 0 2px; }
/* 加/删行:行底 + 行号槽两档,值严格照参考稿(#ccffcc/#aaffaa、#ffcccc/#ffaaaa)。
   删行连正文一起染 #b71c1c,也是参考稿的做法;加行正文留卡面深字。 */
.dl.add { background: var(--lr-diff-add-bg); }
.dl.add .dl-no, .dl.add .dl-sign { background: var(--lr-diff-add-gutter); }
.dl.del { background: var(--lr-diff-del-bg); color: var(--lr-diff-del-fg); }
.dl.del .dl-no, .dl.del .dl-sign {
  background: var(--lr-diff-del-gutter); color: var(--lr-diff-del-fg);
}
/* hunk 头:参考稿的蓝,不跟加/删抢红绿。整行同色,行号槽不另开一档。 */
.dl.hunk {
  background: var(--lr-diff-hunk-bg); color: var(--lr-diff-hunk-fg); font-weight: 600;
}
.dl.hunk .dl-no, .dl.hunk .dl-sign {
  background: var(--lr-diff-hunk-bg); color: var(--lr-diff-hunk-fg);
}
.dl.meta { color: #57606a; }
/* 文件内容:等宽可滚读;外层 .file-body 管横向滚动,内层 code 管代码字体与换行。
   hljs 多数输出<span class="hljs-*">,块级 code 保证每个令牌行内衔接且背景铺满。 */
.file-body {
  margin: 0; overflow: auto;
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
