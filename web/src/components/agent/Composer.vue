<script setup lang="ts">
// 输入区两行(hapi 同款):第一行 textarea 自增高(1~5 行),第二行操作条。
// Enter 只换行(手机软键盘直接给换行,不会误发),Ctrl/Cmd+Enter 发送。
// 停止与发送是两个独立按钮,回合进行中都可点:停止打断,发送进排队。
// 另外两个入口:"/" 指令弹层(模型/思考强度/权限/打断)、附件(传到会话
// 专属目录后以 @路径 提及)。
import { computed, nextTick, ref, watch } from 'vue'
import { NButton, NIcon } from 'naive-ui'
import { AttachOutline, SendOutline, StopOutline } from '@vicons/ionicons5'
import { api } from '@/api/client'

const props = defineProps<{
  running: boolean
  disabled?: boolean // 会话不在运行且无可恢复凭据
  // 有未答的提问卡:回合卡在控制请求上,这时发消息只会排队、还容易被当成
  // 回答。锁输入与发送,但保留停止(想直接打断回合是合法诉求)。
  askPending?: boolean
  sending?: boolean
  sessionId?: string // 附件归属的会话(存独立目录,不进工作区)
  cliCommands?: string[] // CLI 真实 / 指令(claude init 透传 / codex 内置表)
}>()

const emit = defineEmits<{
  (e: 'send', text: string): void
  (e: 'interrupt'): void
  (e: 'command', name: string, arg: string): void
}>()

const text = ref('')
const inputEl = ref<HTMLTextAreaElement | null>(null)
const fileEl = ref<HTMLInputElement | null>(null)
const uploading = ref(false)

// 提问未答 = 输入整体锁死(与 disabled 同效但提示语不同)。
const locked = computed(() => !!props.askPending)

const canSend = computed(() => !props.disabled && !locked.value && !props.sending && text.value.trim() !== '')

// ---- "/" 指令 ----
// 本地指令(管理会话自身):CLI 不认识这些,是我们自己的控制通道。
const localCommands = [
  { name: '/model', args: '<模型名>', desc: '设置模型', hasArg: true, local: true },
  { name: '/think', args: 'none|low|medium|high', desc: '思考强度', hasArg: true, local: true },
  { name: '/perm', args: 'plan|ask|edits|accept', desc: '权限模式', hasArg: true, local: true },
  { name: '/interrupt', args: '', desc: '打断当前回合', hasArg: false, local: true },
]
// CLI 真实指令(claude init 的 slash_commands 透传 / codex 内置表):
// 原样作为消息发给 CLI 自己处理,不截获。
const commands = computed(() => [
  ...localCommands,
  ...(props.cliCommands || []).map((n) => ({
    name: `/${n}`, args: '', desc: '', hasArg: false, local: false,
  })),
])
const slashOpen = computed(() => text.value.startsWith('/') && !props.disabled && !locked.value)
const slashMatches = computed(() => {
  if (!slashOpen.value) return []
  const word = text.value.split(/\s/)[0]!.toLowerCase()
  return commands.value.filter((c) => c.name.startsWith(word))
})

function pickCommand(name: string) {
  const c = commands.value.find((x) => x.name === name)
  text.value = c?.hasArg ? `${name} ` : name
  nextTick(() => inputEl.value?.focus())
}

function autosize() {
  const el = inputEl.value
  if (!el) return
  el.style.height = 'auto'
  el.style.height = Math.min(el.scrollHeight, 132) + 'px'
}
watch(text, () => nextTick(autosize))

function submit() {
  const t = text.value.trim()
  if (!t || props.disabled || locked.value) return
  if (t.startsWith('/')) {
    const [word, ...rest] = t.split(/\s+/)
    const c = commands.value.find((x) => x.name === word!.toLowerCase())
    // 本地管理指令走指令通道(缺参数则继续等输入);CLI 指令与不认识的
    // 都当普通文本发给 CLI 自己处理。
    if (c?.local) {
      if (c.hasArg && !rest[0]) return
      emit('command', c.name.slice(1), rest.join(' '))
      text.value = ''
      return
    }
  }
  emit('send', t)
  text.value = ''
}

function onKeydown(ev: KeyboardEvent) {
  // 发送只认 Ctrl/Cmd+Enter;裸 Enter 留给换行(手机软键盘的"换行"键)。
  // isComposing 的输入过程(中文候选)不算。
  if (ev.key === 'Enter' && (ev.ctrlKey || ev.metaKey) && !ev.isComposing) {
    ev.preventDefault()
    submit()
  }
}

// ---- 附件:传到会话专属的独立目录(不进工作区),再以 @绝对路径 提及
// (agent 自己会去读;读工作区外的路径不受沙箱限制) ----
async function onFiles(files: FileList | null) {
  if (!files?.length || !props.sessionId) return
  uploading.value = true
  const mention: string[] = []
  for (const f of Array.from(files)) {
    try {
      const res = await api.agentUpload(props.sessionId, f)
      mention.push(`@${res.path}`)
    } catch {
      // 单个失败不拦其余,留一行痕迹在输入框里。
      mention.push(`(上传失败: ${f.name})`)
    }
  }
  if (mention.length) {
    text.value = (text.value ? text.value + '\n' : '') + mention.join(' ')
    nextTick(() => inputEl.value?.focus())
  }
  uploading.value = false
  if (fileEl.value) fileEl.value.value = '' // 同名文件可重复选
}
</script>

