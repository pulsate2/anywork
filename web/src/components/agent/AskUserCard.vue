<script setup lang="ts">
// 提问卡片:agent 经控制协议向用户提问(claude AskUserQuestion / codex
// request_user_input 归一形状)。多个问题不平铺,跟 hapi 一样一次只显示
// 一题、[n/m] 计数、上一题/下一题左右切换:翻页不拦,校验只在提交时做 ——
// 空答案会让 claude 产出空结果并锁死回合(hapi 踩过),提交时跳回第一道
// 没答的题兜底。Options 为空 = 自由文本题,给输入框。result 态只展示
// 结论,不再给按钮。
import { computed, reactive, ref, watch } from 'vue'
import { NButton, NInput } from 'naive-ui'
import type { AgentAskUser, AgentAskUserResult } from '@/api/client'
import { renderPreviewMarkdown } from '@/utils/markdown'

const props = defineProps<{
  ask: AgentAskUser
  result?: AgentAskUserResult | null
  busy?: boolean
}>()

const emit = defineEmits<{ (e: 'answer', answers: Record<string, string[]> | null): void }>()

const done = computed(() => !!props.result)

// 当前题下标;questions 变化(新提问)时回第一题。
const step = ref(0)
watch(() => props.ask.reqId, () => { step.value = 0 })

// 本地选择状态:问题 id → 选项 label 集合(多选)/自由文本。
// otherSel/otherTexts 是「其他」自填项(hapi 同款:选项之外自己写答案,
// 单选时选它清掉已选选项,多选时与选项并存)。
const picked = reactive<Record<string, string[]>>({})
const texts = reactive<Record<string, string>>({})
const otherSel = reactive<Record<string, boolean>>({})
const otherTexts = reactive<Record<string, string>>({})

const total = computed(() => Math.max(1, props.ask.questions.length))
// questions 比 step 短(异常数据)时夹住,渲染层不炸。
const cur = computed(() => props.ask.questions[Math.min(step.value, total.value - 1)])

// 一题的答案:选项选中项 + 「其他」的文本(选了且填了字);没答返回 null。
function answerOf(q: (typeof props.ask.questions)[number]): string[] | null {
  if (q.options?.length) {
    const out = [...(picked[q.id] ?? [])]
    const other = (otherTexts[q.id] ?? '').trim()
    if (otherSel[q.id] && other) out.push(other)
    return out.length ? out : null
  }
  const t = (texts[q.id] ?? '').trim()
  return t ? [t] : null
}

// 必答题没答完的下标列表(提交校验用;非必答跳过)。
const missing = computed(() =>
  props.ask.questions
    .map((q, i) => (q.required && !answerOf(q) ? i : -1))
    .filter((i) => i >= 0),
)

function toggle(q: (typeof props.ask.questions)[number], label: string) {
  if (done.value) return
  const curSel = picked[q.id] ?? []
  if (q.multi) {
    picked[q.id] = curSel.includes(label) ? curSel.filter((x) => x !== label) : [...curSel, label]
  } else {
    picked[q.id] = curSel.includes(label) && !q.required ? [] : [label]
    otherSel[q.id] = false // 单选互斥:选了选项就退出「其他」
  }
}

// 「其他」:单选清掉已选选项并独占;多选只是切换。
function toggleOther(q: (typeof props.ask.questions)[number]) {
  if (done.value) return
  if (!q.multi) picked[q.id] = []
  otherSel[q.id] = !otherSel[q.id]
}

// 单选题的预览(官方客户端同款语义):仅单选、选中的选项带 preview 时显示;
// 多选题官方忽略 preview。「其他」没有预览。裸 ASCII 框线自动按代码块渲染
// (renderPreviewMarkdown,否则 markdown 会把框图并成段落)。
const previewHtml = computed<string | null>(() => {
  const q = cur.value
  if (!q || q.multi || done.value) return null
  const sel = picked[q.id]?.[0]
  if (!sel) return null
  const opt = q.options?.find((o) => o.label === sel)
  return opt?.preview ? renderPreviewMarkdown(opt.preview) : null
})

// 自填框里打字就自动勾上「其他」。
function onOtherInput(q: (typeof props.ask.questions)[number], v: string) {
  otherTexts[q.id] = v
  if (v.trim()) otherSel[q.id] = true
}

// 下一题:随便翻,不拦(hapi 是就地拦,这里按用户偏好放开,漏答提交时再兜)。
const stepError = ref('')
function next() {
  stepError.value = ''
  step.value = Math.min(step.value + 1, total.value - 1)
}

