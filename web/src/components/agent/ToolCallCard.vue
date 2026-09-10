<script setup lang="ts">
// 工具调用单行卡片:一行摘要(工具名 + 关键参数 + 状态),点击打开详情弹窗
// (AgentView 层的 ToolDetailModal,卡片只 emit —— 卡片会随回合实时重组,
// 弹窗挂卡片内部会跟着被卸载)。编辑类 diff / 参数原文 / 结果都在弹窗里;
// codex 改动卡(ApplyPatch/CodexDiff)的 diff 例外,直接铺在卡内。
// 审批请求内嵌在行下方,子 agent 过程(steps)嵌在卡内。
import { computed, ref, watch } from 'vue'
import { NModal } from 'naive-ui'
import { api, type AgentPermissionReq, type AgentToolCall } from '@/api/client'
import DiffView from './DiffView.vue'
import ApprovalCard from './ApprovalCard.vue'

const props = defineProps<{
  call: AgentToolCall
  // 子 agent 步骤(Task/Agent 卡的过程):嵌在本卡内部,不再另起一张卡 ——
  // 父卡与过程是一体的时间线单元。
  steps?: AgentToolCall[]
  // 会话 id:tool_result 的 image 块经 /api/agent/sessions/{id}/files/{name} 取。
  sessionId?: string
  // 内嵌审批:同工具的审批请求并进工具卡(Write/Edit 审批与调用同卡呈现)。
  pendingReq?: AgentPermissionReq
  pendingResolved?: { allow: boolean; session?: boolean } | null
  approveBusy?: boolean
}>()

// detail:打开全局详情弹窗(AgentView 层的 ToolDetailModal)。弹窗不挂在
// 卡片内部 —— 实时回合里卡片会在 单卡↔组卡 间重组,卡片一卸载弹窗就没了。
const emit = defineEmits<{
  (e: 'decide', allow: boolean, session: boolean): void
  (e: 'detail', call: AgentToolCall): void
}>()

// ---- 子 agent 步骤(嵌在本卡里,不另起卡) ----
// 列表展开态:父卡在跑默认展开(实时看子 agent 在做什么),完成默认收起;
// 用户手动点过就不再跟父卡状态走。
const stepsOpen = ref(props.call.state === 'running')
watch(() => props.call.state, (s) => {
  if (s === 'running') stepsOpen.value = true
})

// 子 agent 单条的目标摘要(Read 的 file_path、Bash 的 command…)。
function stepBrief(c: AgentToolCall): string {
  try {
    const args = c.args ? JSON.parse(c.args) : null
    if (args && typeof args === 'object') {
      for (const key of ['command', 'file_path', 'path', 'pattern', 'url', 'query', 'description']) {
        const v = (args as Record<string, unknown>)[key]
        if (typeof v === 'string' && v) return v
      }
    }
  } catch { /* 截断的 args:留空 */ }
  return ''
}

// tool_result 里 image 块的缩略图地址(有会话 id 且带图才有)。
const imageUrls = computed<string[]>(() =>
  props.sessionId && props.call.images?.length
    ? props.call.images.map((n) => api.agentFileUrl(props.sessionId!, n))
    : [],
)
const lightbox = ref<string | null>(null)
// n-modal 要 v-model:show;lightbox 值本身只是 url。
const lightboxOpen = computed({
  get: () => !!lightbox.value,
  set: (v: boolean) => { if (!v) lightbox.value = null },
})

const stateLabel = computed(() => {
  // 拒绝的调用不会真跑:结论直接就是"已拒绝"(替代旧的"出错"),
  // 不再在卡下另挂一张"已拒绝"审批条。
  if (props.pendingResolved && !props.pendingResolved.allow) return '已拒绝'
  return props.call.state === 'running' ? '运行中' : props.call.state === 'error' ? '出错' : '完成'
})

function parseArgs(): Record<string, unknown> | null {
  try {
    const v = props.call.args ? JSON.parse(props.call.args) : null
    return v && typeof v === 'object' ? (v as Record<string, unknown>) : null
  } catch { /* args 已截断成非法 JSON 时整段进详情 */ }
  return null
}

