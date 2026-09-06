<script setup lang="ts">
// 工具调用单行卡片:一行摘要(工具名 + 关键参数 + 状态),点击弹窗看全量详情
// (编辑类工具是行级 diff,其余是参数原文与结果)。长输出不再把手机滚穿,
// 详情是明确动作。审批请求仍内嵌在行下方,不跟着弹窗走。
import { computed, ref } from 'vue'
import { NModal } from 'naive-ui'
import type { AgentPermissionReq, AgentToolCall } from '@/api/client'
import DiffView from './DiffView.vue'
import ApprovalCard from './ApprovalCard.vue'

const props = defineProps<{
  call: AgentToolCall
  // 内嵌审批:同工具的审批请求并进工具卡(Write/Edit 审批与调用同卡呈现)。
  pendingReq?: AgentPermissionReq
  pendingResolved?: { allow: boolean; session?: boolean } | null
  approveBusy?: boolean
}>()

const emit = defineEmits<{ (e: 'decide', allow: boolean, session: boolean): void }>()

const open = ref(false)

const stateLabel = computed(() =>
  props.call.state === 'running' ? '运行中' : props.call.state === 'error' ? '出错' : '完成',
)

function parseArgs(): Record<string, unknown> | null {
  try {
    const v = props.call.args ? JSON.parse(props.call.args) : null
    return v && typeof v === 'object' ? (v as Record<string, unknown>) : null
  } catch { /* args 已截断成非法 JSON 时整段进详情 */ }
  return null
}

// 摘要行给一眼能认出的关键参数:命令(Bash 的 command)、路径、文件名。
const brief = computed(() => {
  const args = parseArgs()
  if (!args) return ''
  for (const key of ['command', 'file_path', 'path', 'pattern', 'url', 'query']) {
    if (typeof args[key] === 'string' && (args[key] as string)) return args[key] as string
  }
  const first = Object.values(args)[0]
  if (typeof first === 'string' && first) return first
  return ''
})

// CodexDiff 的行摘要:取 unified diff 里第一个 "+++ 路径" 的文件名,
// 多文件标 (+n)。unified_diff 本身不适合贴在单行里。
const codexDiffBrief = computed(() => {
  if (props.call.tool !== 'CodexDiff') return ''
  const args = parseArgs()
  if (!args || typeof args.unified_diff !== 'string') return ''
  let name = ''
  let files = 0
  for (const line of (args.unified_diff as string).split('\n')) {
    if (line.startsWith('+++ ')) {
      files++
      if (!name) name = line.replace(/^\+\+\+ (b\/)?/, '')
    }
  }
  if (!name) return ''
  const base = name.split('/').pop() || name
  return files > 1 ? `${base} (+${files - 1})` : base
})

// ApplyPatch 的行摘要:第一个文件名 + 数量(hapi 同款"file (+n)")。
const applyPatchBrief = computed(() => {
  const files = patchFiles.value
  if (!files) return ''
  const base = files[0].split('/').pop() || files[0]
  return files.length > 1 ? `${base} (+${files.length - 1})` : base
})

// 完整命令/参数值(弹窗里用):能解析出 command 等关键值就单取它,
// 解析不出再贴 args 原文。
const command = computed(() => brief.value || props.call.args || '')

// ---- 编辑类工具的 diff ----
interface DiffBlock { old: string; new: string }

// Edit: old_string → new_string;Write: 全新内容(old 为空);
// MultiEdit: edits 数组逐个展开,连续拼接(顺序即生效顺序)。
// ApplyPatch(codex filechange):changes 数组每项 {path, kind:{type}, diff}。
// diff 内容按改动类型拆:kind=add 是全新文件(空→diff 全文);其他形态的
// diff 文本本身就是改动内容,没有旧文参照时按"diff 显示、old 空"呈现。
const patchDiff = computed<DiffBlock[] | null>(() => {
  if (props.call.tool !== 'ApplyPatch') return null
  const args = parseArgs()
  if (!args || !Array.isArray(args.changes)) return null
  const blocks: DiffBlock[] = []
  for (const e of args.changes) {
    if (!e || typeof e !== 'object') continue
    const path = typeof (e as any).path === 'string' ? (e as any).path : ''
    const diff = typeof (e as any).diff === 'string' ? (e as any).diff : ''
    const kind = (e as any)?.kind?.type || ''
    if (!path || !diff) continue
    // add:diff 是文件全文;delete:全文即被删内容(放 old 侧);
    // update:codex 实测 diff 给的就是新内容文本,旧文不随通知来。
    if (kind === 'delete') blocks.push({ old: diff, new: '', path } as DiffBlock)
    else blocks.push({ old: '', new: diff, path } as DiffBlock)
  }
  return blocks.length ? blocks : null
})