function submit() {
  // 全量校验:第一道没答的题跳回去就地提示。
  const first = missing.value[0]
  if (first !== undefined) {
    step.value = first
    stepError.value = '这题还没回答'
    return
  }
  stepError.value = ''
  const answers: Record<string, string[]> = {}
  for (const q of props.ask.questions) {
    const a = answerOf(q)
    if (a) answers[q.id] = a
  }
  emit('answer', answers)
}

// 已答态每题结论:多选拼顿号、自由文本原样。
function resultText(q: (typeof props.ask.questions)[number]): string {
  return props.result?.answers?.[q.id]?.join('、') ?? ''
}

// 历史回放进入已答态时清掉本地交互,防止半选状态残留。
watch(done, (d) => {
  if (d) {
    for (const k of Object.keys(picked)) delete picked[k]
    for (const k of Object.keys(texts)) delete texts[k]
    for (const k of Object.keys(otherSel)) delete otherSel[k]
    for (const k of Object.keys(otherTexts)) delete otherTexts[k]
  }
}, { immediate: true })
</script>

<template>
  <div class="ask" :class="{ done }">
    <div class="ask-top">
      <span class="ask-title">Agent 提问</span>
      <span v-if="!done && total > 1" class="ask-count">[{{ Math.min(step, total - 1) + 1 }}/{{ total }}]</span>
      <span v-if="done" class="ask-verdict" :class="result?.cancelled ? 'no' : 'ok'">
        {{ result?.cancelled ? '已取消' : '已回答' }}
      </span>
      <span v-else class="ask-wait">等待你的回答</span>
    </div>

    <!-- 已答态:历史回放,各题结论一列(不需要再分步) -->
    <template v-if="done">
      <div v-for="q in ask.questions" :key="q.id" class="ask-done">
        <span v-if="q.header" class="ask-q-header">{{ q.header }}</span>
        <span class="ask-done-q">{{ q.question }}</span>
        <span class="ask-done-a">{{ resultText(q) || '(未回答)' }}</span>
      </div>
    </template>

    <!-- 交互态:一次一题,左右切换 -->
    <template v-else>
      <div v-if="cur" class="ask-q">
        <div class="ask-q-title">
          <span v-if="cur.header" class="ask-q-header">{{ cur.header }}</span>
          <span class="ask-q-text">{{ cur.question }}</span>
          <span v-if="cur.multi" class="ask-q-tag">多选</span>
        </div>

        <template v-if="cur.options?.length">
          <div class="ask-opts">
            <button
              v-for="o in cur.options" :key="o.label" type="button"
              class="ask-opt" :class="{ on: picked[cur.id]?.includes(o.label) }"
              @click="toggle(cur, o.label)"
            >
              <!-- 单选/多选控件(hapi 同款:圆点 / 对勾) -->
              <span class="ask-opt-control" :class="cur.multi ? 'check' : 'radio'">
                <svg v-if="cur.multi && picked[cur.id]?.includes(o.label)" viewBox="0 0 16 16" fill="none">
                  <path d="M3.5 8.2l2.8 2.8 6.2-6" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" />
                </svg>
                <span v-else-if="!cur.multi && picked[cur.id]?.includes(o.label)" class="dot" />
              </span>
              <span class="ask-opt-main">
                <span class="ask-opt-label">{{ o.label }}</span>
                <span v-if="o.description" class="ask-opt-desc">{{ o.description }}</span>
              </span>
            </button>
            <!-- 「其他」自填项(hapi 同款):选中后展开输入框 -->
            <button type="button" class="ask-opt" :class="{ on: otherSel[cur.id] }" @click="toggleOther(cur)">
              <span class="ask-opt-control" :class="cur.multi ? 'check' : 'radio'">
                <svg v-if="cur.multi && otherSel[cur.id]" viewBox="0 0 16 16" fill="none">
                  <path d="M3.5 8.2l2.8 2.8 6.2-6" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" />
                </svg>
                <span v-else-if="!cur.multi && otherSel[cur.id]" class="dot" />
              </span>
              <span class="ask-opt-main">
                <span class="ask-opt-label">其他</span>
                <span class="ask-opt-desc">输入您自己的答案</span>
              </span>
            </button>
          </div>
          <n-input
            v-if="otherSel[cur.id]" :value="otherTexts[cur.id] || ''" size="small"
            class="ask-other-input" placeholder="或输入您自己的答案…"
            @update:value="(v: string) => onOtherInput(cur, v)"
          />
          <!-- 单选题预览面板:选中带 preview 的选项时出现,markdown 渲染
               (代码块/ASCII 等宽对齐靠 md-pre 的 white-space: pre)。 -->
          <div v-if="previewHtml" class="ask-preview agent-md-body" v-html="previewHtml" />
        </template>
        <n-input
          v-else v-model:value="texts[cur.id]" size="small"
          :placeholder="cur.placeholder || '输入回答'"
          @keydown.enter.prevent="total > 1 ? next() : submit()"
        />
      </div>

      <div v-if="stepError" class="ask-err">{{ stepError }}</div>

      <div class="ask-ops">
        <n-button size="small" :loading="busy" @click="emit('answer', null)">取消</n-button>
        <n-button v-if="total > 1 && step > 0" size="small" @click="stepError = ''; step--">上一题</n-button>
        <n-button v-if="step < total - 1" size="small" type="primary" secondary @click="next">下一题</n-button>
        <n-button v-else size="small" type="primary" :loading="busy" @click="submit">提交回答</n-button>
      </div>
    </template>
  </div>
