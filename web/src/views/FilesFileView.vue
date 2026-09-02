<script setup lang="ts">
// 文件浏览器二级页:单个文件的 只读语法高亮预览 / 编辑 视图。
// 一级(FilesView)点某文件 → 路由到本页,查询参数带 path(绝对路径)与 name(展示标题)。
// 默认只读预览;点"编辑"进入 textarea 编辑,可替换/保存。搜索在预览模式定位高亮命中。
import { ref, computed, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  NButton, NIcon, NSpin, NEmpty, NInput, useMessage,
} from 'naive-ui'
import {
  ChevronBackOutline, SearchOutline, CreateOutline, SaveOutline,
  CloseOutline, ChevronUpOutline, ChevronDownOutline, SwapHorizontalOutline,
} from '@vicons/ionicons5'
import { api } from '@/api/client'
import { highlightCode } from '@/utils/highlight'

const route = useRoute()
const router = useRouter()
const message = useMessage()

const path = (route.query.path as string) || ''
const name = ref((route.query.name as string) || basename(path))

function basename(p: string): string {
  const trimmed = p.replace(/\/+$/, '')
  return trimmed.split('/').pop() || p
}

const MAX_EDIT = 512 * 1024

// ---- 内容加载 ----
const content = ref('')
const editText = ref('')
const loadError = ref('')
const loading = ref(false)

async function load() {
  if (!path) {
    loadError.value = '缺少文件路径参数'
    return
  }
  loading.value = true
  loadError.value = ''
  try {
    content.value = await api.fsRead(path)
    editText.value = content.value
  } catch (e: any) {
    loadError.value = `无法读取文件:${e?.message || e || '未知错误'}`
    content.value = ''
    editText.value = ''
  } finally {
    loading.value = false
  }
}

// ---- 编辑模式 ----
const editing = ref(false)

function enterEdit() {
  if (content.value.length > MAX_EDIT) {
    message.warning('文件过大,超过 512KB,无法编辑')
    return
  }
  editText.value = content.value
  editing.value = true
}

async function save() {
  try {
    await api.fsWrite(path, editText.value)
    message.success('已保存')
    content.value = editText.value
    editing.value = false
  } catch (e: any) {
    message.error(`保存失败:${e?.message || e || '未知错误'}`)
  }
}

function cancelEdit() {
  editText.value = content.value
  editing.value = false
}

// ---- 搜索(预览模式,只读定位) ----
const searchQ = ref('')
const searchRegex = ref(false)
const searchCase = ref(false)
const searchOpen = ref(false) // 顶部工具栏「搜索」按钮是否展开输入行
const replaceOpen = ref(false) // 顶部工具栏「替换」按钮是否展开输入行
const hits = ref<{ start: number; end: number }[]>([])
const hitIndex = ref(-1) // -1 = 未定位(无当前命中)

const preEl = ref<HTMLElement | null>(null)

// 依据开关构造搜索正则。非正则模式转义特殊字符;g 或 gi 视大小写。
function buildRegex(q: string): RegExp {
  const src = searchRegex.value ? q : q.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  return new RegExp(src, searchCase.value ? 'gi' : 'g')
}

// 记录上一次搜索的"指纹"(关键词+开关),用于判断重复点击时是否要"下一个"。
let lastSig = ''

function doSearch() {
  if (!searchQ.value) {
    hits.value = []
    hitIndex.value = -1
    lastSig = ''
    return
  }
  const sig = searchQ.value + '|' + (searchRegex.value ? 1 : 0) + (searchCase.value ? 1 : 0)
  const re = buildRegex(searchQ.value)
  const found: { start: number; end: number }[] = []
  let m: RegExpExecArray | null
  // 用 exec 循环收集所有命中下标(RegExp 带 g,lastIndex 自动推进)。
  while ((m = re.exec(content.value)) !== null) {
    found.push({ start: m.index, end: m.index + m[0].length })
    // 空命中会死循环:手动推进 lastIndex。
    if (m[0].length === 0) re.lastIndex++
  }
  hits.value = found
  // 关键:若这次与上次是同一查询(关键词/开关都没变),重复点击视为"跳到下一个命中"。
  if (sig === lastSig && found.length) {
    hitIndex.value = (hitIndex.value + 1 + found.length) % found.length
    scrollToHit()
    return
  }
  lastSig = sig
  hitIndex.value = found.length ? 0 : -1
  scrollToHit()
}

