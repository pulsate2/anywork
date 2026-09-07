<script setup lang="ts">
// Agent 会话远控:会话列表 + 聊天时间线(DESIGN-AGENT.md)。普通聊天风格:
// 消息气泡、工具调用折叠卡、审批卡;REST 操作 + WS 推送(断线 afterSeq 补差)。
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import {
  NButton, NEmpty, NIcon, NInputNumber, NModal, NPopconfirm, NSelect, NSpin, NSwitch, useMessage,
} from 'naive-ui'
import { AddOutline, CheckboxOutline, HourglassOutline, StopOutline, TrashOutline } from '@vicons/ionicons5'
import { api, type AgentAskUser, type AgentAskUserResult, type AgentCompaction, type AgentEvent, type AgentPermissionReq, type AgentSession, type AgentSystemInfo, type AgentToolCall, type AgentUsage } from '@/api/client'
import { AgentWS } from '@/api/agent'
import { renderMarkdown } from '@/utils/markdown'
import { useImmersive } from '@/utils/immersive'
import { useWorkspaceStore } from '@/stores/workspace'
import ToolCallCard from '@/components/agent/ToolCallCard.vue'
import ToolGroupCard from '@/components/agent/ToolGroupCard.vue'
import ApprovalCard from '@/components/agent/ApprovalCard.vue'
import AskUserCard from '@/components/agent/AskUserCard.vue'
import Composer from '@/components/agent/Composer.vue'
import StatusBar from '@/components/agent/StatusBar.vue'

const message = useMessage()
const wsStore = useWorkspaceStore()
// 聊天打开时进沉浸模式:底部导航滑走(BottomNav 消费),返回列表/离开页面即恢复。
const immersive = useImmersive()

// ---- 会话列表 ----
const sessions = ref<AgentSession[]>([])
const loading = ref(false)
const selectedId = ref<string | null>(null)
const selected = computed(() => sessions.value.find((s) => s.id === selectedId.value) ?? null)
// 聊天打开 → 沉浸模式(导航滑走);返回列表即退出。视图卸载时在 onBeforeUnmount 兜底复位。
watch(selectedId, (v) => { immersive.value = !!v })

async function loadSessions() {
  loading.value = true
  try {
    sessions.value = await api.agentSessions()
  } catch (e: any) {
    message.error(e?.message || '加载会话失败')
  } finally {
    loading.value = false
  }
}

// 按工作区分组:组内按最近活动(updated_at)倒序,组按组内最新活动排 ——
// 最近用过的目录自然在最上面。
const sessionGroups = computed(() => {
  const groups = new Map<string, AgentSession[]>()
  const sorted = [...sessions.value].sort((a, b) => b.updatedAt.localeCompare(a.updatedAt))
  for (const s of sorted) {
    const arr = groups.get(s.workspace)
    if (arr) arr.push(s)
    else groups.set(s.workspace, [s])
  }
  return [...groups.entries()].map(([workspace, list]) => ({ workspace, list }))
})

// 折叠状态:用户手动折叠/展开过的组记进 localStorage;没动过的组按默认规则
// 处理 —— 组里一个活着的会话都没有就收起,有在跑的就展开。
const savedCollapsed = ref(new Set<string>())
const savedOpen = ref(new Set<string>())
try {
  const saved = JSON.parse(localStorage.getItem('agent.collapsed') || '{}')
  if (saved && Array.isArray(saved.collapsed)) savedCollapsed.value = new Set(saved.collapsed)
  if (saved && Array.isArray(saved.open)) savedOpen.value = new Set(saved.open)
} catch { /* 坏数据当没存过 */ }

interface SessGroup { workspace: string; list: AgentSession[] }

function isCollapsed(g: SessGroup): boolean {
  if (savedCollapsed.value.has(g.workspace)) return true
  if (savedOpen.value.has(g.workspace)) return false
  return !g.list.some((s) => s.status !== 'dead')
}

function toggleGroup(g: SessGroup) {
  if (isCollapsed(g)) {
    savedCollapsed.value.delete(g.workspace)
    savedOpen.value.add(g.workspace)
  } else {
    savedCollapsed.value.add(g.workspace)
    savedOpen.value.delete(g.workspace)
  }
  // Set 内部增删不触发响应式,整体换新引用。
  savedCollapsed.value = new Set(savedCollapsed.value)
  savedOpen.value = new Set(savedOpen.value)
  try {
    localStorage.setItem('agent.collapsed', JSON.stringify({
      collapsed: [...savedCollapsed.value],
      open: [...savedOpen.value],
    }))
  } catch { /* 私密模式等 */ }
}

// ---- 消息与卡片 ----
// messages 只存当前选中会话的原始事件;卡片(气泡/工具卡/审批卡)由此派生,
// tool_use 与 tool_result 按 toolUseId 合并,permission_result 回填审批卡。
const messages = ref<AgentEvent[]>([])
const maxSeq = computed(() => messages.value.length ? messages.value[messages.value.length - 1].seq : 0)

interface Card {
  key: string
  kind: 'user' | 'assistant' | 'reasoning' | 'tool' | 'toolgroup' | 'approval' | 'askuser' | 'error' | 'system'
  text?: string
  call?: AgentToolCall
  // 系统提示线(上下文压缩/api_error/回合统计等):居中淡色一行,不占聊天气泡。
  compaction?: AgentCompaction
  sysinfo?: AgentSystemInfo
  // 提问卡(claude AskUserQuestion / codex request_user_input):
  // ask_user 落卡,ask_user_result 按 reqId 回填已选/取消。
  ask?: AgentAskUser
  askResult?: AgentAskUserResult
  // 聚合组:连续 ≥2 张可组工具(混排不限同类)合成的一张卡(hapi 模型)。
  groupCalls?: AgentToolCall[]
  req?: AgentPermissionReq
  resolved?: { allow: boolean; session?: boolean }
  // 审批聚合:同一 reqId 的多个请求(推送侧可能重复落)合到第一张卡上。
  extraReqs?: AgentPermissionReq[]
  // 工具卡内嵌审批:同工具的审批请求并进这张工具卡(Write 两卡合一)。
  pendingReq?: AgentPermissionReq
}

// 卡片由此派生之前先声明已决记录(上面的 computed 引用它)。
// key = reqId,value = 答复结果(allow + 是否本会话允许);用 ref 包 Map 让
// .set 触发卡片重算。WS 的 permission_result 到达后两条记录语义一致,不冲突。
const decided = ref(new Map<string, { allow: boolean; session?: boolean }>())

// CLI 的 / 指令清单(claude init 透传 / codex 内置):cards 里捞出来存这里,
// 传给 Composer 做输入提示。切会话时清空,等新会话的事件到。
const cliCommands = ref<string[]>([])