</template>

<style scoped>
.ask {
  border: 1px solid var(--lr-warn);
  border-radius: var(--lr-radius);
  background: var(--lr-bg-elevated);
  padding: 10px 12px;
  max-width: 100%;
}
.ask.done { border-color: rgba(127, 127, 127, .3); }
.ask-top { display: flex; align-items: center; gap: 8px; }
.ask-title { font-weight: 600; font-size: 13px; }
.ask-count {
  font-family: ui-monospace, monospace; font-size: 11px;
  padding: 0 6px; border-radius: 999px;
  background: rgba(127, 127, 127, .12); color: var(--lr-fg-muted);
}
.ask-wait { font-size: 12px; color: var(--lr-warn); margin-left: auto; }
.ask-verdict { font-size: 12px; margin-left: auto; }
.ask-verdict.ok { color: var(--lr-ok); }
.ask-verdict.no { color: var(--lr-danger); }

/* 已答态:每题一行,问题淡色 + 结论加粗 */
.ask-done { display: flex; align-items: baseline; gap: 6px; flex-wrap: wrap; margin-top: 8px; }
.ask-done-a { font-size: 13px; font-weight: 500; }

.ask-q { margin-top: 10px; }
.ask-q-title { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }
.ask-q-header {
  font-size: 11px; font-weight: 600; text-transform: uppercase; letter-spacing: .04em;
  color: var(--lr-accent, #63e2b7);
}
.ask-q-text { font-size: 13px; }
.ask-q-tag {
  font-size: 11px; padding: 0 6px; border-radius: 4px;
  background: rgba(127, 127, 127, .15); color: var(--lr-fg-muted);
}

/* 选项按钮:整块可点(手机友好),左侧选控(圆点/对勾)+ 右侧文案 */
.ask-opts { display: flex; flex-direction: column; gap: 6px; margin-top: 8px; }
.ask-opt {
  display: flex; align-items: center; gap: 10px;
  text-align: left; border: 1.5px solid transparent; border-radius: 10px;
  background: rgba(127, 127, 127, .06); padding: 8px 10px; cursor: pointer;
  color: inherit; font: inherit; min-height: 44px;
}
.ask-opt.on { border-color: var(--lr-accent, #63e2b7); background: rgba(99, 226, 183, .08); }
.ask-opt-control {
  flex: none; width: 16px; height: 16px;
  display: inline-flex; align-items: center; justify-content: center;
  border: 1.5px solid var(--lr-fg-muted); background: var(--lr-bg-elevated);
}
.ask-opt-control.radio { border-radius: 50%; }
.ask-opt-control.check { border-radius: 4px; }
.ask-opt.on .ask-opt-control { border-color: var(--lr-accent, #63e2b7); color: var(--lr-accent, #63e2b7); }
.ask-opt-control .dot { width: 8px; height: 8px; border-radius: 50%; background: var(--lr-accent, #63e2b7); }
.ask-opt-control svg { width: 12px; height: 12px; }
.ask-opt-main { min-width: 0; flex: 1; }
.ask-opt-label { display: block; font-size: 13px; font-weight: 500; }
.ask-opt-desc { display: block; font-size: 12px; color: var(--lr-fg-muted); margin-top: 2px; }
/* 「其他」自填输入框:跟在选项列表后面 */
.ask-other-input { margin-top: 6px; }
/* 预览面板:选中选项的 markdown 预览;可滚,长内容不撑爆时间线。
   内容经 v-html 注入,样式走全局 .agent-md-body(hljs 同款,老坑)。 */
.ask-preview {
  margin-top: 8px; padding: 8px 10px;
  border: 1px solid rgba(127, 127, 127, .18); border-radius: 8px;
  background: rgba(127, 127, 127, .06);
  font-size: 13px; max-height: 40vh; overflow: auto;
}

.ask-err { margin-top: 8px; font-size: 12px; color: var(--lr-danger); }

/* 取消在左,上一题/下一题/提交在右 */
.ask-ops { display: flex; justify-content: flex-end; gap: 8px; margin-top: 10px; }
.ask-ops > :first-child { margin-right: auto; }
</style>
