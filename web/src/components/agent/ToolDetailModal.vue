<script setup lang="ts">
// 工具调用详情弹窗(全局唯一,挂在 AgentView 层):单卡头部、组卡行、
// 子 agent 过程条目点击都打开它。
// 挂在视图层而不是各卡片内部,是因为实时回合里卡片会重组:审批请求到达时
// 卡片从组里拆成单卡,答复后又并回去,卡片一卸载它内部的弹窗就没了 ——
// 表现为「点开就关/点不开」。视图层的弹窗不随卡片重组卸载;父组件按
// toolUseId 传最新的调用对象进来,结果晚到弹窗里也能接着看到。
// 详情内容三处(原单卡/组卡/子过程)逻辑的合并版,统一取最全的实现
// (编辑类 diff、ApplyPatch/CodexDiff、截断信封兜底、结果图片)。
import { computed, ref } from 'vue'
import { NModal } from 'naive-ui'
import { api, type AgentToolCall } from '@/api/client'
import DiffView from './DiffView.vue'

const props = defineProps<{
  // null = 关。调用对象由父组件解析成"活"引用(重组/回填后仍是最新那份)。
  call: AgentToolCall | null
  // 会话 id:tool_result 的 image 块经 /api/agent/sessions/{id}/files/{name} 取。
  sessionId?: string
}>()

const emit = defineEmits<{ (e: 'close'): void }>()

const show = computed(() => !!props.call)

// ---- 参数解析 ----
// 严格 parse:args 是 JSON 字符串。截断的非法 JSON 返回 null。
function parseArgs(c: AgentToolCall | null): Record<string, unknown> | null {
  try {
    const v = c?.args ? JSON.parse(c.args) : null
    return v && typeof v === 'object' ? v as Record<string, unknown> : null
  } catch { return null }
}

// 截断兜底:后端信封形态 {"truncated":true,"raw":"…前缀…"} 的参数虽 parse 不回
// 结构,diff/摘要都取不到字段,但 raw 前缀是合法 JSON 的开头 —— 再 parse 一次,
// 能解出对象就把已到手的部分当参数用(仅丢了被截字段的后半,前面字段仍可渲染)。
function parseArgsLenient(c: AgentToolCall | null): Record<string, unknown> | null {
  const strict = parseArgs(c)
  if (strict) return strict
  const raw = c?.args
  if (!raw) return null
  // raw 是被截断的 JSON 字符串原文(自身带引号与转义):先解出一层拿到原文,
  // 剥掉截断提示后补一个收尾引号,常能把外层对象解出来 —— 截断多落在字符串
  // 值中间,补上引号和收尾括号后其余字段(如 file_path)完整可用。
  try {
    const env = JSON.parse(raw) as { truncated?: boolean; raw?: string }
    if (!env || env.truncated !== true || typeof env.raw !== 'string') return null
    let body = env.raw
    const cut = body.lastIndexOf('…(已截断)')
    if (cut >= 0) body = body.slice(0, cut)
    for (const patched of [body + '"}', body + '"', body]) {
      try {
        const v = JSON.parse(patched)
        if (v && typeof v === 'object') return v as Record<string, unknown>
      } catch { /* 下一种补法 */ }
    }
  } catch { /* 不是信封形态 */ }
  return null
}

// 摘要行给一眼能认出的关键参数:命令(Bash 的 command)、路径、文件名。
const brief = computed(() => {
  const args = parseArgs(props.call)
  if (!args) return ''
  for (const key of ['command', 'file_path', 'path', 'pattern', 'url', 'query', 'task_name', 'description', 'task_id', 'target', 'message']) {
    if (typeof args[key] === 'string' && (args[key] as string)) return args[key] as string
  }
  const first = Object.values(args)[0]
  if (typeof first === 'string' && first) return first
  return ''
})

// 完整命令/参数值(弹窗里用):能解析出 command 等关键值就单取它,
// 解析不出再贴 args 原文。
const command = computed(() => brief.value || props.call?.args || '')

// ---- 编辑类工具的 diff ----
interface DiffBlock { old: string; new: string; path?: string }

