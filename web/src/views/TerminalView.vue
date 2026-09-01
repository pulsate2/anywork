<script setup lang="ts">
// 终端视图:多窗口 PTY + 服务端滚动缓冲 + 移动键盘层。
import { ref, onMounted, onUnmounted, nextTick, watch } from 'vue'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import {
  NButton, NIcon, NModal, NList, NListItem,
  NTag, NEmpty, useMessage, NSelect, NForm, NFormItem, NInput,
} from 'naive-ui'
import { TerminalOutline, AddOutline, PlayOutline, CopyOutline, ClipboardOutline } from '@vicons/ionicons5'
import { TermClient, type TermSummary } from '@/api/term'
import { type Workspace } from '@/api/client'
import { useWorkspaceStore } from '@/stores/workspace'
import TerminalKeyboard from '@/mobile/TerminalKeyboard.vue'

const message = useMessage()
const store = useWorkspaceStore()
const termEl = ref<HTMLDivElement>()
const terminalWrap = ref<HTMLDivElement>()
const kbd = ref<InstanceType<typeof TerminalKeyboard> | null>(null)
let term: Terminal | null = null
let fitAddon: FitAddon | null = null
const client = new TermClient(handleEvent)

const sessions = ref<TermSummary[]>([])
const activeId = ref<string | null>(null)
const showSessionList = ref(false)
const showNewModal = ref(false)
const workspaces = ref<Workspace[]>([])
const selectedWs = ref<string | null>(null)
const execShell = ref('')

// 终端尝试 resize 用的信号。
const fitSignal = ref(0)

// 进入页面后自动附加到最新会话。只认首次列表响应:之后的 list()(新建/结束/退出触发)
// 不能再自动跳转,否则用户结束当前会话就会被塞进另一个会话。
let autoAttachDone = false

function handleEvent(e: any) {
  switch (e.type) {
    case 'output':
      term?.write(e.data)
      break
    case 'session':
      // create/attach 都会回这一帧:切掉空状态遮罩,并把真实尺寸同步给 PTY。
      activeId.value = e.session.id
      nextTick(() => {
        attemptFit()
        if (term) client.resize(term.cols, term.rows)
        term?.focus()
      })
      client.list()
      break
    case 'sessionList':
      sessions.value = e.list
      if (!autoAttachDone && !activeId.value) {
        autoAttachDone = true
        // 服务端已按创建时间倒序,第一个活着的就是最新会话。
        const latest = (e.list as TermSummary[]).find((s) => !s.dead)
        if (latest) attachSession(latest)
      }
      break
    case 'exit':
      message.info(`会话已退出 (退出码 ${e.exitCode})`)
      // 服务端紧跟着会广播新的 sessionList,这里不用再 list()。
      if (e.id === activeId.value) releaseActive()
      break
    case 'error':
      message.error(e.message)
      break
    case 'close':
      break
  }
}

function ensureTerm() {
  if (term) return
  term = new Terminal({
    cursorBlink: true,
    fontSize: 14,
    fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
    scrollback: 2000,
    convertEol: true,
    allowProposedApi: true,
    theme: {
      background: 'transparent',
    },
  })
  fitAddon = new FitAddon()
  term.loadAddon(fitAddon)
  term.open(termEl.value!)
  // 软键盘/物理键盘的输入先过一遍键盘条的粘滞修饰符(Ctrl/Alt),再发给 PTY。
  term.onData((d) => client.input(kbd.value?.applySticky(d) ?? d))
  // 物理键盘 resize 也在 resize observer 里处理。
  term.onResize(({ cols, rows }) => client.resize(cols, rows))
  applyTheme()
}

function applyTheme() {
  // 跟随当前实际主题(浅/深)。NAI 主题由 CSS 类控制,这里用媒体查询判断。
  const dark = window.matchMedia('(prefers-color-scheme: dark)').matches
  term?.options.theme && Object.assign(term.options.theme, {
    foreground: dark ? '#e5e7eb' : '#1c1f24',
    background: 'transparent',
    cursor: dark ? '#e5e7eb' : '#1c1f24',
    selectionBackground: 'rgba(37,99,235,.3)',
  })
  term?.refresh?.(0, term.rows * 1000)
}

let resizeObserver: ResizeObserver | null = null
function setUpResize() {
  if (!terminalWrap.value) return
  resizeObserver = new ResizeObserver(() => {
    attemptFit()
  })
  resizeObserver.observe(terminalWrap.value)
}