// ---- 任务清单(hapi SessionStatusPanel 同款:会话级状态,不进时间线) ----
// Task 套件(新版 claude)与 TodoWrite(旧版)都汇进这里,面板永远显示
// 最新快照。TaskGet 只读单条,不改状态;TaskList 结果是全表,整表重建。
const sessionTasks = computed<{ id: string; subject: string; status: 'pending' | 'in_progress' | 'completed' }[]>(() => {
  interface TaskItem { id: string; subject: string; status: 'pending' | 'in_progress' | 'completed' }
  const taskItems = new Map<string, TaskItem>()
  const taskToolOf = new Map<string, string>() // toolUseId → Task 工具名(结果事件反查)
  const normStatus = (s: unknown) =>
    s === 'completed' || s === 'in_progress' || s === 'pending' ? s : undefined
  // TaskCreate 的结果里带真任务 id:从文本/JSON 里抠出来,换掉临时 key。
  const extractTaskId = (result?: string): string => {
    if (!result) return ''
    try {
      const v = JSON.parse(result)
      const id = v && typeof v === 'object' ? (v as any).id ?? (v as any).taskId : v
      if (typeof id === 'number' || typeof id === 'string') return String(id)
    } catch { /* 文本结果:regex 抠数字 */ }
    const m = result.match(/#?(\d+)/)
    return m ? m[1]! : ''
  }
  const taskSuite = (t: string) => t === 'TaskCreate' || t === 'TaskUpdate' || t === 'TaskList' || t === 'TaskGet'
  // TaskList 的文本结果:"#1 [completed] 标题 [blocked by #3]" 每行一条。
  const parseTaskListText = (result: string) => {
    const out: { id: string; subject: string; status: 'pending' | 'in_progress' | 'completed' }[] = []
    for (const line of result.split('\n')) {
      const m = line.match(/^#(\d+)\s+\[(completed|in_progress|pending)\]\s+(.*)$/)
      if (m) {
        out.push({
          id: m[1]!,
          // blocked by 后缀是依赖提示,不是标题
          subject: m[3]!.replace(/\s*\[blocked by [^\]]*\]\s*$/, ''),
          status: m[2] as 'pending' | 'in_progress' | 'completed',
        })
      }
    }
    return out
  }
  for (const ev of messages.value) {
    if (ev.kind !== 'tool_call') continue
    const p = ev.payload as AgentToolCall
    // 结果事件也要放行:旧数据的 tool 是空串(claude 的 tool_result 不带名,
    // 后端回填上线前的存量),归属靠 taskToolOf 反查 —— TaskCreate 结果里
    // 的真任务 id 是把临时 key 换成数字 id 的唯一来源,跳过它 TaskUpdate
    // 就永远配不上对。非 Task 工具的结果查不到归属,自然落空。
    const isResult = !p.tool && !!p.result && !!p.toolUseId
    if (!taskSuite(p.tool) && p.tool !== 'TodoWrite' && p.tool !== 'update_plan' && !isResult) continue
    try {
      const args = p.args ? JSON.parse(p.args) : {}
      // 只记有名有姓的 tool_use;空名结果事件进来会把映射覆盖成 ""
      if (p.toolUseId && p.tool) taskToolOf.set(p.toolUseId, p.tool)
      if (p.tool === 'TaskCreate' && typeof args.subject === 'string' && args.subject) {
        taskItems.set(p.toolUseId || `t${ev.seq}`, { id: p.toolUseId || `t${ev.seq}`, subject: args.subject, status: 'pending' })
      } else if (p.tool === 'TaskUpdate' && args.taskId !== undefined) {
        const key = String(args.taskId)
        if (args.status === 'deleted') {
          // 删除:列表里不再显示(claude 没有 TaskDelete 工具,走 status=deleted)
          taskItems.delete(key)
        } else {
          const cur = taskItems.get(key)
          const subject = typeof args.subject === 'string' && args.subject ? args.subject : cur?.subject ?? `任务 ${key}`
          const status = normStatus(args.status)
          if (status) taskItems.set(key, { id: key, subject, status })
          else if (cur) taskItems.set(key, { ...cur, subject })
        }
      } else if ((p.tool === 'TodoWrite' || p.tool === 'update_plan') && Array.isArray(args.todos ?? args.plan)) {
        // TodoWrite 的 todos 是全量快照(不是增量),直接整表替换。
        const next = new Map<string, TaskItem>()
        for (const t of (args.todos ?? args.plan) as any[]) {
          if (!t || typeof t !== 'object') continue
          const text = t.content ?? t.step ?? t.text
          if (typeof text !== 'string' || !text) continue
          next.set(String(t.id ?? text), { id: String(t.id ?? text), subject: text, status: normStatus(t.status) ?? 'pending' })
        }
        taskItems.clear()
        for (const [k, v] of next) taskItems.set(k, v)
      }
    } catch { /* args 截断:状态不完整也比丢了好 */ }
    // 结果事件(TaskCreate 拿真 id / TaskList 全表重建)。
    if (p.toolUseId && p.result) {
      const tool = taskToolOf.get(p.toolUseId)
      if (tool === 'TaskCreate') {
        const tmpKey = p.toolUseId
        const tmp = taskItems.get(tmpKey)
        if (tmp) {
          const key = extractTaskId(p.result) || tmpKey
          taskItems.delete(tmpKey)
          taskItems.set(key, { ...tmp, id: key })
        }
      } else if (tool === 'TaskList') {
        // 全表重建:结果是 claude 的权威快照。JSON 数组或文本行都认
        // (实测是文本:"#1 [completed] 标题"),重建顺带清掉已删除的任务。
        let list: { id?: string; taskId?: string; subject?: string; title?: string; status?: string }[] | null = null
        try {
          const v = JSON.parse(p.result)
          if (Array.isArray(v)) list = v
        } catch { /* 文本结果走下面的行解析 */ }
        if (!list) list = parseTaskListText(p.result)
        if (list.length) {
          const next = new Map<string, TaskItem>()
          for (const t of list) {
            if (!t || typeof t !== 'object') continue
            const id = String(t.id ?? t.taskId ?? '')
            const subject = t.subject ?? t.title
            if (id && typeof subject === 'string' && subject) {
              next.set(id, { id, subject, status: normStatus(t.status) ?? 'pending' })
            }
          }
          if (next.size) {
            taskItems.clear()
            for (const [k, v2] of next) taskItems.set(k, v2)
          }
        }
      }
    }
  }
  return [...taskItems.values()]
})

// 顶部任务面板的完成计数。
const taskDone = computed(() => sessionTasks.value.filter((t) => t.status === 'completed').length)

const cards = computed<Card[]>(() => {
  const out: Card[] = []
  const toolIndex = new Map<string, number>() // toolUseId → out 下标
  const apprIndex = new Map<string, number>() // reqId → out 下标
  const askIndex = new Map<string, number>() // ask reqId → out 下标
  // 隐藏的任务类工具的 toolUseId:它们的结果事件(tool 名可能为空)一并隐藏。
  const hiddenToolIds = new Set<string>()
  for (const ev of messages.value) {
    switch (ev.kind) {
      case 'user':
        if (typeof ev.payload === 'string' && ev.payload) out.push({ key: `m${ev.seq}`, kind: 'user', text: ev.payload })
        break
      case 'slash_commands': {
        // CLI 指令清单(元信息):记进响应式列表喂给 Composer,不渲染卡片。
        const p = ev.payload
        if (Array.isArray(p) && p.every((x) => typeof x === 'string') && p.length) {
          cliCommands.value = p as string[]
        }
        break
      }
      case 'assistant_text':
        if (typeof ev.payload === 'string' && ev.payload) out.push({ key: `m${ev.seq}`, kind: 'assistant', text: ev.payload })
        break
      case 'reasoning':
        if (typeof ev.payload === 'string' && ev.payload) out.push({ key: `m${ev.seq}`, kind: 'reasoning', text: ev.payload })
        break
      case 'error': {
        const p = ev.payload as { message?: string } | null
        // message 为空时不能拿 payload 对象兜底 —— 模板插值对象会渲染成
        // "[object Object]"(中断回合曾落过空 message 的 error 事件)。
        out.push({ key: `m${ev.seq}`, kind: 'error', text: p?.message || (typeof ev.payload === 'string' ? ev.payload : '发生错误') })
        break
      }
      case 'compaction': {
        // 上下文压缩:完成/失败渲染成居中系统提示线(📦 已压缩 + token 变化);
        // phase=start 是进行中的瞬态,只喂 StatusBar 的 compacting,不进时间线。
        const p = ev.payload as AgentCompaction | null
        if (p && p.phase !== 'start') out.push({ key: `m${ev.seq}`, kind: 'system', compaction: p })
        break
      }
      case 'system_info': {
        // api_error 重试 / 回合统计 / 离开 recap:同样是居中系统提示线
        // (hapi SystemMessage 同款),不接的话过载重试期间像卡死。
        const p = ev.payload as AgentSystemInfo | null
        if (p && p.type) out.push({ key: `m${ev.seq}`, kind: 'system', sysinfo: p })
        break
      }
      case 'ask_user': {
        // agent 向用户提问:选项卡,回答走 api.agentAnswer。
        const p = ev.payload as AgentAskUser | null
        if (p && p.reqId && p.questions?.length) {
          out.push({ key: `m${ev.seq}`, kind: 'askuser', ask: p })
          askIndex.set(p.reqId, out.length - 1)
        }
        break
      }
      case 'ask_user_result': {
        // 回答回执:按 reqId 回填提问卡(历史回放显示已选/取消)。
        const p = ev.payload as AgentAskUserResult | null
        const i = p?.reqId ? askIndex.get(p.reqId) : undefined
        if (i !== undefined && p) out[i].askResult = p
        break
      }
      case 'tool_call': {
        const p = ev.payload as AgentToolCall
        // Task 套件与 TodoWrite 不出卡:状态汇进顶部 sessionTasks 面板。
        // 结果事件的 tool 可能为空串(claude 老数据:tool_result 不带名),
        // 靠记录下来的 toolUseId 追溯隐藏,否则会兜底单开一张卡漏进时间线。
        if (p.toolUseId && hiddenToolIds.has(p.toolUseId)) break
        if (TASK_PANEL_TOOLS.has(p.tool)) {
          if (p.toolUseId) hiddenToolIds.add(p.toolUseId)
          break
        }
        // 结果事件:合进已有的工具卡;没找到(历史被截断/顺序异常)就单开一张。
        if (p.toolUseId && toolIndex.has(p.toolUseId)) {
          const card = out[toolIndex.get(p.toolUseId)!]
          if (card.call) {
            card.call = { ...card.call, result: p.result, state: p.state, images: p.images }
          }
          break
        }
        out.push({ key: `m${ev.seq}`, kind: 'tool', call: p })
        if (p.toolUseId) toolIndex.set(p.toolUseId, out.length - 1)
        break
      }
      case 'permission_request': {
        const p = ev.payload as AgentPermissionReq
        // 同一 reqId 重复到达(重连补差与推送重合等):并进已有卡,不再单开一张。
        if (p.reqId && apprIndex.has(p.reqId)) {
          const card = out[apprIndex.get(p.reqId)!]
          if (card.extraReqs) card.extraReqs.push(p)
          else if (card.req && card.req.reqId !== p.reqId) card.extraReqs = [p]
          break
        }
        // 同工具的 running 工具卡就在跟前(claude 的 Write/Edit/Bash:先 tool_use 卡
        // 再审批请求):把审批并进那张卡,不再单开一张 —— 免得"同一个工具两张卡"。
        // 往前看 3 张并包含最后一张(刚推入的 tool_use 卡就是它)。
        const near = out.slice(-3)
        const host = [...near].reverse().find((cd) =>
          cd.kind === 'tool' && cd.call && cd.call.tool === p.tool && cd.call.state === 'running' && !cd.pendingReq)
        if (host) {
          host.pendingReq = p
          const local = decided.value.get(p.reqId)
          host.resolved = local ? { ...local } : undefined
          if (p.reqId) apprIndex.set(p.reqId, out.indexOf(host))
          break
        }
        // 本端已答复但 WS 回执未到(弱网/断线)时,先按本地记录收起按钮。
        const local = decided.value.get(p.reqId)
        out.push({ key: `m${ev.seq}`, kind: 'approval', req: p, resolved: local ? { ...local } : undefined })
        if (p.reqId) apprIndex.set(p.reqId, out.length - 1)
        break
      }
      case 'permission_result': {
        const p = ev.payload as { reqId?: string; allow?: boolean; session?: boolean }
        const i = p.reqId ? apprIndex.get(p.reqId) : undefined
        if (i !== undefined) out[i].resolved = { allow: !!p.allow, session: !!p.session }
        break
      }
      // status 不渲染:回合状态走下面的 turnState。
    }
  }
  return groupToolCards(out)
})

// ---- 工具卡聚合(hapi buildVisibleChatBlocks 的精简版) ----
// 连续 ≥2 张"可组"工具卡(混排不限同类)合一张 ToolGroupCard。
// 聚合规则(hapi buildVisibleChatBlocks 同款):连续的工具卡混排聚合成一张
// 组卡 —— 不限同类(Read+Grep+Bash+Edit 连着出就进同一组),只有少数例外
// 单独成卡:子 agent 启动类(里程碑)、计划类、待审批的(要等决定)。
// 注意:聚合只在渲染层,toolUseId 索引(结果合并、审批内嵌)都已完成,
// 组卡是最终展示形态,不需要再参与回填。
const UNGROUPABLE_TOOLS = new Set([
  // 里程碑:开 agent / 跨 agent 通信,时间线上值得单独一卡
  'Task', 'Agent', 'CodexAgent', 'TeamCreate', 'TeamDelete', 'SendMessage',
  'Skill', 'spawn_agent', 'send_input', 'send_message', 'resume_agent',
  'followup_task', 'wait_agent', 'close_agent', 'interrupt_agent', 'list_agents',
  // 计划类:进任务面板或单独成卡
  'TodoWrite', 'update_plan', 'ExitPlanMode', 'exit_plan_mode', 'CodexReasoning',
  // codex 长期目标:里程碑级,单独成卡
  'CodexGoal',
])

// compactionText 压缩提示文案(hapi SystemMessage 同款语义):微压缩报省下
// 多少,整压报压缩前规模;没带数字就说一句"已压缩";失败带原因。
function compactionText(p: AgentCompaction): string {
  if (p.failed) return p.error ? `上下文压缩失败:${p.error}` : '上下文压缩失败'
  if (p.micro) {
    if (p.tokensSaved) return `上下文已压缩(省下 ${p.tokensSaved.toLocaleString()} tokens)`
    return '上下文已压缩'
  }
  if (p.preTokens) return `会话已压缩(原上下文 ${p.preTokens.toLocaleString()} tokens)`
  return '会话已压缩'
}

// sysinfoIcon 系统信息行前缀图标:重试 ⏳ / 上限 ⚠️ / 回合统计 ⏱️ / recap 💭 /
// 后台通知 🔔(失败 ⚠️)/ 监视器事件 📡。
function sysinfoIcon(p: AgentSystemInfo): string {
  switch (p.type) {
    case 'api_error': return p.retry ? '⏳' : '⚠️'
    case 'turn_duration': return '⏱️'
    case 'task_notification': {
      if (p.status === 'failed') return '⚠️'
      if ((p.text || '').startsWith('Monitor event:')) return '📡'
      return '🔔'
    }
    default: return '💭'
  }
}

// sysinfoText 系统信息行文案(hapi presentation 同款):api_error 重试进度、
// 回合结束统计、离开期间的 recap。
function sysinfoText(p: AgentSystemInfo): string {
  switch (p.type) {
    case 'api_error': {
      if (p.retry && p.maxRetry) return `API 错误,重试中(${p.retry}/${p.maxRetry})`
      if (p.maxRetry) return `API 错误:重试已达上限(${p.maxRetry} 次)${p.error ? `:${p.error}` : ''}`
      return `API 错误${p.error ? `:${p.error}` : ''}`
    }
    case 'turn_duration': {
      const sec = Math.round((p.durationMs ?? 0) / 1000)
      const dur = sec >= 60 ? `${Math.floor(sec / 60)} 分 ${sec % 60} 秒` : `${sec} 秒`
      const parts = [`回合完成 · ${dur}`]
      if (p.turns) parts.push(`${p.turns} 轮`)
      if (p.costUsd) parts.push(`$${p.costUsd.toFixed(2)}`)
      return parts.join(' · ')
    }
    case 'away_summary':
      return p.text ? `recap:${p.text}` : 'recap'
    default:
      return p.text || p.error || ''
  }
}

// notifyLabel / notifySummary 后台通知卡(sysinfoText 已不覆盖 task_notification,
// 单独拆出来):Monitor 的 summary 形如 'Monitor event: "名字"',剥前缀取名字;
// 正文走 markdown 渲染,不再塞一行纯文本。
function notifyLabel(p: AgentSystemInfo): string {
  if ((p.text || '').startsWith('Monitor event:')) return '监视器'
  if (p.status === 'failed') return '后台任务失败'
  if (p.status === 'completed') return '后台任务完成'
  return p.status ? `后台任务(${p.status})` : '后台任务'
}

function notifySummary(p: AgentSystemInfo): string {
  return (p.text || '').replace(/^Monitor event:\s*/, '')
}

// groupableToolCard 一张工具卡能否进组:非例外工具、且没有未决审批
// (已答复的审批照常进组,审批内嵌的展示由详情弹窗兜底)。
function groupableToolCard(c: Card): boolean {
  return c.kind === 'tool' && !!c.call && !UNGROUPABLE_TOOLS.has(c.call.tool)
    && !(c.pendingReq && !c.resolved)
}

function groupToolCards(cards: Card[]): Card[] {
  const out: Card[] = []
  let i = 0
  while (i < cards.length) {
    if (!groupableToolCard(cards[i])) {
      out.push(cards[i])
      i++
      continue
    }
    // 收集连续可组工具,不限同类(hapi:混排进同一组,标题按主导意图起)。
    const group: AgentToolCall[] = [cards[i].call!]
    let j = i + 1
    while (j < cards.length && groupableToolCard(cards[j])) {
      group.push(cards[j].call!)
      j++
    }
    if (group.length < 2) {
      out.push(cards[i])
      i++
      continue
    }
    out.push({ key: `g${cards[i].key}`, kind: 'toolgroup', groupCalls: group })
    i = j
  }
  return out
}

// 回合状态:会话存活时看最后一条 status 事件,死了就是 dead。
const turnState = computed<'running' | 'idle' | 'dead'>(() => {
  if (!selected.value || selected.value.status === 'dead') return 'dead'
  for (let i = messages.value.length - 1; i >= 0; i--) {
    const ev = messages.value[i]
    if (ev.kind === 'status') return (ev.payload as { state?: string })?.state === 'running' ? 'running' : 'idle'
  }
  return 'idle'
})

// 压缩进行中(claude code 底部 Compacting… 同款):从后往前找,先碰到
// 压缩事件就看它是不是 start;先碰到回合状态/正文/用量就说明压缩已经
// 结束(边界事件或新一轮输出都排在 start 后面)。
const compacting = computed(() => {
  for (let i = messages.value.length - 1; i >= 0; i--) {
    const ev = messages.value[i]
    if (ev.kind === 'compaction') return (ev.payload as AgentCompaction | null)?.phase === 'start'
    if (ev.kind === 'status' || ev.kind === 'assistant_text' || ev.kind === 'usage') return false
  }
  return false
})

// 任务类工具:时间线不出卡,状态只进顶部 sessionTasks 面板(hapi 同款)。
const TASK_PANEL_TOOLS = new Set([
  'TaskCreate', 'TaskUpdate', 'TaskList', 'TaskGet', 'TodoWrite', 'update_plan',
])

// 最新 token 用量(StatusBar 上下文占用):取最后一条 usage 事件。
const latestUsage = computed<AgentUsage | null>(() => {
  for (let i = messages.value.length - 1; i >= 0; i--) {
    const ev = messages.value[i]
    if (ev.kind === 'usage') return ev.payload as AgentUsage
  }
  return null
})

// 有待决审批(工具卡内嵌或独立卡)或未答提问:StatusBar 状态优先显示。
const pendingApproval = computed(() =>
  cards.value.some((c) => (c.kind === 'approval' && !c.resolved) || (c.kind === 'tool' && c.pendingReq && !c.resolved)),
)
const pendingAsk = computed(() =>
  cards.value.some((c) => c.kind === 'askuser' && !c.askResult),
)

// ---- 流式输出(瞬态增量,不进 messages) ----
// assistant_delta / reasoning_delta 是 --include-partial-messages 的逐段增量:
// 只在 WS 上广播、不落库,完整消息随后到达并落库。这里攒两块缓冲区直播,
// 任意持久化事件(含完整消息、状态、错误)到达即清空 —— 缓冲区只负责
// "正在生成"的那一段,历史回放完全靠落库消息,不依赖它。
const streamText = ref('')
const streamThink = ref('')
watch([streamText, streamThink], () => scrollBottom())

// ---- WS 推送 ----
const ws = new AgentWS((e) => {
  if (e.type === 'message') {
    const ev = e.event
    if (ev.sessionId !== selectedId.value) return
    // 流式增量:无 seq(不落库),不走去重,直接进缓冲区。
    if (ev.kind === 'assistant_delta' || ev.kind === 'reasoning_delta') {
      const t = typeof ev.payload === 'string' ? ev.payload : ''
      if (ev.kind === 'assistant_delta') streamText.value += t
      else streamThink.value += t
      return
    }
    // 任何持久化事件都意味着当前流式块已收尾(完整消息就是下一个事件)。
    streamText.value = ''
    streamThink.value = ''
    // seq 去重:REST 响应已本地追加过,推送与补差重合的也在这里丢掉。
    if (ev.seq <= maxSeq.value) return
    messages.value.push(ev)
  } else if (e.type === 'exit') {
    // exit 只发给会话订阅者:正在看这个会话才收得到,顺手清排队消息。
    if (e.sessionId !== selectedId.value) return
    streamText.value = ''
    streamThink.value = ''
    markDead(e.sessionId)
  } else if (e.type === 'session') {
    // 会话状态变化(watch 观察者):列表实时刷新,不管当前看没看它。
    // 不认识的 id(别的设备新建的)先不管,等下次进页面拉列表。
    const s = sessions.value.find((x) => x.id === e.sessionId)
    if (s) {
      s.status = e.status
      if (e.title && !s.title) s.title = e.title
    }
  } else if (e.type === 'open') {
    // (重)连上:按 seq 补差,期间丢的推送都找得回来。
    catchUp()
  }
})

function markDead(id: string) {
  const s = sessions.value.find((x) => x.id === id)
  if (s) s.status = 'dead'
  if (id === selectedId.value) queued.value = []
}

async function catchUp() {
  const id = selectedId.value
  if (!id) return
  try {
    const got = await api.agentMessages(id, { afterSeq: maxSeq.value })
    for (const ev of got) {
      if (ev.seq > maxSeq.value) messages.value.push(ev)
    }
  } catch { /* 下次重连再补 */ }
}

// ---- 会话打开/关闭 ----
const timelineEl = ref<HTMLElement | null>(null)
// 历史分页:一页 200 条(与 hapi 一致)。打开时只取最新一页,更早的往前翻(顶部按钮)。
const MSG_PAGE = 200
const hasOlder = ref(false)
const loadingOlder = ref(false)

// 手机上聊天视图盖住列表;桌面两栏并排(CSS 处理,这里只管状态)。
async function openSession(id: string) {
  if (selectedId.value && selectedId.value !== id) ws.unsubscribe(selectedId.value)
  selectedId.value = id
  messages.value = []
  streamText.value = ''
  streamThink.value = ''
  decided.value.clear()
  cliCommands.value = []
  pendingBubbles.value = []
  queued.value = []
  hasOlder.value = false
  try {
    const got = await api.agentMessages(id, { limit: MSG_PAGE })
    messages.value = got
    // 拉满一页且最旧一条不是 seq 1,说明前面还有更早的。
    hasOlder.value = got.length >= MSG_PAGE && got[0].seq > 1
  } catch (e: any) {
    message.error(e?.message || '加载历史失败')
  }
  ws.subscribe(id)
  scrollBottom(true)
}

async function loadOlder() {
  const id = selectedId.value
  if (!id || !messages.value.length || loadingOlder.value) return
  loadingOlder.value = true
  try {
    const got = await api.agentMessages(id, { beforeSeq: messages.value[0].seq, limit: MSG_PAGE })
    if (got.length) {
      // 补出来的消息插在最前;把新增高度加回 scrollTop,视口停在原来的消息上。
      const el = timelineEl.value
      const prevHeight = el?.scrollHeight ?? 0
      const prevTop = el?.scrollTop ?? 0
      messages.value = [...got, ...messages.value]
      nextTick(() => { if (el) el.scrollTop = prevTop + (el.scrollHeight - prevHeight) })
      hasOlder.value = got.length >= MSG_PAGE && got[0].seq > 1
    } else {
      hasOlder.value = false
    }
  } catch (e: any) {
    message.error(e?.message || '加载失败')
  } finally {
    loadingOlder.value = false
  }
}

function backToList() {
  if (selectedId.value) ws.unsubscribe(selectedId.value)
  selectedId.value = null
  messages.value = []
  queued.value = []
  loadSessions()
}

// 只在本来就在底部时跟底:往上翻历史时不许把人拽下去。
function scrollBottom(force = false) {
  const el = timelineEl.value
  if (!el) return
  if (!force && el.scrollHeight - el.scrollTop - el.clientHeight > 120) return
  nextTick(() => { el.scrollTop = el.scrollHeight })
}
watch(() => cards.value.length, () => scrollBottom())

// ---- 发送 / 排队 / 打断 / 审批 ----
const sending = ref(false)
// 回合进行中发出的消息排队等下一个空闲:agent 一次只吃一条,
// 插话会被 codex 当 steer / claude 语义不明,排队是最不会出错的形态。
const queued = ref<string[]>([])
// 排队气泡入列也要跟底(声明在 queued 之后,别再犯 TDZ)。
watch(() => queued.value.length, () => scrollBottom())

// 乐观气泡:发送先入列再等 REST 落库回执(否则断网时消息凭空消失几秒)。
// state: sending = 转圈等回执;failed = 发送失败(⚠ 可重发)。
interface PendingBubble { key: number; text: string; state: 'sending' | 'failed' }
const pendingBubbles = ref<PendingBubble[]>([])
let pendingSeq = 0
watch(() => pendingBubbles.value.length, () => scrollBottom())

function send(text: string) {
  if (!selectedId.value) return
  if (turnState.value === 'running') {
    queued.value.push(text)
    return
  }
  doSend(text)
}

async function doSend(text: string) {
  if (!selectedId.value) return
  const s = sessions.value.find((x) => x.id === selectedId.value)
  const wasDead = s?.status === 'dead'
  // 乐观入列:气泡立刻出现在对话流里(转圈),REST 回执到了再换真事件。
  const bubble: PendingBubble = { key: ++pendingSeq, text, state: 'sending' }
  pendingBubbles.value.push(bubble)
  sending.value = true
  try {
    const ev = await api.agentSend(selectedId.value, text)
    if (ev.seq > maxSeq.value) messages.value.push(ev)
    const i = pendingBubbles.value.indexOf(bubble)
    if (i >= 0) pendingBubbles.value.splice(i, 1)
    if (s) {
      // 首条消息即标题:后端已落库,本地同步一下,聊天头立即从路径换成摘要。
      // 规则与后端 titleFromText 一致:第一行,超 30 字省略。
      if (!s.title) {
        const line = text.split(/\r?\n/)[0]!.trim()
        s.title = [...line].length > 30 ? [...line].slice(0, 30).join('') + '…' : line
      }
      // 死会话被后端复活了:本地同步状态,并补一次订阅
      // (打开时订阅在服务端落空,不补就收不到后续推送)。
      if (wasDead) {
        s.status = 'running'
        ws.subscribe(s.id)
      }
    }
  } catch (e: any) {
    // 失败不吞:气泡标 ⚠,点重发再试一次;REST 没回执前消息没落库,
    // 重发不会重复。
    bubble.state = 'failed'
    message.error(e?.message || '发送失败')
  } finally {
    sending.value = false
  }
}

// 重发失败的气泡:doSend 自己会重新 push 一个气泡(且在首个 await 前同步完成),
// 紧接着把旧的抽掉,时间线上不出现重复。
function retrySend(key: number) {
  const b = pendingBubbles.value.find((x) => x.key === key)
  if (!b || b.state !== 'failed' || sending.value) return
  doSend(b.text)
  const j = pendingBubbles.value.indexOf(b)
  if (j >= 0) pendingBubbles.value.splice(j, 1)
}

// 丢弃失败的气泡(放弃这条消息)。
function dropPending(key: number) {
  const i = pendingBubbles.value.findIndex((x) => x.key === key)
  if (i >= 0) pendingBubbles.value.splice(i, 1)
}

// 回合结束(idle)自动放行队首一条;该条发出后状态会转 running,
// 下一次 idle 再放行下一条 —— 逐条串行,天然限速。
watch(turnState, (st) => {
  if (st === 'idle' && queued.value.length && !sending.value) {
    doSend(queued.value.shift()!)
  }
})

async function interrupt() {
  if (!selectedId.value) return
  try {
    await api.agentInterrupt(selectedId.value)
  } catch (e: any) {
    message.error(e?.message || '打断失败')
  }
}

const approveBusy = ref('')
async function decide(reqId: string, allow: boolean, session = false) {  if (!selectedId.value) return
  approveBusy.value = reqId
  try {
    await api.agentApprove(selectedId.value, reqId, allow, session)
    // 服务端广播 permission_result 会经 WS 回来落库;本地先记一份,
    // 弱网下按钮也立即收起,重连补差后两条记录被同一 reqId 覆盖、不重复渲染。
    decided.value.set(reqId, { allow, session })
    // 换新 Map 引用触发 cards 重算(同 reqId 聚合的那张卡收起按钮)。
    decided.value = new Map(decided.value)
  } catch (e: any) {
    message.error(e?.message || '操作失败')
  } finally {
    approveBusy.value = ''
  }
}

// 回答 agent 的提问:null = 取消。回执(ask_user_result)经 WS 回来落库,
// 这里只负责发 REST + 记 busy。
const askBusy = ref('')
async function answerAsk(reqId: string, answers: Record<string, string[]> | null) {
  if (!selectedId.value) return
  askBusy.value = reqId
  try {
    await api.agentAnswer(selectedId.value, reqId, answers ?? undefined)
  } catch (e: any) {
    message.error(e?.message || '回答失败')
  } finally {
    askBusy.value = ''
  }
}

async function killSession(s: AgentSession) {
  try {
    await api.agentKill(s.id)
    markDead(s.id)
    message.success('已结束')
  } catch (e: any) {
    message.error(e?.message || '结束失败')
  }
}

async function deleteSession(s: AgentSession) {
  try {
    await api.agentDelete(s.id)
    sessions.value = sessions.value.filter((x) => x.id !== s.id)
    if (selectedId.value === s.id) backToList()
    message.success('已删除')
  } catch (e: any) {
    message.error(e?.message || '删除失败')
  }
}

// ---- 多选删除 ----
const selectMode = ref(false)
const selectedIds = ref(new Set<string>())
const deletingSelected = ref(false)
const allSelected = computed(
  () => selectMode.value && sessions.value.length > 0 && selectedIds.value.size === sessions.value.length,
)

function enterSelectMode() {
  selectMode.value = true
  selectedIds.value = new Set()
}

function exitSelectMode() {
  selectMode.value = false
  selectedIds.value = new Set()
}

function toggleSelect(id: string) {
  const next = new Set(selectedIds.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  selectedIds.value = next
}

function toggleSelectAll() {
  selectedIds.value = allSelected.value
    ? new Set()
    : new Set(sessions.value.map((s) => s.id))
}

async function deleteSelected() {
  if (!selectedIds.value.size) return
  deletingSelected.value = true
  const ids = [...selectedIds.value]
  let failed = 0
  // 逐个删而不是并发:每个删除都要杀进程树,一窝蜂打过去容易撞车。
  for (const id of ids) {
    try {
      await api.agentDelete(id)
    } catch {
      failed++
    }
  }
  deletingSelected.value = false
  if (failed) message.error(`${failed} 个会话删除失败,其余已删除`)
  else message.success(`已删除 ${ids.length} 个会话`)
  // 正在聊天的会话被删了就回列表;backToList 内部会刷新列表。
  if (selectedId.value && selectedIds.value.has(selectedId.value)) backToList()
  else await loadSessions()
  selectedIds.value = new Set()
}

// ---- 会话设置(模型/思考强度/权限模式) ----
const effortOptions = [
  { value: 'none', label: '默认' },
  { value: 'low', label: '低' },
  { value: 'medium', label: '中' },
  { value: 'high', label: '高' },
]

// 权限 4 档,两家的原生模式由后端各自映射(claude: plan/default/acceptEdits/bypass;
// codex: untrusted+只读 / on-request / on-failure / never+全放行)。
const permOptions = [
  { value: 'plan', label: '只读' },
  { value: 'ask', label: '询问' },
  { value: 'edits', label: '编辑' },
  { value: 'accept', label: '放行' },
]
const permHints: Record<string, string> = {
  plan: '只看不改:执行与写入都要你批准,适合让它先出方案。',
  ask: '工具调用推审批卡片,批准后才执行 —— 离开屏幕也能靠推送接着批。',
  edits: '文件编辑直接放行,命令等仍会询问。',
  accept: '全自动,不再询问。只在你盯着的时候用。',
}

const settingsModal = ref(false)
const setModel = ref('')
const setEffort = ref('none')
const setPerm = ref('ask')
const setBusy = ref(false)

function openSettings() {
  setModel.value = selected.value?.model || ''
  setEffort.value = selected.value?.effort || 'none'
  setPerm.value = selected.value?.permissionMode || 'ask'
  settingsModal.value = true
}

// 应用设置并同步本地会话记录;note 非空 = 保存了但不立即生效(claude 续聊才生效)。
async function applySettings(patch: { model?: string; effort?: string; permissionMode?: string }) {
  if (!selectedId.value) return
  setBusy.value = true
  try {
    const res = await api.agentSettings(selectedId.value, patch)
    const i = sessions.value.findIndex((x) => x.id === selectedId.value)
    if (i >= 0) sessions.value[i] = { ...sessions.value[i], ...res.session }
    if (res.note) message.warning(res.note)
    else message.success('已生效(下一回合起)')
    settingsModal.value = false
  } catch (e: any) {
    message.error(e?.message || '设置失败')
  } finally {
    setBusy.value = false
  }
}

// Composer 的 "/" 指令:与设置弹层同一通道。
async function onCommand(name: string, arg: string) {
  if (!selectedId.value) return
  if (name === 'interrupt') {
    await interrupt()
    return
  }
  if (!arg) {
    message.error('缺少参数')
    return
  }
  if (name === 'think' && !effortOptions.some((o) => o.value === arg)) {
    message.error('思考强度可选:none / low / medium / high')
    return
  }
  if (name === 'perm' && !permOptions.some((o) => o.value === arg)) {
    message.error('权限模式可选:plan / ask / edits / accept')
    return
  }
  if (name === 'model') applySettings({ model: arg })
  else if (name === 'think') applySettings({ effort: arg })
  else if (name === 'perm') applySettings({ permissionMode: arg })
}

// ---- 新建 / 续聊 ----
const createModal = ref(false)
const createApp = ref<'claude' | 'codex'>('claude')
const createWorkspace = ref<string | null>(null)
const createPerm = ref('ask')
const createModel = ref('')
const createEffort = ref('none')
const creating = ref(false)

const workspaceOptions = computed(() =>
  wsStore.list.map((w) => ({ label: w.name, value: w.path })),
)

function openCreate() {
  createApp.value = 'claude'
  createWorkspace.value = wsStore.currentPath || wsStore.root
  createPerm.value = 'ask'
  createModel.value = ''
  createEffort.value = 'none'
  createModal.value = true
}

async function createSession() {
  creating.value = true
  try {
    const sess = await api.agentSessionCreate({
      app: createApp.value,
      workspace: createWorkspace.value || wsStore.root,
      permissionMode: createPerm.value,
      model: createModel.value.trim() || undefined,
      effort: createEffort.value === 'none' ? undefined : createEffort.value,
    })
    createModal.value = false
    await loadSessions()
    await openSession(sess.id)
  } catch (e: any) {
    message.error(e?.message || '创建失败')
  } finally {
    creating.value = false
  }
}

// ---- 清理(DESIGN-AGENT.md 5.7) ----
const cleanupModal = ref(false)
const cleanupDays = ref(30)
const cleanupAuto = ref(false)
const cleanupPreview = ref<{ sessions: number; messages: number } | null>(null)
const cleanupLast = ref<{ sessions: number; messages: number } | null>(null)
const cleanupLastAt = ref('')
const cleanupBusy = ref('')

async function openCleanup() {
  cleanupPreview.value = null
  try {
    const st = await api.agentCleanupStatus()
    cleanupDays.value = st.days
    cleanupAuto.value = st.auto
    cleanupLast.value = st.lastRun ?? null
    cleanupLastAt.value = st.lastAt ?? ''
  } catch { /* 用默认值 */ }
  cleanupModal.value = true
}

async function previewCleanup() {
  cleanupBusy.value = 'preview'
  try {
    cleanupPreview.value = await api.agentCleanupRun(cleanupDays.value, true)
  } catch (e: any) {
    message.error(e?.message || '预览失败')
  } finally {
    cleanupBusy.value = ''
  }
}

async function runCleanup() {
  cleanupBusy.value = 'run'
  try {
    const res = await api.agentCleanupRun(cleanupDays.value, false)
    message.success(`已清理 ${res.sessions} 个会话、${res.messages} 条消息`)
    cleanupModal.value = false
    await loadSessions()
  } catch (e: any) {
    message.error(e?.message || '清理失败')
  } finally {
    cleanupBusy.value = ''
  }
}

async function saveCleanupSettings() {
  cleanupBusy.value = 'save'
  try {
    const st = await api.agentCleanupSave(cleanupDays.value, cleanupAuto.value)
    cleanupAuto.value = st.auto
    message.success('清理设置已保存')
  } catch (e: any) {
    message.error(e?.message || '保存失败')
  } finally {
    cleanupBusy.value = ''
  }
}

// ---- 杂项 ----
// 状态圆点的悬浮说明(卡片上不再摆文字标签,颜色语义靠这里兜底)。
function statusLabel(s: AgentSession): string {
  if (s.status === 'running') return '回合进行中'
  if (s.status === 'dead') return '已结束,发消息继续'
  return '空闲'
}

function timeAgo(iso: string): string {
  const ms = Date.now() - new Date(iso).getTime()
  if (!Number.isFinite(ms)) return ''
  const min = Math.floor(ms / 60000)
  if (min < 1) return '刚刚'
  if (min < 60) return `${min} 分钟前`
  const h = Math.floor(min / 60)
  if (h < 24) return `${h} 小时前`
  return `${Math.floor(h / 24)} 天前`
}

onMounted(async () => {
  // watch:观察全部会话状态,列表的运行/空闲/结束点实时刷新(不只当前会话)。
  ws.connect()
  ws.watch()
  await Promise.all([loadSessions(), wsStore.ensure()])
})
onBeforeUnmount(() => {
  ws.close()
  immersive.value = false
})
</script>

<template>
  <div class="page-content agent-page" :class="{ 'chat-open': !!selectedId }">
    <!-- ---- 会话列表 ---- -->
    <div class="list-col">
      <div class="agent-head">
        <h2>Agent 会话</h2>
        <div class="agent-head-ops">
          <template v-if="selectMode">
            <n-button size="small" secondary @click="toggleSelectAll">{{ allSelected ? '取消全选' : '全选' }}</n-button>
            <n-popconfirm @positive-click="deleteSelected">
              <template #trigger>
                <n-button size="small" type="error" :disabled="!selectedIds.size" :loading="deletingSelected">
                  删除({{ selectedIds.size }})
                </n-button>
              </template>
              删除选中的 {{ selectedIds.size }} 个会话?聊天记录与附件一并删除,不可恢复。
            </n-popconfirm>
            <n-button size="small" secondary @click="exitSelectMode">完成</n-button>
          </template>
          <template v-else>
            <n-button size="small" secondary title="选择" aria-label="选择" @click="enterSelectMode">
              <template #icon><n-icon :component="CheckboxOutline" /></template>
            </n-button>
            <n-button size="small" secondary title="清理" aria-label="清理" @click="openCleanup">
              <template #icon><n-icon :component="HourglassOutline" /></template>
            </n-button>
            <n-button size="small" type="primary" title="新建会话" aria-label="新建会话" @click="openCreate">
              <template #icon><n-icon :component="AddOutline" /></template>
            </n-button>
          </template>
        </div>
      </div>
      <n-spin :show="loading">
        <n-empty v-if="!sessions.length" description="还没有会话。新建一个,在手机上遥控 Claude Code。" class="agent-empty">
          <template #extra>
            <n-button size="small" @click="openCreate">新建会话</n-button>
          </template>
        </n-empty>
        <div v-else class="sess-list">
          <div v-for="g in sessionGroups" :key="g.workspace" class="sess-group">
            <button type="button" class="sess-group-head" @click="toggleGroup(g)">
              <span class="sess-chevron" :class="{ open: !isCollapsed(g) }">▸</span>
              <span class="sess-group-path">{{ g.workspace }}</span>
              <!-- 折叠时露个数量,展开就不占地方 -->
              <span v-if="isCollapsed(g)" class="sess-count">{{ g.list.length }}</span>
            </button>
            <template v-if="!isCollapsed(g)">
              <div
                v-for="s in g.list" :key="s.id" class="sess-card"
                :class="{ on: s.id === selectedId, checked: selectMode && selectedIds.has(s.id) }"
              >
                <button
                  v-if="selectMode" type="button" class="sess-check"
                  :title="selectedIds.has(s.id) ? '取消选择' : '选择'" @click="toggleSelect(s.id)"
                >
                  <span class="sess-check-box">{{ selectedIds.has(s.id) ? '✓' : '' }}</span>
                </button>
                <button
                  type="button" class="sess-main"
                  @click="selectMode ? toggleSelect(s.id) : openSession(s.id)"
                >
                  <div class="sess-title-row">
                    <span class="sess-dot" :class="s.status" :title="statusLabel(s)" />
                    <span class="sess-title">{{ s.title || '未命名会话' }}</span>
                  </div>
                  <div class="sess-meta">
                    <span class="sess-app">{{ s.app === 'claude' ? 'Claude Code' : s.app }}</span>
                    <span class="sess-time">{{ timeAgo(s.updatedAt) }}</span>
                  </div>
                </button>
                <div v-if="!selectMode" class="sess-ops">
                  <n-button
                    v-if="s.status !== 'dead'" size="tiny" quaternary type="error"
                    title="结束进程" @click="killSession(s)"
                  >
                    <template #icon><n-icon :component="StopOutline" /></template>
                  </n-button>
                  <n-popconfirm @positive-click="deleteSession(s)">
                    <template #trigger>
                      <n-button size="tiny" quaternary type="error" title="删除会话">
                        <template #icon><n-icon :component="TrashOutline" /></template>
                      </n-button>
                    </template>
                    删除该会话?聊天记录与附件一并删除,不可恢复。
                  </n-popconfirm>
                </div>
              </div>
            </template>
          </div>
        </div>
      </n-spin>
    </div>

    <!-- ---- 聊天视图 ---- -->
    <div v-if="selected" class="chat-col">
      <div class="chat-head">
        <button type="button" class="chat-back" title="返回列表" @click="backToList">‹</button>
        <div class="chat-title">
          <div class="chat-name">{{ selected.title || selected.workspace }}</div>
          <!-- 回合状态/模型挪到输入框上方的 StatusBar,聊天头只留标题 -->
          <div class="chat-sub"><span>{{ selected.workspace }}</span></div>
        </div>
        <button type="button" class="chat-settings" title="会话设置" @click="openSettings">⚙</button>
      </div>

      <!-- 任务清单(hapi SessionStatusPanel 同款):会话级状态,钉在 header 下,
      永远最新;Task 套件/TodoWrite 的事件都汇进这里,时间线不出卡 -->
      <details v-if="sessionTasks.length" class="task-panel">
        <summary>
          <span class="task-panel-title">任务清单</span>
          <span class="task-panel-count">{{ taskDone }}/{{ sessionTasks.length }}</span>
          <span class="task-panel-chev">▾</span>
        </summary>
        <div class="task-panel-body">
          <div v-for="t in sessionTasks" :key="t.id" class="task-item" :class="t.status">
            <span class="task-mark">{{ t.status === 'completed' ? '☑' : t.status === 'in_progress' ? '◉' : '☐' }}</span>
            <span class="task-text">{{ t.subject }}</span>
          </div>
        </div>
      </details>

      <div ref="timelineEl" class="chat-timeline">
        <div v-if="hasOlder" class="load-older">
          <n-button size="tiny" quaternary :loading="loadingOlder" @click="loadOlder">加载更早的消息</n-button>
        </div>
        <n-empty v-if="!cards.length" description="会话已就绪,发第一条消息开始" class="chat-empty" />
        <div v-for="c in cards" :key="c.key" class="chat-row" :class="c.kind">
          <div v-if="c.kind === 'user'" class="bubble user">{{ c.text }}</div>
          <div v-else-if="c.kind === 'assistant'" class="agent-plain agent-md-body" v-html="renderMarkdown(c.text || '')" />
          <details v-else-if="c.kind === 'reasoning'" class="reasoning">
            <summary>思考过程</summary>
            <div class="reasoning-body">{{ c.text }}</div>
          </details>
          <ToolGroupCard v-else-if="c.kind === 'toolgroup' && c.groupCalls" :calls="c.groupCalls" :session-id="selectedId || undefined" />
          <ToolCallCard
            v-else-if="c.kind === 'tool' && c.call" :call="c.call" :session-id="selectedId || undefined"
            :pending-req="c.pendingReq" :pending-resolved="c.resolved ?? null"
            :approve-busy="c.pendingReq ? approveBusy === c.pendingReq.reqId : false"
            @decide="(allow, session) => c.pendingReq && decide(c.pendingReq.reqId, allow, session)"
          />
          <ApprovalCard
            v-else-if="c.kind === 'approval' && c.req"
            :req="c.req" :resolved="c.resolved ?? null" :busy="approveBusy === c.req.reqId"
            @decide="(allow, session) => decide(c.req!.reqId, allow, session)"
          />
          <AskUserCard
            v-else-if="c.kind === 'askuser' && c.ask"
            :ask="c.ask" :result="c.askResult ?? null" :busy="askBusy === c.ask.reqId"
            @answer="(answers) => answerAsk(c.ask!.reqId, answers)"
          />
          <div v-else-if="c.kind === 'error'" class="bubble error">{{ c.text }}</div>
          <div v-else-if="c.kind === 'system' && c.compaction" class="sysline" :class="{ 'sysline-failed': c.compaction.failed }">📦 {{ compactionText(c.compaction) }}</div>
          <!-- 后台通知卡:头部图标+标签,正文 markdown 渲染,Monitor 的 event 是原始日志行走等宽 pre -->
          <div v-else-if="c.kind === 'system' && c.sysinfo?.type === 'task_notification'" class="notify-card" :class="{ 'notify-failed': c.sysinfo.status === 'failed' }">
            <div class="notify-head">{{ sysinfoIcon(c.sysinfo) }} {{ notifyLabel(c.sysinfo) }}</div>
            <div v-if="notifySummary(c.sysinfo)" class="notify-body agent-md-body" v-html="renderMarkdown(notifySummary(c.sysinfo))" />
            <pre v-if="c.sysinfo.event" class="notify-event">{{ c.sysinfo.event }}</pre>
          </div>
          <div v-else-if="c.kind === 'system' && c.sysinfo" class="sysline" :class="{ 'sysline-failed': c.sysinfo.type === 'api_error' && c.sysinfo.maxRetry && !c.sysinfo.retry }">{{ sysinfoIcon(c.sysinfo) }} {{ sysinfoText(c.sysinfo) }}</div>
        </div>
        <!-- 流式直播区:正在生成的思考与正文(瞬态,完整消息落库后清空换正式卡) -->
        <div v-if="streamThink" class="chat-row reasoning">
          <details class="reasoning" open>
            <summary>思考过程中…</summary>
            <div class="reasoning-body">{{ streamThink }}</div>
          </details>
        </div>
        <div v-if="streamText" class="chat-row assistant">
          <div class="agent-plain agent-md-body" v-html="renderMarkdown(streamText)" />
        </div>
        <!-- 乐观气泡:发送中转圈,失败标 ⚠ 可重发/丢弃 -->
        <div v-for="pb in pendingBubbles" :key="pb.key" class="chat-row user">
          <div class="bubble user pending-bubble" :class="pb.state">
            <div class="pending-text">{{ pb.text }}</div>
            <div class="pending-ops">
              <span v-if="pb.state === 'sending'" class="pending-tag"><span class="pending-spinner" />发送中</span>
              <template v-else>
                <span class="pending-tag failed">⚠ 发送失败</span>
                <button type="button" class="pending-retry" @click="retrySend(pb.key)">重发</button>
                <button type="button" class="pending-drop" @click="dropPending(pb.key)">丢弃</button>
              </template>
            </div>
          </div>
        </div>
        <!-- 排队中的消息:回合结束自动逐条发出,发出前可撤回 -->
        <div v-for="(q, i) in queued" :key="`q${i}`" class="chat-row user">
          <div class="bubble user queued-bubble">
            <div class="queued-text">{{ q }}</div>
            <div class="queued-ops">
              <span class="queued-tag">排队中</span>
              <button type="button" class="queued-cancel" @click="queued.splice(i, 1)">撤回</button>
            </div>
          </div>
        </div>
      </div>

      <!-- 待决审批就是时间线上那张卡(推送也会点进来),不再叠横幅。 -->

      <!-- 状态行(hapi StatusBar 同款位置):回合状态 + 上下文用量都从聊天头挪到这里 -->
      <StatusBar
        :state="turnState"
        :pending-approval="pendingApproval"
        :pending-ask="pendingAsk"
        :compacting="compacting"
        :usage="latestUsage"
        :app="selected.app"
        :model="selected.model"
        :effort="selected.effort"
        :permission-mode="selected.permissionMode"
      />
      <Composer
        :running="turnState === 'running'"
        :disabled="turnState === 'dead' && !selected.externalId"
        :ask-pending="pendingAsk"
        :sending="sending"
        :session-id="selected.id"
        :cli-commands="cliCommands"
        @send="send" @interrupt="interrupt" @command="onCommand"
      />
    </div>

    <!-- ---- 新建会话 ---- -->
    <n-modal v-model:show="createModal" preset="card" title="新建会话" class="agent-modal">
      <div class="form">
        <label>Agent</label>
        <div class="seg">
          <button type="button" class="seg-btn" :class="{ on: createApp === 'claude' }" @click="createApp = 'claude'">
            Claude Code
          </button>
          <button type="button" class="seg-btn" :class="{ on: createApp === 'codex' }" @click="createApp = 'codex'">
            Codex
          </button>
        </div>
        <label>工作目录</label>
        <n-select v-model:value="createWorkspace" :options="workspaceOptions" placeholder="选择工作区" />
        <label>权限</label>
        <div class="seg">
          <button v-for="o in permOptions" :key="o.value" type="button"
                  class="seg-btn" :class="{ on: createPerm === o.value }" @click="createPerm = o.value">
            {{ o.label }}
          </button>
        </div>
        <div class="form-hint">{{ permHints[createPerm] }}</div>
        <label>模型(可选)</label>
        <input
          v-model="createModel" class="model-input"
          :placeholder="createApp === 'claude' ? '如 sonnet / opus(留空用默认)' : '如 gpt-5.6-luna(留空用默认)'"
        >
        <label>思考强度</label>
        <div class="seg">
          <button v-for="o in effortOptions" :key="o.value" type="button"
                  class="seg-btn" :class="{ on: createEffort === o.value }" @click="createEffort = o.value">
            {{ o.label }}
          </button>
        </div>
      </div>
      <template #footer>
        <div class="modal-foot">
          <n-button @click="createModal = false">取消</n-button>
          <n-button type="primary" :loading="creating" @click="createSession()">创建</n-button>
        </div>
      </template>
    </n-modal>

    <!-- ---- 会话设置 ---- -->
    <!-- auto-focus 关掉:手机上弹设置面板不该顺手顶出软键盘 -->
    <n-modal v-model:show="settingsModal" preset="card" title="会话设置" class="agent-modal" :auto-focus="false">
      <div class="form">
        <label>模型</label>
        <input v-model="setModel" class="model-input" placeholder="留空用默认">
        <label>思考强度</label>
        <div class="seg">
          <button v-for="o in effortOptions" :key="o.value" type="button"
                  class="seg-btn" :class="{ on: setEffort === o.value }" @click="setEffort = o.value">
            {{ o.label }}
          </button>
        </div>
        <label>权限模式</label>
        <div class="seg">
          <button v-for="o in permOptions" :key="o.value" type="button"
                  class="seg-btn" :class="{ on: setPerm === o.value }" @click="setPerm = o.value">
            {{ o.label }}
          </button>
        </div>
        <div class="form-hint">{{ permHints[setPerm] }}</div>
        <div class="form-hint">
          {{ selected?.app === 'claude'
            ? 'claude 的模型/思考强度在进程启动时固化,运行中改设置会保存、续聊时生效。'
            : 'codex 原生支持,下一回合立即生效。' }}
        </div>
      </div>
      <template #footer>
        <div class="modal-foot">
          <n-button @click="settingsModal = false">取消</n-button>
          <n-button type="primary" :loading="setBusy" @click="applySettings({
            model: setModel.trim(),
            effort: setEffort,
            permissionMode: setPerm,
          })">应用</n-button>
        </div>
      </template>
    </n-modal>

    <!-- ---- 清理 ---- -->
    <n-modal v-model:show="cleanupModal" preset="card" title="会话清理" class="agent-modal">
      <div class="form">
        <label>清理 N 天前的已结束会话</label>
        <n-input-number v-model:value="cleanupDays" :min="1" :max="3650" style="width: 100%">
          <template #suffix>天</template>
        </n-input-number>
        <div class="form-hint">只清已结束的;运行中的会话无论多旧都不会动。清理后这些会话无法再续聊(原生转录仍在 ~/.claude/projects)。</div>
        <div class="cleanup-auto">
          <div>
            <div class="cleanup-auto-label">每日自动清理</div>
            <div class="form-hint">默认关闭;开启后每天自动删一次,删了多少可在本页看到。</div>
          </div>
          <n-switch v-model:value="cleanupAuto" />
        </div>
        <div v-if="cleanupLast" class="form-hint">
          上次自动清理:{{ cleanupLastAt ? timeAgo(cleanupLastAt) : '' }},删了 {{ cleanupLast.sessions }} 个会话、{{ cleanupLast.messages }} 条消息。
        </div>
        <div v-if="cleanupPreview" class="cleanup-preview">
          将清理 {{ cleanupPreview.sessions }} 个会话、{{ cleanupPreview.messages }} 条消息
        </div>
      </div>
      <template #footer>
        <div class="modal-foot">
          <n-button :loading="cleanupBusy === 'save'" @click="saveCleanupSettings">保存设置</n-button>
          <n-button secondary :loading="cleanupBusy === 'preview'" @click="previewCleanup">预览</n-button>
          <n-popconfirm @positive-click="runCleanup">
            <template #trigger>
              <n-button type="error" :loading="cleanupBusy === 'run'">立即清理</n-button>
            </template>
            删除 {{ cleanupDays }} 天前的已结束会话?消息历史一并删除,无法恢复。
          </n-popconfirm>
        </div>
      </template>
    </n-modal>
  </div>