// 截断兜底:后端信封形态 {"truncated":true,"raw":"…前缀…"} 的参数虽 parse 不回
// 结构,diff/摘要都取不到字段,但 raw 前缀是合法 JSON 的开头 —— 再 parse 一次,
// 能解出对象就把已到手的部分当参数用(仅丢了被截字段的后半,前面字段仍可渲染)。
function parseArgsLenient(): Record<string, unknown> | null {
  const strict = parseArgs()
  if (strict) return strict
  const raw = props.call.args
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

// 摘要行给一眼能认出的关键参数:命令(Bash 的 command)、路径、文件名,
// 多智能体工具(spawn_agent 的 task_name、send_message 的 message 等),
// 子 agent(Agent/Task 的 description、TaskOutput 的 task_id)。
const brief = computed(() => {
  const args = parseArgs()
  if (!args) return ''
  for (const key of ['command', 'file_path', 'path', 'pattern', 'url', 'query', 'task_name', 'description', 'task_id', 'target', 'message']) {
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

// ---- 编辑类工具的 diff ----
interface DiffBlock { old: string; new: string; path?: string }

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
    const args = parseArgsLenient()
    if (args && typeof args.unified_diff === 'string') {
      const blocks = parseUnifiedDiff(args.unified_diff)
      return blocks.length ? blocks : null
    }
    return null
  }
  if (t !== 'Edit' && t !== 'MultiEdit' && t !== 'Write' && t !== 'NotebookEdit') return null
  const args = parseArgsLenient()
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

// codex 的改动卡(ApplyPatch/CodexDiff)结果本身是空的,把 diff 直接铺在
// 卡片里(hapi CodexDiffView 同款观感)——只靠一行摘要 + 点弹窗,远端
// 看起来就像"空的工具执行"。
const inlineDiff = computed(() =>
  diffBlocks.value && (props.call.tool === 'ApplyPatch' || props.call.tool === 'CodexDiff'),
)
</script>

<template>
  <div class="tool-card" :class="call.state">
    <button type="button" class="tool-head" @click="emit('detail', call)">
      <span class="tool-dot" />
      <span class="tool-name">{{ call.tool || '工具' }}</span>
      <span v-if="codexDiffBrief || applyPatchBrief || brief" class="tool-brief">{{ codexDiffBrief || applyPatchBrief || brief }}</span>
      <span class="tool-state">{{ stateLabel }}</span>
    </button>
    <!-- 子 agent 过程:嵌在父卡内的一体区块(不另起卡)。头部一行开关,
         单条可点看详情;每条上下两行(工具名一行、摘要换行下一行) -->
    <div v-if="steps?.length" class="steps">
      <button type="button" class="steps-toggle" @click="stepsOpen = !stepsOpen">
        <span class="steps-caret" :class="{ open: stepsOpen }" />过程({{ steps.length }})
      </button>
      <div v-if="stepsOpen" class="steps-body">
        <button
          v-for="(c, i) in steps" :key="c.toolUseId || i" type="button"
          class="steps-item" :class="c.state" @click="emit('detail', c)"
        >
          <div class="steps-line">
            <span class="steps-dot" />
            <span class="steps-name">{{ c.tool || '工具' }}</span>
          </div>
          <div v-if="stepBrief(c)" class="steps-brief">{{ stepBrief(c) }}</div>
        </button>
      </div>
    </div>
    <!-- codex 改动卡:diff 直接铺在卡片里,不用点开就能看见改了什么 -->
    <div v-if="inlineDiff" class="tool-inline-diff">
      <DiffView
        v-for="(b, i) in diffBlocks" :key="i"
        :old="b.old" :new="b.new" :file-path="(b as any).path || codexDiffBrief || applyPatchBrief || ''"
      />
    </div>
    <!-- 结果里的图片:缩略图条(点击放大),弹窗里也有全量 -->
    <div v-if="imageUrls.length" class="tool-imgs">
      <button v-for="(u, i) in imageUrls" :key="u" type="button" class="tool-img" @click.stop="lightbox = u">
        <img :src="u" :alt="`图片 ${i + 1}`" loading="lazy" />
      </button>
    </div>
    <!-- 内嵌审批:compact 省掉命令摘要行(头部本来就显示着);留在时间线上,弹窗打开也能答复。
         已答复的不再渲染 —— 结论并进卡头的状态词(允许→完成,拒绝→出错),一卡一行一个结论 -->
    <ApprovalCard
      v-if="pendingReq && !pendingResolved"
      :req="pendingReq" :busy="approveBusy" compact
      class="tool-approval"
      @decide="(allow, session) => emit('decide', allow, session)"
    />
    <!-- 点亮的原图:独立小弹窗,Cookie 认同所以 <img src> 直连。
         详情弹窗在 AgentView 层(ToolDetailModal),卡片只 emit。 -->
    <n-modal v-model:show="lightboxOpen" preset="card" class="tool-modal" title="图片">
      <img v-if="lightbox" :src="lightbox" class="tool-lightbox" alt="图片" />
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

/* ---- 子 agent 过程(嵌在父卡内的区块) ---- */
.steps { border-top: 1px solid rgba(127, 127, 127, .14); }
.steps-toggle {
  display: flex; align-items: center; gap: 6px;
  width: 100%; min-height: 32px; padding: 4px 10px;
  appearance: none; border: 0; background: transparent;
  font: inherit; font-size: 12px; color: var(--lr-fg-muted);
  cursor: pointer; text-align: left;
  -webkit-tap-highlight-color: transparent;
}
.steps-caret {
  flex: none; width: 1em; text-align: center;
  opacity: .7; transition: transform .15s ease;
  /* 收起 ">";展开旋转 90° 朝下 */
}
.steps-toggle .steps-caret::before { content: '›'; font-weight: 600; }
.steps-caret.open { transform: rotate(90deg); }
.steps-body { padding: 0 0 4px; }
/* 每条可点(弹窗看该次参数与结果);上下两行,不左右挤 */
.steps-item {
  display: flex; flex-direction: column; align-items: flex-start; gap: 1px;
  width: 100%; padding: 5px 10px 5px 22px;
  appearance: none; border: 0; background: transparent;
  font: inherit; color: inherit; text-align: left; cursor: pointer;
  -webkit-tap-highlight-color: transparent;
  min-height: 38px;
}
.steps-item:hover { background: rgba(127, 127, 127, .06); }
.steps-line { display: flex; align-items: center; gap: 6px; align-self: stretch; }
.steps-dot { flex: none; width: 6px; height: 6px; border-radius: 50%; background: var(--lr-ok); }
.steps-item.error .steps-dot { background: var(--lr-danger); }
.steps-item.running .steps-dot { background: var(--lr-warn); animation: tool-pulse 1.2s ease-in-out infinite; }
.steps-name { flex: 1; min-width: 0; font-family: ui-monospace, monospace; font-size: 11px; color: var(--lr-fg); }
.steps-brief {
  align-self: stretch;
  white-space: normal; overflow-wrap: anywhere;
  font-family: ui-monospace, monospace; font-size: 11px; color: var(--lr-fg-muted);
}
/* 内铺 diff:多块之间只留 1px 分隔;正文高度收紧(时间线上只是扫一眼,
   完整内容仍在详情弹窗),超出内部滚动,不把手机时间线滚穿 */
.tool-inline-diff { border-top: 1px solid rgba(127, 127, 127, .14); }
.tool-inline-diff .diff-view + .diff-view { border-top: 1px solid rgba(127, 127, 127, .14); }
.tool-inline-diff :deep(.diff-body) { max-height: 180px; }
/* 结果缩略图条:小方图,横向可滚;点击在时间线上直接放大,不必先进详情 */
.tool-imgs {
  display: flex; gap: 6px; padding: 6px 10px 8px;
  overflow-x: auto; scrollbar-width: none;
}
.tool-imgs::-webkit-scrollbar { display: none; }
.tool-img {
  flex: none; width: 56px; height: 56px; padding: 0;
  border: 1px solid rgba(127, 127, 127, .2); border-radius: 6px;
  background: transparent; cursor: zoom-in; overflow: hidden;
}
.tool-img img {
  width: 100%; height: 100%; object-fit: cover; display: block;
}
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
.tool-modal .tool-imgs { display: flex; gap: 8px; flex-wrap: wrap; }
.tool-modal .tool-imgs img {
  width: 96px; height: 96px; border-radius: 6px; cursor: zoom-in;
  border: 1px solid rgba(127, 127, 127, .2); object-fit: cover;
}
.tool-modal .tool-lightbox { max-width: 100%; max-height: 70vh; display: block; margin: 0 auto; }
</style>