function attemptFit() {
  if (!fitAddon || !term) return
  try {
    fitAddon.fit()
  } catch {
    // 元素尚未布局,忽略。
  }
}

async function connect() {
  try {
    await client.connect()
    client.list()
  } catch (e: any) {
    message.error(e.message || '连接失败')
  }
}

function pickWorkspaceAndCreate() {
  selectedWs.value = store.currentId
  showNewModal.value = true
}

function createSession() {
  // 没有工作区也要能开终端:退回 root 目录。
  const ws = workspaces.value.find((w) => w.id === selectedWs.value)
  const dir = ws?.path ?? store.root
  if (ws) store.select(ws.id)
  client.createSession(dir, execShell.value, term?.cols || 80, term?.rows || 24)
  showNewModal.value = false
  execShell.value = ''
}

function attachSession(sum: TermSummary) {
  if (sum.dead) {
    message.warning('该会话已结束,请新建')
    return
  }
  // 切换前整屏重置,避免上一个会话的内容残留(clear() 会留下当前提示行)。
  term?.reset()
  activeId.value = sum.id
  client.attach(sum.id)
  showSessionList.value = false
}

function killSession(id: string) {
  client.kill(id)
  // 结束的是当前会话:立刻脱离。否则 activeId 还指着已死的 PTY,
  // 输入照发但不会有任何回显,就是"结束之后终端无法操作"。
  if (id === activeId.value) releaseActive()
  // 进程退出是异步的,服务端会在真正退出后广播 exit + 最新列表。
}

// releaseActive 解除与当前会话的绑定,回到"选择或新建一个会话"空状态。
// 用 reset() 而不是 clear():clear() 会保留当前提示行,它会留在空状态遮罩后面。
function releaseActive() {
  client.detach()
  activeId.value = null
  term?.reset()
}

// 会话列表渲染。
function sessionShortName(s: TermSummary): string {
  // 取工作区名(路径最后一段)+ 时间。Windows 下 dir 是反斜杠形式,两种分隔符都要切。
  const base = s.dir.split(/[/\\]/).filter(Boolean).pop() || s.dir
  const t = new Date(s.createdAt)
  const hh = String(t.getHours()).padStart(2, '0')
  const mm = String(t.getMinutes()).padStart(2, '0')
  return `${base} · ${hh}:${mm}`
}

// 粘贴按钮:手机剪贴板 → 终端。
async function pasteIntoTerm() {
  try {
    const text = await navigator.clipboard.readText()
    if (text) client.input(text)
    else message.info('剪贴板为空')
  } catch {
    message.warning('无法读取剪贴板(需用户手势且在安全上下文)')
  }
}

// 拷贝当前会话选择内容。
function copySelection() {
  const sel = term?.getSelection()
  if (!sel) {
    message.info('没有选中内容')
    return
  }
  navigator.clipboard.writeText(sel).then(
    () => message.success('已复制'),
    () => message.warning('复制失败')
  )
}

async function loadWorkspaces() {
  try {
    await store.ensure()
    workspaces.value = store.list
    selectedWs.value = store.currentId
  } catch {
    workspaces.value = []
  }
}

onMounted(async () => {
  ensureTerm()
  await nextTick()
  attemptFit()
  setUpResize()
  await connect()
  loadWorkspaces()
})

onUnmounted(() => {
  resizeObserver?.disconnect()
  client.close()
  term?.dispose()
})

// 主题切换时重刷配色。
watch(fitSignal, () => applyTheme())
</script>