function gotoHit(next: number) {
  if (!hits.value.length) return
  hitIndex.value = (hitIndex.value + next + hits.value.length) % hits.value.length
  scrollToHit()
}

function scrollToHit() {
  requestAnimationFrame(() => {
    if (hitIndex.value < 0 || hitIndex.value >= hits.value.length) return
    const marks = preEl.value?.querySelectorAll<HTMLElement>('mark.hit')
    const el = marks?.[hitIndex.value]
    el?.scrollIntoView({ block: 'center', behavior: 'smooth' })
  })
}

const currentHit = computed(() =>
  hitIndex.value >= 0 && hitIndex.value < hits.value.length ? hitIndex.value + 1 : 0)

// ---- 渲染(预览/编辑共用):命中切片包进 <mark class="hit">,其余片段走 highlightCode 高亮。
// 单独抽出,让 content(预览)与 editText(编辑)都经过同一套命中高亮逻辑。
function renderHighlight(text: string): string {
  if (!text) return ''
  if (!hits.value.length) return highlightCode(text, path)
  let out = ''
  let pos = 0
  for (const h of hits.value) {
    if (h.start > pos) out += highlightCode(text.slice(pos, h.start), path)
    // 命中段本身也过 highlightCode,保留语法色,外层再包 mark。
    out += `<mark class="hit">${highlightCode(text.slice(h.start, h.end), path)}</mark>`
    pos = h.end
  }
  if (pos < text.length) out += highlightCode(text.slice(pos), path)
  return out
}

// 当前展示的原始文本:编辑态取 editText,预览态取 content。
const displayText = computed(() => (editing.value ? editText.value : content.value))

// 行号 1..N,基于当前展示文本的换行数,编辑态随换行实时更新。
const gutterLines = computed<number[]>(() => {
  const n = displayText.value.split('\n').length
  return Array.from({ length: n }, (_, i) => i + 1)
})

// 覆盖层要显示的语法高亮 HTML。
const displayHtml = computed<string>(() => renderHighlight(displayText.value))

// ---- 替换(仅编辑模式) ----
const replaceText = ref('')

function replaceAll() {
  if (!searchQ.value) {
    message.warning('请先输入查找内容')
    return
  }
  const re = buildRegex(searchQ.value)
  const count = (editText.value.match(re) || []).length
  editText.value = editText.value.replace(re, replaceText.value)
  message.success(`已替换 ${count} 处`)
}

function goBack() {
  router.back()
}

onMounted(load)
</script>