// ApplyPatch(codex filechange):changes 数组每项 {path, kind:{type}, diff}。
// add:diff 是文件全文;delete:全文即被删内容(放 old 侧);update:codex 实测
// diff 给的就是新内容文本,旧文不随通知来。
const patchDiff = computed<DiffBlock[] | null>(() => {
  if (props.call?.tool !== 'ApplyPatch') return null
  const args = parseArgs(props.call)
  if (!args || !Array.isArray(args.changes)) return null
  const blocks: DiffBlock[] = []
  for (const e of args.changes) {
    if (!e || typeof e !== 'object') continue
    const path = typeof (e as any).path === 'string' ? (e as any).path : ''
    const diff = typeof (e as any).diff === 'string' ? (e as any).diff : ''
    const kind = (e as any)?.kind?.type || ''
    if (!path || !diff) continue
    if (kind === 'delete') blocks.push({ old: diff, new: '', path })
    else blocks.push({ old: '', new: diff, path })
  }
  return blocks.length ? blocks : null
})

// ApplyPatch 的文件清单(解析不出 diff 时兜底列文件)。
const patchFiles = computed<string[] | null>(() => {
  if (props.call?.tool !== 'ApplyPatch') return null
  const args = parseArgs(props.call)
  if (!args) return null
  const out: string[] = []
  if (Array.isArray(args.changes)) {
    for (const e of args.changes) {
      if (e && typeof e === 'object') {
        const v = (e as any).path ?? (e as any).file ?? (e as any).filePath ?? (e as any).file_path
        if (typeof v === 'string' && v) out.push(v)
      }
    }
  } else if (args.changes && typeof args.changes === 'object') {
    for (const k of Object.keys(args.changes as object)) out.push(k)
  }
  return out.length ? out : null
})

// CodexDiff:unified diff 解析成 per-file 的 old/new 文本对。
// 按 "diff --git" 边界分文件;单文件没头也兜得住。行级归类:hunk 外跳过,
// +→new,-→old,空格→两侧。
function parseUnifiedDiff(text: string): DiffBlock[] {
  const blocks: DiffBlock[] = []
  let oldLines: string[] = []
  let newLines: string[] = []
  let fileName = ''
  let inHunk = false
  const flush = () => {
    if (oldLines.length || newLines.length || fileName) {
      blocks.push({ old: oldLines.join('\n'), new: newLines.join('\n'), path: fileName })
    }
    oldLines = []; newLines = []; fileName = ''; inHunk = false
  }
  for (const line of text.split('\n')) {
    if (line.startsWith('diff --git')) { flush(); continue }
    if (line.startsWith('+++ b/') || line.startsWith('+++ ')) {
      fileName = line.replace(/^\+\+\+ (b\/)?/, '')
      continue
    }
    if (line.startsWith('---') || line.startsWith('index ')
      || line.startsWith('new file mode') || line.startsWith('deleted file mode')) continue
    if (line.startsWith('@@')) { inHunk = true; continue }
    if (!inHunk) continue
    if (line.startsWith('+')) newLines.push(line.substring(1))
    else if (line.startsWith('-')) oldLines.push(line.substring(1))
    else if (line.startsWith(' ')) { oldLines.push(line.substring(1)); newLines.push(line.substring(1)) }
    else if (line === '\\ No newline at end of file') continue
    else if (line === '') { oldLines.push(''); newLines.push('') }
  }
  flush()
  return blocks
}

// Edit: old_string → new_string;Write: 全新内容(old 为空);
// MultiEdit: edits 数组逐个展开(顺序即生效顺序)。
const diffBlocks = computed<DiffBlock[] | null>(() => {
  const c = props.call
  if (!c) return null
  const t = c.tool
  if (t === 'ApplyPatch') return patchDiff.value
  if (t === 'CodexDiff') {
    const args = parseArgsLenient(c)
    if (args && typeof args.unified_diff === 'string') {
      const blocks = parseUnifiedDiff(args.unified_diff)
      return blocks.length ? blocks : null
    }
    return null
  }
  if (t !== 'Edit' && t !== 'MultiEdit' && t !== 'Write' && t !== 'NotebookEdit') return null
  const args = parseArgsLenient(c)
  if (!args) return null
  const filePath = typeof args.file_path === 'string' ? args.file_path : ''
  if (t === 'MultiEdit' && Array.isArray(args.edits)) {
    const blocks: DiffBlock[] = []
    for (const e of args.edits) {
      if (e && typeof e === 'object' && typeof (e as any).old_string === 'string' && typeof (e as any).new_string === 'string') {
        blocks.push({ old: (e as any).old_string, new: (e as any).new_string })
      }
    }
    return blocks.length ? blocks : null
  }
  // Write 的内容字段两种形态:content(常规)或 code_content(部分 flavor)。
  const content = typeof args.content === 'string' ? args.content
    : typeof args.code_content === 'string' ? args.code_content : ''
  const oldStr = typeof args.old_string === 'string' ? args.old_string : ''
  const newStr = t === 'Write' ? content : (typeof args.new_string === 'string' ? args.new_string : '')
  if (t !== 'Write' && oldStr === '' && newStr === '') return null
  return [{ old: oldStr, new: newStr, path: filePath }]
})