<template>
  <div class="composer">
    <div v-if="slashOpen && slashMatches.length" class="slash">
      <button
        v-for="c in slashMatches" :key="c.name" type="button" class="slash-item"
        @mousedown.prevent="pickCommand(c.name)"
      >
        <span class="slash-name">{{ c.name }}</span>
        <span v-if="c.args" class="slash-args">{{ c.args }}</span>
        <span v-if="c.desc" class="slash-desc">{{ c.desc }}</span>
        <span v-else class="slash-desc">CLI 指令</span>
      </button>
    </div>
    <!-- 自绘 textarea 而不用 n-input:输入区要贴着软键盘自动增高,naive 的 autosize
       在 fixed 布局里重算时机不稳。样式对齐 n-input 的观感。 -->
    <textarea
      ref="inputEl" v-model="text" rows="2" :disabled="disabled || locked"
      class="composer-input"
      :placeholder="uploading ? '上传附件中…'
        : locked ? 'Agent 正在等你的回答,请先回答上方提问…'
        : '发消息给 Agent(回车换行,Ctrl+Enter 发送,/ 查看指令)…'"
      @keydown="onKeydown"
    />
    <div class="composer-row">
      <button
        type="button" class="attach-btn" title="上传附件"
        :disabled="disabled || locked || uploading || !sessionId" @click="fileEl?.click()"
      >
        <n-icon :component="AttachOutline" />
      </button>
      <input ref="fileEl" type="file" multiple hidden @change="onFiles(($event.target as HTMLInputElement).files)" />
      <div class="composer-hint">{{ uploading ? '附件上传中…' : 'Ctrl+Enter 发送' }}</div>
      <n-button
        v-if="running" class="composer-btn" type="warning" secondary
        title="打断当前回合" @click="emit('interrupt')"
      >
        <template #icon><n-icon :component="StopOutline" /></template>
        停止
      </n-button>
      <n-button
        class="composer-btn" type="primary"
        :disabled="!canSend" :loading="sending" title="发送" @click="submit()"
      >
        <template #icon><n-icon :component="SendOutline" /></template>
        发送
      </n-button>
    </div>
  </div>
</template>

<style scoped>
.composer { position: relative; display: flex; flex-direction: column; gap: 6px; padding: 8px 0 2px; }
.composer-input {
  width: 100%;
  min-height: 58px; max-height: 132px;
  padding: 10px 12px;
  border: 1px solid rgba(127, 127, 127, .3);
  border-radius: var(--lr-radius);
  background: var(--lr-bg-elevated);
  color: var(--lr-fg);
  font: inherit; font-size: 15px; line-height: 1.5;
  resize: none; outline: none;
  overflow-y: auto;
}
.composer-input:focus { border-color: var(--lr-accent); }
.composer-input:disabled { opacity: .55; }
.composer-row { display: flex; align-items: center; gap: 8px; }
.composer-hint { flex: 1; min-width: 0; font-size: 11px; color: var(--lr-fg-muted); }
.composer-btn { flex: none; min-width: var(--lr-touch); }
.attach-btn {
  flex: none;
  width: var(--lr-touch); height: 36px;
  display: flex; align-items: center; justify-content: center;
  border: 1px solid rgba(127, 127, 127, .3);
  border-radius: var(--lr-radius);
  background: var(--lr-bg-elevated); color: var(--lr-fg-muted);
  cursor: pointer; font-size: 18px;
  -webkit-tap-highlight-color: transparent;
}
.attach-btn:disabled { opacity: .5; }

/* "/" 指令候选:浮在输入框上方(mousedown 防止点选时失焦丢候选) */
.slash {
  position: absolute; bottom: 100%; left: 0; right: 0;
  margin-bottom: 4px;
  background: var(--lr-bg-elevated);
  border: 1px solid rgba(127, 127, 127, .25);
  border-radius: var(--lr-radius);
  box-shadow: 0 4px 16px rgba(0, 0, 0, .18);
  overflow: hidden;
  max-height: 40dvh; overflow-y: auto;
  z-index: 5;
}
.slash-item {
  display: flex; align-items: baseline; gap: 8px;
  width: 100%; min-height: 38px; padding: 6px 12px;
  appearance: none; border: 0; background: transparent;
  font: inherit; text-align: left; cursor: pointer;
  color: var(--lr-fg);
  -webkit-tap-highlight-color: transparent;
}
.slash-item:hover { background: rgba(127, 127, 127, .1); }
.slash-name { font-family: ui-monospace, monospace; font-weight: 600; font-size: 13px; }
.slash-args { font-family: ui-monospace, monospace; font-size: 11px; color: var(--lr-fg-muted); }
.slash-desc { margin-left: auto; font-size: 12px; color: var(--lr-fg-muted); }
</style>