<template>
  <div class="file-view page-content">
    <!-- 顶部工具栏:固定在最上,滚动代码时始终可见。图标钮→点一下在下方展开对应输入行。 -->
    <div class="tom-bar">
      <div class="tom-row">
        <n-button quaternary size="small" title="返回列表" aria-label="返回列表" @click="goBack">
          <template #icon><n-icon :component="ChevronBackOutline" /></template>
        </n-button>
        <div class="fv-title">
          <div class="fv-path" :title="path">{{ name }}</div>
        </div>
        <div class="tom-tools">
          <n-button quaternary size="small" :type="searchOpen ? 'primary' : 'default'"
            title="搜索" aria-label="搜索" @click="searchOpen = !searchOpen">
            <template #icon><n-icon :component="SearchOutline" /></template>
          </n-button>
          <n-button v-if="editing" quaternary size="small" :type="replaceOpen ? 'primary' : 'default'"
            title="替换" aria-label="替换" @click="replaceOpen = !replaceOpen">
            <template #icon><n-icon :component="SwapHorizontalOutline" /></template>
          </n-button>
          <template v-if="!editing">
            <n-button quaternary size="small" type="primary" title="编辑" aria-label="编辑" @click="enterEdit">
              <template #icon><n-icon :component="CreateOutline" /></template>
            </n-button>
          </template>
          <template v-else>
            <n-button quaternary size="small" type="primary" title="保存" aria-label="保存" @click="save">
              <template #icon><n-icon :component="SaveOutline" /></template>
            </n-button>
            <n-button quaternary size="small" title="取消" aria-label="取消" @click="cancelEdit">
              <template #icon><n-icon :component="CloseOutline" /></template>
            </n-button>
          </template>
        </div>
      </div>

      <!-- 搜索输入行:点搜索钮展开 -->
      <div v-if="searchOpen" class="tom-panel" @keydown.esc="searchOpen = false">
        <n-input v-model:value="searchQ" size="small" placeholder="搜索当前文件" class="tom-search" clearable
          @keyup.enter="doSearch" @keyup.esc="searchOpen = false" />
        <n-button size="small" :type="searchRegex ? 'primary' : 'default'" quaternary
          title="正则" aria-label="正则" @click="searchRegex = !searchRegex">.*</n-button>
        <n-button size="small" :type="searchCase ? 'primary' : 'default'" quaternary
          title="区分大小写" aria-label="区分大小写" @click="searchCase = !searchCase">Aa</n-button>
        <n-button size="small" type="primary" title="搜索" aria-label="搜索" @click="doSearch">
          <template #icon><n-icon :component="SearchOutline" /></template>
        </n-button>
        <span class="tom-sep"></span>
        <span v-if="searchQ && hits.length" class="ft-counter">{{ currentHit }}/{{ hits.length }}</span>
        <span v-else-if="searchQ && !hits.length" class="ft-nohit">无命中</span>
        <n-button size="small" quaternary :disabled="!hits.length" title="上一个" aria-label="上一个" @click="gotoHit(-1)">
          <template #icon><n-icon :component="ChevronUpOutline" /></template>
        </n-button>
        <n-button size="small" quaternary :disabled="!hits.length" title="下一个" aria-label="下一个" @click="gotoHit(1)">
          <template #icon><n-icon :component="ChevronDownOutline" /></template>
        </n-button>
      </div>

      <!-- 替换输入行:点替换钮展开(仅编辑态) -->
      <div v-if="editing && replaceOpen" class="tom-panel" @keydown.esc="replaceOpen = false">
        <n-input v-model:value="searchQ" size="small" placeholder="查找" class="tom-search" clearable
          @keyup.enter="doSearch" />
        <n-button size="small" type="primary" title="查找" aria-label="查找" @click="doSearch">
          <template #icon><n-icon :component="SearchOutline" /></template>
        </n-button>
        <n-input v-model:value="replaceText" size="small" placeholder="替换为" class="tom-search" />
        <n-button size="small" type="error" title="全部替换" aria-label="全部替换" @click="replaceAll">
          <template #icon><n-icon :component="SwapHorizontalOutline" /></template>
        </n-button>
      </div>
    </div>

    <!-- 主体 -->
    <n-spin :show="loading" class="fv-body">
      <div v-if="loadError" class="fv-error">{{ loadError }}</div>
      <n-empty v-else-if="!editing && !content" description="文件内容为空" style="padding: 24px" />

      <!-- 预览/编辑 共用编辑器:高亮 <pre> 打底 + 透明 <textarea> 覆盖,行号在左侧粘性栏。 -->
      <div v-else ref="editorEl" class="code-editor">
        <div ref="gutterEl" class="ce-gutter">
          <span v-for="n in gutterLines" :key="n">{{ n }}</span>
        </div>
        <div ref="preEl" class="ce-body">
          <pre><code v-html="displayHtml"></code></pre>
          <textarea ref="inputEl" class="ce-input" :readonly="!editing" v-model="editText"
            spellcheck="false" aria-label="文件内容"></textarea>
        </div>
      </div>
    </n-spin>
  </div>