// ApplyPatch 的文件清单(行摘要与兜底展示用)。
const patchFiles = computed<string[] | null>(() => {
  if (props.call.tool !== 'ApplyPatch') return null
  const args = parseArgs()
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
// 按 "diff --git" 边界分文件;单文件没头也兜得住。行级归类与 hapi
// CodexDiffView 同款:+→new,-→old,空格→两侧。
function parseUnifiedDiff(text: string): DiffBlock[] {
  const blocks: DiffBlock[] = []
  let oldLines: string[] = []
  let newLines: string[] = []
  let fileName = ''
  let inHunk = false
  const flush = () => {
    if (oldLines.length || newLines.length || fileName) {
      blocks.push({ old: oldLines.join('\n'), new: newLines.join('\n'), path: fileName } as DiffBlock)
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

const diffBlocks = computed<DiffBlock[] | null>(() => {
  const t = props.call.tool
  if (t === 'ApplyPatch') return patchDiff.value
  if (t === 'CodexDiff') {
    const args = parseArgs()
    if (args && typeof args.unified_diff === 'string') {
      const blocks = parseUnifiedDiff(args.unified_diff)
      return blocks.length ? blocks : null
    }
    return null
  }
  if (t !== 'Edit' && t !== 'MultiEdit' && t !== 'Write' && t !== 'NotebookEdit') return null
  const args = parseArgs()
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
  return [{ old: oldStr, new: newStr, path: filePath } as DiffBlock]
})
</script>

<template>
  <div class="tool-card" :class="call.state">
    <button type="button" class="tool-head" @click="open = true">
      <span class="tool-dot" />
      <span class="tool-name">{{ call.tool || '工具' }}</span>
      <span v-if="codexDiffBrief || applyPatchBrief || brief" class="tool-brief">{{ codexDiffBrief || applyPatchBrief || brief }}</span>
      <span class="tool-state">{{ stateLabel }}</span>
    </button>
    <!-- 内嵌审批:compact 省掉命令摘要行(头部本来就显示着);留在时间线上,弹窗打开也能答复 -->
    <ApprovalCard
      v-if="pendingReq"
      :req="pendingReq" :resolved="pendingResolved ?? null" :busy="approveBusy" compact
      class="tool-approval"
      @decide="(allow, session) => emit('decide', allow, session)"
    />
    <n-modal v-model:show="open" preset="card" :title="call.tool || '工具'" class="tool-modal">
      <div class="tool-detail">
        <template v-if="diffBlocks">
          <DiffView
            v-for="(b, i) in diffBlocks" :key="i"
            :old="b.old" :new="b.new"
            :file-path="(b as any).path || (i === 0 ? (codexDiffBrief || brief) : '')"
          />
        </template>
        <!-- ApplyPatch 解析不出 diff(形态未知)时兜底列文件 -->
        <div v-else-if="patchFiles" class="patch-files">
          <div v-for="(f, i) in patchFiles" :key="i" class="patch-file">{{ f }}</div>
        </div>
        <pre v-else-if="command" class="tool-block">{{ command }}</pre>
        <pre v-if="call.result" class="tool-block result">{{ call.result }}</pre>
        <div v-else-if="call.state === 'running'" class="tool-wait">等待结果…</div>
      </div>
    </n-modal>
  </div>
</template>

<style scoped>
.tool-card {
  border: 1px solid rgba(127, 127, 127, .18);
  border-radius: var(--lr-radius);
  background: var(--lr-bg-elevated);
  overflow: hidden;
}
/* 内嵌审批:去掉 ApprovalCard 自身的圆角边框,融进工具卡 */
.tool-approval { border: 0; border-top: 1px solid rgba(127, 127, 127, .14); border-radius: 0; }
.tool-card.error { border-color: rgba(220, 38, 38, .45); }
.tool-head {
  display: flex; align-items: center; gap: 8px;
  width: 100%; min-height: 38px; padding: 6px 10px;
  appearance: none; border: 0; background: transparent;
  font: inherit; font-size: 13px; color: var(--lr-fg);
  cursor: pointer; text-align: left;
  -webkit-tap-highlight-color: transparent;
}
.tool-dot {
  flex: none; width: 8px; height: 8px; border-radius: 50%;
  background: var(--lr-warn);
}
.tool-card.ok .tool-dot { background: var(--lr-ok); }
.tool-card.error .tool-dot { background: var(--lr-danger); }
.tool-card.running .tool-dot { animation: tool-pulse 1.2s ease-in-out infinite; }
@keyframes tool-pulse { 50% { opacity: .35; } }
.tool-name { flex: none; font-weight: 600; font-size: 12px; font-family: ui-monospace, monospace; }
.tool-brief {
  min-width: 0; flex: 1;
  /* 单行可左右滑动,不省略:滑动看命令尾,点行开弹窗看全量 */
  white-space: nowrap;
  overflow-x: auto; overflow-y: hidden;
  scrollbar-width: none;
  font-size: 12px; color: var(--lr-fg-muted);
  font-family: ui-monospace, monospace;
}
.tool-brief::-webkit-scrollbar { display: none; }
.tool-state { flex: none; font-size: 11px; color: var(--lr-fg-muted); }
.tool-card.error .tool-state { color: var(--lr-danger); }
</style>

<!-- 弹窗里的详情由 teleport 渲染到 body,scoped 样式作用不到,放非 scoped 块 -->
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
</style>