// 该次结果里的图片(有会话 id 且带图才有)。
const imageUrls = computed<string[]>(() =>
  props.sessionId && props.call?.images?.length
    ? props.call.images.map((n) => api.agentFileUrl(props.sessionId!, n))
    : [],
)
const lightbox = ref<string | null>(null)
// n-modal 要 v-model:show;lightbox 值本身只是 url。
const lightboxOpen = computed({
  get: () => !!lightbox.value,
  set: (v: boolean) => { if (!v) lightbox.value = null },
})
</script>

<template>
  <n-modal
    :show="show" preset="card" :title="call?.tool || '工具'" class="tool-modal"
    @update:show="(v: boolean) => { if (!v) emit('close') }"
  >
    <div class="tool-detail">
      <template v-if="diffBlocks">
        <DiffView
          v-for="(b, i) in diffBlocks" :key="i"
          :old="b.old" :new="b.new"
          :file-path="b.path || (i === 0 ? brief : '')"
        />
      </template>
      <!-- ApplyPatch 解析不出 diff(形态未知)时兜底列文件 -->
      <div v-else-if="patchFiles" class="patch-files">
        <div v-for="(f, i) in patchFiles" :key="i" class="patch-file">{{ f }}</div>
      </div>
      <pre v-else-if="command" class="tool-block">{{ command }}</pre>
      <pre v-if="call?.result" class="tool-block result">{{ call.result }}</pre>
      <div v-else-if="call?.state === 'running'" class="tool-wait">等待结果…</div>
      <div v-if="imageUrls.length" class="tool-imgs">
        <img v-for="(u, i) in imageUrls" :key="u" :src="u" :alt="`图片 ${i + 1}`" loading="lazy" @click="lightbox = u" />
      </div>
    </div>
  </n-modal>
  <!-- 点亮的原图:独立小弹窗,Cookie 认同所以 <img src> 直连 -->
  <n-modal v-model:show="lightboxOpen" preset="card" class="tool-modal" title="图片">
    <img v-if="lightbox" :src="lightbox" class="tool-lightbox" alt="图片" />
  </n-modal>
</template>

<!-- 弹窗内容 teleport 到 body,scoped 样式作用不到,放非 scoped 块。
     规则与 ToolCallCard 非 scoped 块保持一致(两处全局重复注入无害,
     ToolCallCard 的 lightbox 也依赖它)。 -->
<style>
.tool-detail { display: flex; flex-direction: column; gap: 12px; }
.tool-modal .tool-block {
  margin: 0; padding: 8px 10px;
  border: 1px solid rgba(127, 127, 127, .14); border-radius: var(--lr-radius);
  font-family: ui-monospace, monospace; font-size: 12px; line-height: 1.5;
  white-space: pre-wrap; overflow-wrap: anywhere;
  max-height: 55vh; overflow: auto;
  color: var(--lr-fg);
}
.tool-modal .tool-block.result { background: rgba(127, 127, 127, .06); }
.tool-modal .tool-wait { padding: 8px 10px; font-size: 12px; color: var(--lr-fg-muted); }
.tool-modal .patch-files { display: flex; flex-direction: column; gap: 4px; }
.tool-modal .patch-file {
  font-family: ui-monospace, monospace; font-size: 12px; line-height: 1.5;
  color: var(--lr-fg); overflow-wrap: anywhere;
}
.tool-modal .tool-imgs { display: flex; gap: 8px; flex-wrap: wrap; }
.tool-modal .tool-imgs img {
  width: 96px; height: 96px; border-radius: 6px; cursor: zoom-in;
  border: 1px solid rgba(127, 127, 127, .2); object-fit: cover;
}
.tool-modal .tool-lightbox { max-width: 100%; max-height: 70vh; display: block; margin: 0 auto; }
</style>