<template>
  <div class="page-content terminal-page">
    <!-- 顶部操作栏 -->
    <div class="term-toolbar">
      <n-button size="small" quaternary @click="showSessionList = true">
        <template #icon><n-icon :component="TerminalOutline" /></template>
        会话
      </n-button>
      <div class="spacer"></div>
      <n-button size="small" quaternary @click="copySelection">
        <template #icon><n-icon :component="CopyOutline" /></template>
      </n-button>
      <n-button size="small" quaternary @click="pasteIntoTerm">
        <template #icon><n-icon :component="ClipboardOutline" /></template>
        粘贴
      </n-button>
      <n-button size="small" type="primary" @click="pickWorkspaceAndCreate">
        <template #icon><n-icon :component="AddOutline" /></template>
        新建
      </n-button>
    </div>

    <!-- xterm 容器 -->
    <div ref="terminalWrap" class="term-wrap" :class="{ focused: activeId }">
      <div ref="termEl" class="term-el"></div>
      <n-empty v-if="!activeId" description="选择或新建一个会话开始" class="term-empty" />
    </div>

    <!-- 移动键盘层 -->
    <TerminalKeyboard ref="kbd" :on-key="(k: string) => client.input(k)" />

    <!-- 会话列表抽屉 -->
    <n-modal v-model:show="showSessionList" preset="card" title="终端会话" style="width: 92%; max-width: 420px">
      <n-list v-if="sessions.length">
        <n-list-item v-for="s in sessions" :key="s.id" class="sess-item">
          <div class="sess-main" @click="attachSession(s)">
            <div class="sess-name">
              {{ sessionShortName(s) }}
              <n-tag v-if="s.dead" size="small" type="error" :bordered="false">结束</n-tag>
              <n-tag v-else size="small" type="success" :bordered="false">运行中</n-tag>
            </div>
            <div class="sess-dir">{{ s.dir }}</div>
          </div>
          <template #suffix>
            <n-button size="tiny" quaternary type="error" @click.stop="killSession(s.id)">结束</n-button>
          </template>
        </n-list-item>
      </n-list>
      <n-empty v-else description="暂无会话" style="padding: 24px 0" />
    </n-modal>

    <!-- 新建会话 -->
    <n-modal v-model:show="showNewModal" preset="card" title="新建终端会话" style="width: 92%; max-width: 420px">
      <n-form label-placement="top">
        <n-form-item label="工作区目录">
          <n-select
            v-model:value="selectedWs"
            :options="workspaces.map(w => ({ label: `${w.name} (${w.path})`, value: w.id }))"
            :placeholder="`不选则用根目录 ${store.root}`"
            clearable
          />
        </n-form-item>
        <n-form-item label="Shell(可选)">
          <n-input v-model:value="execShell" placeholder="Windows 默认 powershell,Unix 默认 $SHELL" />
        </n-form-item>
      </n-form>
      <template #footer>
        <div class="modal-footer">
          <n-button @click="showNewModal = false">取消</n-button>
          <n-button type="primary" @click="createSession">
            <template #icon><n-icon :component="PlayOutline" /></template>
            启动
          </n-button>
        </div>
      </template>
    </n-modal>
  </div>
</template>

<style scoped>
.terminal-page {
  display: flex;
  flex-direction: column;
  height: 100vh;
  /* dvh 跟随移动端地址栏收缩,100vh 会把键盘条顶到屏幕外 */
  height: 100dvh;
  /* 自己接管留白:覆盖 .page-content 的 72px 底部内边距,只给固定导航留位置,
     键盘条因此紧贴导航上沿,省下的高度全部给终端。 */
  padding: 8px 12px calc(56px + env(safe-area-inset-bottom));
  overflow: hidden;
}
.term-toolbar {
  flex: none;
  display: flex;
  align-items: center;
  gap: 6px;
  margin-bottom: 6px;
}
.spacer { flex: 1; }
.term-wrap {
  position: relative;
  flex: 1;
  /* min-height 不能设死值,否则小屏上会把键盘条挤出可视区 */
  min-height: 0;
  border-radius: var(--lr-radius);
  border: 1px solid rgba(127,127,127,.2);
  overflow: hidden;
  padding: 4px;
  background: var(--lr-bg);
}
@media (min-width: 768px) {
  /* 桌面端导航在左侧(见 main.css),底部不用留位置 */
  .terminal-page {
    padding-bottom: 8px;
    padding-left: calc(var(--lr-sidebar) + 12px);
  }
}
.term-el { width: 100%; height: 100%; }
.term-empty { position: absolute; inset: 0; margin: auto; }
.sess-main { flex: 1; min-width: 0; cursor: pointer; }
/* n-list-item 的 __main 是 flex: 1 但 min-width 仍是 auto,不清零长路径会把 suffix 里的
   结束按钮挤出可视区。 */
.sess-item :deep(.n-list-item__main) { min-width: 0; }
.sess-item :deep(.n-list-item__suffix) { margin-left: 12px; }
.sess-name { display: flex; align-items: center; gap: 6px; font-weight: 600; }
.sess-dir {
  color: var(--lr-fg-muted); font-size: 12px;
  font-family: ui-monospace, monospace;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.modal-footer { display: flex; justify-content: flex-end; gap: 8px; }
</style>