</template>

<style scoped>
.file-view { display: flex; flex-direction: column; height: 100%; }
/* 顶部工具栏:sticky 固定在最上,代码滚动时始终可见。 */
.tom-bar {
  position: sticky;
  top: 0;
  z-index: 20;
  background: var(--lr-bg);
  box-shadow: 0 1px 0 rgba(127, 127, 127, 0.12);
  border-radius: var(--lr-radius);
  flex: none;
}
.tom-row {
  display: flex; align-items: center; gap: 6px;
  padding: 4px 0;
}
.fv-title {
  flex: 1; min-width: 0;
  display: flex; align-items: center; gap: 6px;
}
.fv-path {
  font-family: ui-monospace, monospace; font-size: 13px; font-weight: 600;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.tom-tools { flex: none; display: flex; align-items: center; gap: 2px; }
/* 搜索/替换 展开的输入行 */
.tom-panel {
  display: flex; align-items: center; gap: 4px;
  padding: 6px 0 8px;
  flex-wrap: nowrap;
}
.tom-search { width: 180px; }
.tom-sep { flex: 1; }
.tom-search :deep(.n-input__input-el),
.tom-search :deep(.n-input__placeholder) { line-height: 24px; }
.ft-counter {
  flex: none; font-size: 12px; color: var(--lr-fg-muted);
  font-family: ui-monospace, monospace; white-space: nowrap;
}
.ft-nohit {
  flex: none; font-size: 12px; color: var(--lr-danger, #d03050);
  font-family: ui-monospace, monospace; white-space: nowrap;
}
.fv-body { flex: 1; min-height: 0; }
.fv-error {
  margin: 8px 0; padding: 8px 12px; border-radius: 4px;
  color: var(--lr-danger, #d03050); background: rgba(208, 48, 80, 0.1);
  font-size: 13px;
}
/* 代码编辑器:单个滚动容器承载 行号栏 + 高亮 pre + 透明 textarea。 */
.code-editor {
  position: relative;
  overflow: auto; /* 纵向横向都在这滚动,行号栏内部 sticky 跟着走 */
  flex: 1;
  min-height: 0;
  display: flex;
  align-items: flex-start;
  padding: 8px 0;
  /* 共享字级:pre/code/textarea 全部继承,保证覆盖层像素对齐 */
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
  line-height: 1.5;
  letter-spacing: normal;
  white-space: pre;
  overflow-wrap: normal;
  word-break: keep-all;
  tab-size: 4;
}
/* 行号栏:sticky 横向贴左,纵向随容器一起滚,天然与代码行对齐 */
.ce-gutter {
  position: sticky;
  left: 0;
  z-index: 2;
  flex: none;
  min-width: 14px;
  padding: 0 4px 0 0;
  text-align: right;
  user-select: none;
  color: var(--lr-fg-muted);
  background: var(--lr-bg);
}
.ce-gutter span { display: block; }
/* 高亮层:宽度贴合内容,让 textarea 能精确覆盖到行尾 */
.ce-body { position: relative; flex: none; width: max-content; }
.ce-body pre { margin: 0; white-space: pre; }
.ce-body pre code { display: block; padding: 0; color: var(--lr-fg); }
/* 覆盖层 textarea:绝对铺满 .ce-body(= pre 同尺寸),透明文字只留光标。
   .ce-body 随内容变宽变高,textarea 一并长,纵向滚动时逐行仍与高亮对齐。 */
.ce-input {
  position: absolute;
  top: 0; left: 0;
  width: 100%; height: 100%;
  margin: 0; padding: 0; border: 0; outline: none; resize: none;
  background: transparent;
  color: transparent;
  -webkit-text-fill-color: transparent;
  caret-color: var(--lr-fg);
  font-family: inherit; font-size: inherit; line-height: inherit;
  letter-spacing: inherit; tab-size: inherit;
  white-space: pre; overflow-wrap: normal; word-break: keep-all;
  overflow: hidden;
}
.ce-input::selection { background: rgba(64, 120, 242, 0.35); }
/* 只读(预览)态:仍可点选/滚动,但光标不闪烁可输入 */
.ce-input[readonly] { user-select: none; }
</style>

<!-- 语法高亮令牌色 + 命中标记:非 scoped。v-html 注入的 <span class="hljs-*"> 与
     <mark class="hit"> 不带组件 scope 属性,scoped 选择器永远匹配不上,须用全局样式。
     .file-body 前缀限定作用域,映射到 --lr-* 令牌,浅/深主题自动适配。 -->
<style>
.ce-body { margin: 0; max-height: 100%; overflow: auto; padding: 8px 0; }
.ce-body code {
  display: block;
  font-family: ui-monospace, monospace; font-size: 12px; line-height: 1.5;
  color: var(--lr-fg);
  white-space: pre; word-break: break-word;
}
.ce-body :where(.hljs) { color: var(--lr-fg); }
.ce-body .hljs-attr, .ce-body .hljs-selector-tag, .ce-body .hljs-name { color: #c26; }
.ce-body .hljs-comment, .ce-body .hljs-quote { color: var(--lr-fg-muted); font-style: italic; }
.ce-body .hljs-keyword, .ce-body .hljs-selector-class, .ce-body .hljs-meta { color: #a626a4; }
.ce-body .hljs-string, .ce-body .hljs-regexp, .ce-body .hljs-addition { color: #3d8c3e; }
.ce-body .hljs-number, .ce-body .hljs-literal, .ce-body .hljs-deletion { color: #b76b01; }
.ce-body .hljs-title, .ce-body .hljs-title.function_, .ce-body .hljs-section,
.ce-body .hljs-built_in { color: #286983; }
.ce-body .hljs-type, .ce-body .hljs-class, .ce-body .hljs-selector-id { color: #4078f2; }
.ce-body .hljs-variable, .ce-body .hljs-params, .ce-body .hljs-property { color: var(--lr-fg); }
.ce-body .hljs-bullet, .ce-body .hljs-emphasis, .ce-body .hljs-strong {
  color: var(--lr-fg); font-weight: 600;
}
/* 搜索命中标记:命中仍保留语法色(mark 内嵌 hljs span),仅加底色高亮。 */
.ce-body mark.hit {
  background: rgba(255, 213, 0, 0.45);
  color: var(--lr-fg);
  border-radius: 2px;
  padding: 0;
}
@media (prefers-color-scheme: dark) {
  .ce-body .hljs-attr, .ce-body .hljs-selector-tag, .ce-body .hljs-name { color: #f7797b; }
  .ce-body .hljs-keyword, .ce-body .hljs-selector-class, .ce-body .hljs-meta { color: #d88cf6; }
  .ce-body .hljs-string, .ce-body .hljs-regexp, .ce-body .hljs-addition { color: #8fcc7a; }
  .ce-body .hljs-number, .ce-body .hljs-literal, .ce-body .hljs-deletion { color: #ffc37a; }
  .ce-body .hljs-title, .ce-body .hljs-title.function_, .ce-body .hljs-section,
  .ce-body .hljs-built_in { color: #7bd5ff; }
  .ce-body .hljs-type, .ce-body .hljs-class, .ce-body .hljs-selector-id { color: #9bb8ff; }
  .ce-body mark.hit { background: rgba(255, 213, 0, 0.35); }
}
</style>