</template>

<!-- 气泡里的 markdown(hljs 令牌、md-pre)经 v-html 注入,不带 scope 属性,
     样式必须放非 scoped 块 —— 老坑,第三次遇到它了。 -->
<style>
.agent-md-body { font-size: 14px; line-height: 1.65; overflow-wrap: anywhere; }
.agent-md-body > :first-child { margin-top: 0; }
.agent-md-body > :last-child { margin-bottom: 0; }
.agent-md-body p, .agent-md-body ul, .agent-md-body ol,
.agent-md-body blockquote, .agent-md-body table { margin: 0.6em 0; }
.agent-md-body ul, .agent-md-body ol { padding-left: 1.4em; }
.agent-md-body h1, .agent-md-body h2, .agent-md-body h3 { margin: 1em 0 0.4em; line-height: 1.3; font-size: 1.05em; }
.agent-md-body a { color: var(--lr-accent); }
.agent-md-body code {
  font-family: ui-monospace, monospace; font-size: 0.88em;
  padding: 0.12em 0.3em; border-radius: 3px; background: rgba(127, 127, 127, .16);
}
.agent-md-body pre.md-pre {
  margin: 0.6em 0; padding: 8px 10px; border-radius: 6px;
  background: rgba(127, 127, 127, .12); overflow: auto; line-height: 1.5;
}
.agent-md-body pre.md-pre code {
  padding: 0; background: none; font-size: 12px; white-space: pre; overflow-wrap: normal;
  /* 显式等宽栈:框线字符(─│├…)在缺省回退字体里宽度不稳;
     连字必须关 —— 有的等宽字体把 ├── 合成一个字形,ASCII 图直接错位。 */
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, "DejaVu Sans Mono", monospace;
  font-variant-ligatures: none; font-feature-settings: "liga" 0, "calt" 0;
}
.agent-md-body table { border-collapse: collapse; }
.agent-md-body th, .agent-md-body td { padding: 4px 8px; border: 1px solid rgba(127, 127, 127, .28); }
.agent-md-body blockquote {
  margin: 0.6em 0; padding-left: 10px; border-left: 3px solid rgba(127, 127, 127, .35);
  color: var(--lr-fg-muted);
}
/* hljs 令牌色,同 FilesFileView 的 .ce-body 一套 */
.agent-md-body :where(.hljs) { color: var(--lr-fg); }
.agent-md-body .hljs-attr, .agent-md-body .hljs-name { color: #c26; }
.agent-md-body .hljs-comment, .agent-md-body .hljs-quote { color: var(--lr-fg-muted); font-style: italic; }
.agent-md-body .hljs-keyword, .agent-md-body .hljs-meta { color: #a626a4; }
.agent-md-body .hljs-string, .agent-md-body .hljs-regexp, .agent-md-body .hljs-addition { color: #3d8c3e; }
.agent-md-body .hljs-number, .agent-md-body .hljs-literal, .agent-md-body .hljs-deletion { color: #b76b01; }
.agent-md-body .hljs-title, .agent-md-body .hljs-built_in { color: #286983; }
.agent-md-body .hljs-type, .agent-md-body .hljs-class { color: #4078f2; }
</style>

<style scoped>
.agent-page { display: flex; gap: 14px; }

/* ---- 列表列 ---- */
.list-col { flex: 1 1 320px; min-width: 0; }
.agent-head { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
.agent-head h2 { margin: 0; font-size: 20px; }
.agent-head-ops { display: flex; gap: 8px; }
.agent-empty { padding: 48px 0; }
.sess-list { display: flex; flex-direction: column; gap: 12px; margin-top: 12px; }
/* 工作区分组:整组包在一个容器里,会话是容器内的行(不再各自成卡,免得框套框) */
.sess-group {
  display: flex; flex-direction: column;
  background: var(--lr-bg-elevated);
  border: 1px solid rgba(127, 127, 127, .14);
  border-radius: var(--lr-radius);
  overflow: hidden;
}
.sess-group-head {
  display: flex; align-items: center; gap: 6px;
  width: 100%; min-height: 34px; padding: 0 10px;
  appearance: none; border: 0; background: transparent;
  font: inherit; text-align: left; cursor: pointer;
  color: var(--lr-fg-muted);
  -webkit-tap-highlight-color: transparent;
}
/* 展开时组头下面垫一条发丝线;整组只剩组头(收起)时不垫 */
.sess-group-head:not(:last-child) { border-bottom: 1px solid rgba(127, 127, 127, .1); }
.sess-chevron { flex: none; font-size: 10px; transition: transform .15s ease; }
.sess-chevron.open { transform: rotate(90deg); }
.sess-group-path {
  flex: 1; min-width: 0;
  font-size: 11px; font-family: ui-monospace, monospace;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.sess-count {
  flex: none; padding: 0 6px;
  font-size: 11px; line-height: 18px; border-radius: 999px;
  background: rgba(127, 127, 127, .16);
}
/* 状态圆点:与聊天头同一套颜色/脉动语义 */
.sess-dot { flex: none; width: 8px; height: 8px; border-radius: 50%; background: var(--lr-ok); }
.sess-dot.running { background: var(--lr-warn); animation: dot-pulse 1.2s ease-in-out infinite; }
.sess-dot.dead { background: var(--lr-fg-muted); }
.sess-card { display: flex; align-items: stretch; gap: 4px; }
.sess-card + .sess-card { border-top: 1px solid rgba(127, 127, 127, .1); }
.sess-card.on { background: rgba(127, 127, 127, .1); box-shadow: inset 2px 0 0 var(--lr-accent); }
.sess-card.checked { background: rgba(127, 127, 127, .08); }
/* 多选模式的复选框;行本身也可点(sess-main 共用 toggleSelect) */
.sess-check {
  flex: none; display: flex; align-items: center;
  padding: 0 0 0 10px;
  appearance: none; border: 0; background: transparent; cursor: pointer;
  -webkit-tap-highlight-color: transparent;
}
.sess-check-box {
  width: 20px; height: 20px; border-radius: 6px;
  border: 1.5px solid rgba(127, 127, 127, .45);
  display: flex; align-items: center; justify-content: center;
  font-size: 13px; color: #fff;
}
.sess-card.checked .sess-check-box {
  background: var(--lr-accent); border-color: var(--lr-accent);
}
.sess-main {
  flex: 1; min-width: 0; padding: 10px 12px;
  appearance: none; border: 0; background: transparent;
  font: inherit; color: var(--lr-fg); text-align: left; cursor: pointer;
  -webkit-tap-highlight-color: transparent;
}
.sess-title-row { display: flex; align-items: center; gap: 8px; }
.sess-title {
  flex: 1; min-width: 0; font-weight: 600; font-size: 14px;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.sess-meta {
  display: flex; align-items: baseline; gap: 8px; margin-top: 4px;
  font-size: 12px; color: var(--lr-fg-muted);
}
.sess-app { flex: none; }
.sess-time { flex: none; }
.sess-ops { display: flex; align-items: center; padding-right: 10px; }

/* ---- 聊天列 ---- */
.chat-col {
  flex: 2 1 480px; min-width: 0;
  display: flex; flex-direction: column;
  height: calc(100dvh - var(--lr-page-pad) - var(--lr-page-pad-bottom));
}
.chat-head {
  display: flex; align-items: center; gap: 10px;
  padding-bottom: 10px; border-bottom: 1px solid rgba(127, 127, 127, .18);
}
.chat-back {
  appearance: none; border: 0; background: transparent; cursor: pointer;
  font-size: 26px; line-height: 1; color: var(--lr-fg-muted);
  min-width: 36px; min-height: var(--lr-touch);
  -webkit-tap-highlight-color: transparent;
}
.chat-title { min-width: 0; flex: 1; }
.chat-name {
  font-weight: 600; font-size: 15px;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.chat-sub {
  display: flex; align-items: center; gap: 6px;
  font-size: 12px; color: var(--lr-fg-muted); margin-top: 2px;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
/* 回合状态/模型的展示挪到输入框上方 StatusBar,这两个样式不再需要 */
.chat-settings {
  flex: none; min-width: 40px; min-height: var(--lr-touch);
  appearance: none; border: 0; background: transparent; cursor: pointer;
  font-size: 17px; color: var(--lr-fg-muted);
  -webkit-tap-highlight-color: transparent;
}

.chat-timeline {
  flex: 1; min-height: 0; overflow-y: auto;
  padding: 12px 2px;
  display: flex; flex-direction: column; gap: 10px;
}
.chat-empty { padding: 60px 0; }
.load-older { display: flex; justify-content: center; }

/* 排队气泡:右对齐同用户消息,但降透明度 + 可撤回 */
.queued-bubble { opacity: .75; display: flex; flex-direction: column; gap: 6px; min-width: 0; }
.queued-text { white-space: pre-wrap; overflow-wrap: anywhere; }
.queued-ops { display: flex; align-items: center; justify-content: flex-end; gap: 10px; }
.queued-tag { font-size: 11px; color: rgba(255, 255, 255, .85); }
.queued-cancel {
  appearance: none; border: 0; background: transparent; cursor: pointer;
  font: inherit; font-size: 12px; color: #fff;
  min-height: 28px; padding: 0 4px;
  text-decoration: underline;
  -webkit-tap-highlight-color: transparent;
}

/* 乐观气泡:套用户气泡底色,sending 底部一行小字转圈;failed 白底红字标 ⚠ */
.pending-bubble { display: flex; flex-direction: column; gap: 6px; min-width: 0; }
.pending-text { white-space: pre-wrap; overflow-wrap: anywhere; }
.pending-ops { display: flex; align-items: center; justify-content: flex-end; gap: 10px; }
.pending-tag {
  display: inline-flex; align-items: center; gap: 5px;
  font-size: 11px; color: rgba(255, 255, 255, .85);
}
.pending-bubble.failed { background: #fee2e2; } /* 浅红底,脱掉主色一眼可辨 */
/* .bubble.user 的白字在浅红底上不可读,更高优先级把正文压回深色 */
.bubble.user.pending-bubble.failed { color: #991b1b; }
.pending-tag.failed { color: var(--lr-danger); font-weight: 600; }
.pending-spinner {
  flex: none; width: 12px; height: 12px; border-radius: 50%;
  border: 2px solid rgba(255, 255, 255, .4); border-top-color: #fff;
  animation: pending-spin .8s linear infinite;
}
@keyframes pending-spin { to { transform: rotate(360deg); } }
.pending-retry, .pending-drop {
  appearance: none; border: 0; background: transparent; cursor: pointer;
  font: inherit; font-size: 12px; color: var(--lr-danger);
  min-height: 28px; padding: 0 4px;
  text-decoration: underline;
  -webkit-tap-highlight-color: transparent;
}
.pending-drop { color: #6b7280; } /* 浅红底上取固定灰,不依赖主题变量对比度 */
.chat-row { display: flex; min-width: 0; }
/* flex 子项默认 min-width:auto 会被 nowrap 内容顶宽:长 bash 命令把卡片
   撑出屏、内部 overflow-x: auto 失效的根源。归零后卡片宽度由容器决定,
   卡内的横向滚动/换行才生效。 */
.chat-row > * { min-width: 0; }
.chat-row.user { justify-content: flex-end; }
.chat-row.tool, .chat-row.toolgroup, .chat-row.approval, .chat-row.reasoning, .chat-row.error { justify-content: stretch; }

/* 顶部任务面板(hapi SessionStatusPanel 同款):钉在 header 下的折叠面板,
   永远显示最新任务快照;Task 套件/TodoWrite 的事件都汇进这里 */
.task-panel {
  flex: none; margin: 10px 12px 0;
  border: 1px solid rgba(127, 127, 127, .18);
  border-radius: var(--lr-radius);
  background: var(--lr-bg-elevated);
}
.task-panel summary {
  display: flex; align-items: center; gap: 8px;
  min-height: 36px; padding: 4px 10px;
  cursor: pointer; list-style: none; user-select: none;
  font-size: 12px; font-weight: 600; color: var(--lr-fg);
  -webkit-tap-highlight-color: transparent;
}
.task-panel summary::-webkit-details-marker { display: none; }
.task-panel-title { flex: none; }
.task-panel-count { flex: none; font-size: 11px; font-weight: 400; color: var(--lr-fg-muted); }
.task-panel-chev { margin-left: auto; font-size: 10px; color: var(--lr-fg-muted); transition: transform .15s ease; }
.task-panel[open] .task-panel-chev { transform: rotate(180deg); }
.task-panel-body {
  display: flex; flex-direction: column; gap: 4px;
  padding: 6px 10px 10px;
  border-top: 1px solid rgba(127, 127, 127, .14);
  max-height: 40vh; overflow-y: auto;
}
.task-item { display: flex; align-items: baseline; gap: 6px; font-size: 13px; line-height: 1.5; }
.task-mark { flex: none; font-size: 12px; }
.task-item.completed .task-mark,
.task-item.completed .task-text { color: var(--lr-ok); text-decoration: line-through; }
.task-item.in_progress .task-mark,
.task-item.in_progress .task-text { color: var(--lr-accent); }
.task-item.pending .task-mark,
.task-item.pending .task-text { color: var(--lr-fg-muted); }
.task-text { min-width: 0; overflow-wrap: anywhere; }

.bubble {
  max-width: 86%;
  padding: 8px 12px; border-radius: var(--lr-radius);
  font-size: 14px; line-height: 1.6;
  overflow-wrap: anywhere;
}
.bubble.user {
  background: var(--lr-accent); color: #fff;
  border-bottom-right-radius: 4px;
  white-space: pre-wrap;
}
/* 普通聊天直接显示,不包卡片(hapi 风格):只有正文排版,没有气泡底色 */
.agent-plain { min-width: 0; color: var(--lr-fg); }
.bubble.error {
  background: rgba(220, 38, 38, .1);
  border: 1px solid rgba(220, 38, 38, .4);
  color: var(--lr-danger);
  font-size: 13px;
  white-space: pre-wrap;
}

/* 系统提示线:居中淡色一行(上下文压缩等),不占聊天气泡 */
.sysline {
  margin: 4px auto;
  width: fit-content;
  max-width: 92%;
  padding: 2px 8px;
  border-radius: var(--lr-radius);
  background: rgba(127, 127, 127, .08);
  color: var(--lr-fg-muted);
  font-size: 12px;
  text-align: center;
}
.sysline-failed {
  background: rgba(220, 38, 38, .08);
  color: var(--lr-danger);
}
/* 后台通知卡(task_notification):工具卡同款底,头部标签行 + markdown 正文 +
   Monitor 的等宽事件行;失败态描红。 */
.notify-card {
  align-self: stretch;
  border: 1px solid rgba(127, 127, 127, .18);
  border-radius: var(--lr-radius);
  background: var(--lr-bg-elevated);
  overflow: hidden;
}
.notify-failed { border-color: rgba(220, 38, 38, .45); }
.notify-head {
  display: flex; align-items: center; gap: 8px;
  min-height: 38px; padding: 6px 10px;
  font-size: 12px; font-weight: 600; color: var(--lr-fg);
}
.notify-failed .notify-head { color: var(--lr-danger); }
.notify-body { padding: 0 10px 8px; font-size: 13px; }
.notify-event {
  margin: 0; padding: 6px 10px 8px;
  border-top: 1px solid rgba(127, 127, 127, .14);
  font-family: ui-monospace, monospace; font-size: 12px;
  color: var(--lr-fg-muted);
  white-space: pre-wrap; overflow-wrap: anywhere;
  max-height: 30vh; overflow-y: auto;
}
.reasoning {
  align-self: stretch;
  font-size: 12px; color: var(--lr-fg-muted);
  border-left: 2px solid rgba(127, 127, 127, .22);
  padding-left: 10px;
}
.reasoning summary {
  cursor: pointer; min-height: 28px; display: flex; align-items: center; gap: 6px;
  list-style: none; user-select: none; -webkit-tap-highlight-color: transparent;
}
.reasoning summary::-webkit-details-marker { display: none; }
.reasoning summary::before {
  content: '▸'; font-size: 10px; line-height: 1;
  transition: transform .15s ease; opacity: .7;
}
.reasoning[open] summary::before { transform: rotate(90deg); }
.reasoning-body {
  white-space: pre-wrap; overflow-wrap: anywhere;
  max-height: 220px; overflow-y: auto;
  padding: 2px 0 6px; font-style: italic; opacity: .85;
}

/* ---- 表单 ---- */
.form { display: flex; flex-direction: column; gap: 10px; }
.form label { font-size: 12px; color: var(--lr-fg-muted); }
.form-static { font-size: 14px; }
.model-input {
  height: var(--lr-touch); padding: 0 12px;
  border: 1px solid rgba(127, 127, 127, .3);
  border-radius: var(--lr-radius);
  background: var(--lr-bg-elevated); color: var(--lr-fg);
  font: inherit; font-size: 14px; outline: none;
}
.model-input:focus { border-color: var(--lr-accent); }
.form-hint { font-size: 12px; color: var(--lr-fg-muted); line-height: 1.5; }
.seg { display: inline-flex; gap: 2px; padding: 3px; border-radius: var(--lr-radius); background: rgba(127, 127, 127, .12); }
.seg-btn {
  appearance: none; border: 0; background: transparent; cursor: pointer;
  font: inherit; font-size: 13px; font-weight: 600; color: var(--lr-fg-muted);
  height: 32px; padding: 0 16px; border-radius: calc(var(--lr-radius) - 4px);
}
.seg-btn.on { background: var(--lr-bg-elevated); color: var(--lr-accent); box-shadow: 0 1px 3px rgba(0, 0, 0, .14); }
.cleanup-auto { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.cleanup-auto-label { font-size: 14px; }
.cleanup-preview {
  padding: 8px 12px; border-radius: var(--lr-radius);
  background: rgba(217, 119, 6, .12); color: var(--lr-warn);
  font-size: 13px;
}
.modal-foot { display: flex; justify-content: flex-end; gap: 8px; flex-wrap: wrap; }
.agent-modal { width: min(520px, calc(100vw - 24px)); }

/* ---- 移动端:列表与聊天互斥,打开聊天即全屏 ---- */
@media (max-width: 767px) {
  .agent-page.chat-open .list-col { display: none; }
  .agent-page:not(.chat-open) .chat-col { display: none; }
  .list-col { flex: none; width: 100%; }
  /* 聊天打开 = 全屏覆盖层。不用 100dvh 算高度:手机上 dvh 和真实可见高度
     常差一截(地址栏/WebView),之前按 dvh 算就把输入框顶到屏幕外了。
     fixed + inset:0 的高度就是真实视口,输入框必在屏幕下缘内。 */
  .agent-page.chat-open {
    position: fixed; inset: 0; z-index: 90;
    padding: var(--lr-page-pad) var(--lr-page-pad) 0;
    background: var(--lr-bg);
    align-items: stretch;
  }
  .agent-page.chat-open .chat-col {
    flex: 1; min-width: 0;
    height: auto; /* 覆盖基础规则的 dvh 计算,跟随覆盖层拉伸 */
    padding-bottom: env(safe-area-inset-bottom);
  }
  .chat-back { display: flex; }
}
/* 桌面端两栏常驻,返回按钮没存在必要 */
@media (min-width: 768px) {
  .chat-back { display: none; }
}
</style>
