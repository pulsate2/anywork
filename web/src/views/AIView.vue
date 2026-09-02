<script setup lang="ts">
// AI 配置切换:一条记录 = 一份能直接落盘的供应商配置。切过去就是把它写回
// ~/.claude/settings.json 或 ~/.codex/config.toml + auth.json,CLI 下次启动自己读到。
import { computed, onMounted, ref, watch } from 'vue'
import {
  NButton, NEmpty, NInput, NModal, NPopconfirm, NSelect, NSpin, NTag, useMessage,
} from 'naive-ui'
import { api, type AiApp, type AiProvider } from '@/api/client'
import { API_KEY_PLACEHOLDER, categoryLabel, fillKey, presetsFor } from '@/config/aiPresets'

// 两套 CLI 各有各的配置落点,切换条的两个档位就是它们。
const APPS: { value: AiApp; label: string }[] = [
  { value: 'claude', label: 'Claude Code' },
  { value: 'codex', label: 'Codex' },
]

const message = useMessage()
const app = ref<AiApp>('claude')
const providers = ref<AiProvider[]>([])
const loading = ref(false)
const busy = ref('')
const importInput = ref<HTMLInputElement | null>(null)

const editModal = ref(false)
const editId = ref('')
const editName = ref('')
const editCategory = ref('custom')
const editSite = ref('')
const presetName = ref<string | null>(null)
const apiKey = ref('')
const configText = ref('')
const authText = ref('')
const saving = ref(false)

const keyHint = `保存时替换配置里的 ${API_KEY_PLACEHOLDER}`

const activeApp = computed(() => APPS.find((a) => a.value === app.value) ?? APPS[0])

const presetOptions = computed(() =>
  presetsFor(app.value).map((p) => ({
    label: `${p.name} · ${categoryLabel(p.category)}`,
    value: p.name,
  })),
)

// 正文里还留着占位符才需要填 key;编辑已有配置时里面是真值,这个输入框就不出现了。
const needsKey = computed(
  () => configText.value.includes(API_KEY_PLACEHOLDER) || authText.value.includes(API_KEY_PLACEHOLDER),
)

// 当前生效的排最前,切换时不用满屏找。
const sorted = computed(() =>
  providers.value.slice().sort((a, b) => Number(b.isCurrent) - Number(a.isCurrent)),
)
async function load() {
  loading.value = true
  try {
    const r = await api.aiProviders(app.value)
    providers.value = r.providers || []
  } catch (e: any) {
    message.error(e?.message || '加载失败')
  } finally {
    loading.value = false
  }
}

// baseUrlOf 卡片上那行摘要。取不到就是官方那档 —— 官方本来就不配 base_url。
function baseUrlOf(p: AiProvider): string {
  const cfg = p.config as any
  if (p.app === 'claude') return cfg?.env?.ANTHROPIC_BASE_URL || '官方直连'
  const m = String(cfg?.config || '').match(/^[ \t]*base_url[ \t]*=[ \t]*"([^"]*)"/m)
  return m ? m[1] : '官方登录'
}

// modelOf 钉死的模型名,没钉就跟着 CLI 默认走,那行直接不显示。
// codex 的 model_provider / model_reasoning_effort 不会误命中:model 后面紧跟的是下划线。
function modelOf(p: AiProvider): string {
  const cfg = p.config as any
  if (p.app === 'claude') return String(cfg?.env?.ANTHROPIC_MODEL || '')
  const m = String(cfg?.config || '').match(/^[ \t]*model[ \t]*=[ \t]*"([^"]*)"/m)
  return m ? m[1] : ''
}

function resetForm() {
  editId.value = ''
  editName.value = ''
  editCategory.value = 'custom'
  editSite.value = ''
  presetName.value = null
  apiKey.value = ''
  configText.value = app.value === 'claude' ? JSON.stringify({ env: {} }, null, 2) : ''
  authText.value = app.value === 'claude' ? '' : '{}'
}

function fillForm(p: AiProvider) {
  const cfg = p.config as any
  if (p.app === 'claude') {
    configText.value = JSON.stringify(cfg ?? {}, null, 2)
    return
  }
  configText.value = String(cfg?.config || '')
  authText.value = JSON.stringify(cfg?.auth ?? {}, null, 2)
}
function openCreate() {
  resetForm()
  editModal.value = true
}

function openEdit(p: AiProvider) {
  resetForm()
  editId.value = p.id
  editName.value = p.name
  editCategory.value = p.category
  editSite.value = p.websiteUrl
  fillForm(p)
  editModal.value = true
}

// openCopy 拿现成的改一版:清掉 id 就变成新增,名字先给个不撞的。
function openCopy(p: AiProvider) {
  openEdit(p)
  editId.value = ''
  editName.value = `${p.name} 副本`
}

function applyPreset(name: string | null) {
  const p = presetsFor(app.value).find((x) => x.name === name)
  if (!p) return
  editName.value = p.name
  editCategory.value = p.category
  editSite.value = p.websiteUrl
  configText.value = p.config
  authText.value = p.auth ?? ''
}

// buildConfig 把编辑框拼成后端要的载荷,API Key 只在这里替换一次。
function buildConfig(): unknown {
  const conf = fillKey(configText.value, apiKey.value)
  if (app.value === 'claude') return JSON.parse(conf.trim() || '{}')
  const auth = fillKey(authText.value, apiKey.value).trim()
  return { auth: auth ? JSON.parse(auth) : {}, config: conf }
}
async function save() {
  if (!editName.value.trim()) {
    message.warning('请填配置名')
    return
  }
  let config: unknown
  try {
    config = buildConfig()
  } catch (e: any) {
    message.error(`JSON 格式不对: ${e?.message || e}`)
    return
  }
  saving.value = true
  try {
    const body = {
      app: app.value,
      name: editName.value.trim(),
      category: editCategory.value,
      websiteUrl: editSite.value,
      config,
    }
    if (editId.value) await api.aiProviderUpdate({ ...body, id: editId.value })
    else await api.aiProviderCreate(body)
    editModal.value = false
    message.success('已保存')
    await load()
  } catch (e: any) {
    message.error(e?.message || '保存失败')
  } finally {
    saving.value = false
  }
}

async function switchTo(p: AiProvider) {
  busy.value = p.id
  try {
    await api.aiSwitch(app.value, p.id)
    message.success(`已切到 ${p.name},配置文件写好了`)
    await load()
  } catch (e: any) {
    message.error(e?.message || '切换失败')
  } finally {
    busy.value = ''
  }
}

// applyNow 把当前这份重新写一遍,不管文件在不在、被谁改过。后端切到"已经是当前"
// 的那份时不做回填,正好就是用库里的内容盖掉文件 —— 复用同一个接口。
async function applyNow(p: AiProvider) {
  busy.value = p.id
  try {
    await api.aiSwitch(app.value, p.id)
    message.success(`已按 ${p.name} 重写配置文件`)
    await load()
  } catch (e: any) {
    message.error(e?.message || '写入失败')
  } finally {
    busy.value = ''
  }
}

async function remove(p: AiProvider) {
  try {
    await api.aiProviderDelete(app.value, p.id)
    message.success('已删除')
    await load()
  } catch (e: any) {
    message.error(e?.message || '删除失败')
  }
}
function onImportPick(ev: Event) {
  const input = ev.target as HTMLInputElement
  const f = input.files?.[0]
  if (!f) return
  api.aiImport(f)
    .then(async (r) => {
      if (!r.ok) throw new Error((await r.text()) || '导入失败')
      message.success('导入完成')
      await load()
    })
    .catch((e: any) => message.error(e?.message || '导入失败'))
    .finally(() => { input.value = '' })
}

watch(app, load)
onMounted(load)
</script>

<template>
  <div class="page-content">
    <div class="ai-head">
      <h2>AI 配置</h2>
      <n-button size="small" type="primary" @click="openCreate">新增配置</n-button>
    </div>

    <div class="ai-bar">
      <!-- 切换条自己画:naive-ui 里没有内容的 n-tab-pane 会渲染成一个没有文字的标签,
           整条切换器等于消失了(见 tabs/src/Tabs.mjs 对空 slot 的归一化)。 -->
      <div class="ai-switch" role="tablist">
        <button v-for="a in APPS" :key="a.value" type="button" role="tab" class="ai-seg"
          :class="{ on: app === a.value }" :aria-selected="app === a.value" @click="app = a.value">
          {{ a.label }}
        </button>
      </div>
      <div class="ai-tools">
        <n-button class="ai-btn" size="tiny" secondary @click="importInput?.click()">导入</n-button>
        <n-button class="ai-btn" size="tiny" secondary tag="a" :href="api.aiExportUrl()" download>导出</n-button>
      </div>
    </div>
    <input ref="importInput" type="file" accept=".json,application/json" class="ai-file"
      @change="onImportPick" />

    <n-spin :show="loading">
      <n-empty v-if="!providers.length" :description="`还没有 ${activeApp.label} 的配置`" class="ai-empty">
        <template #extra>
          <n-button size="small" @click="openCreate">从预设建一份</n-button>
        </template>
      </n-empty>
      <div v-else class="ai-grid">
        <div v-for="p in sorted" :key="p.id" class="ai-card" :class="{ on: p.isCurrent }">
          <div class="ai-top">
            <span class="ai-name">{{ p.name }}</span>
            <n-tag v-if="p.isCurrent" type="success" size="small" :bordered="false">当前</n-tag>
            <n-tag size="small" :bordered="false">{{ categoryLabel(p.category) }}</n-tag>
          </div>
          <div class="ai-meta">
            <div class="ai-row">
              <span class="ai-k">端点</span><span class="ai-v">{{ baseUrlOf(p) }}</span>
            </div>
            <div v-if="modelOf(p)" class="ai-row">
              <span class="ai-k">模型</span><span class="ai-v">{{ modelOf(p) }}</span>
            </div>
          </div>
          <div class="ai-ops">
            <n-button v-if="!p.isCurrent" class="ai-btn" size="tiny" type="primary" secondary
              :loading="busy === p.id" @click="switchTo(p)">
              设为当前
            </n-button>
            <n-button v-else class="ai-btn" size="tiny" secondary :loading="busy === p.id"
              title="用这份记录重写配置文件,覆盖机器上的改动" @click="applyNow(p)">
              立即设定
            </n-button>
            <n-button class="ai-btn" size="tiny" quaternary @click="openEdit(p)">编辑</n-button>
            <n-button class="ai-btn" size="tiny" quaternary @click="openCopy(p)">复制</n-button>
            <n-button v-if="p.websiteUrl" class="ai-btn" size="tiny" quaternary tag="a"
              :href="p.websiteUrl" target="_blank" rel="noreferrer">
              官网
            </n-button>
            <n-popconfirm @positive-click="remove(p)">
              <template #trigger>
                <n-button class="ai-btn" size="tiny" quaternary type="error">删除</n-button>
              </template>
              删掉 {{ p.name }}?真实配置文件保持原样。
            </n-popconfirm>
          </div>
        </div>
      </div>
    </n-spin>
    <n-modal v-model:show="editModal" preset="card" :title="editId ? '编辑配置' : '新增配置'"
      class="ai-modal">
      <div class="form">
        <label>预设</label>
        <n-select v-model:value="presetName" :options="presetOptions" clearable
          :placeholder="`选一个 ${activeApp.label} 供应商自动填好,也可以全手填`" @update:value="applyPreset" />
        <label>配置名</label>
        <n-input v-model:value="editName" placeholder="如 Kimi / 公司代理" />
        <template v-if="needsKey">
          <label>API Key</label>
          <n-input v-model:value="apiKey" type="password" show-password-on="click" :placeholder="keyHint" />
        </template>
        <template v-if="app === 'claude'">
          <label>settings.json</label>
          <n-input v-model:value="configText" type="textarea" :autosize="{ minRows: 8, maxRows: 20 }"
            class="mono" />
        </template>
        <template v-else>
          <label>config.toml</label>
          <n-input v-model:value="configText" type="textarea" :autosize="{ minRows: 8, maxRows: 20 }"
            class="mono" />
          <label>auth.json</label>
          <n-input v-model:value="authText" type="textarea" :autosize="{ minRows: 3, maxRows: 8 }" class="mono" />
          <div class="ai-hint">留空或写成空对象就不动 auth.json —— 官方 ChatGPT 的登录态也存在这个文件里。</div>
        </template>
        <label>官网(可选)</label>
        <n-input v-model:value="editSite" placeholder="https://…" />
      </div>
      <template #footer>
        <div class="ai-foot">
          <n-button @click="editModal = false">取消</n-button>
          <n-button type="primary" :loading="saving" @click="save">保存</n-button>
        </div>
      </template>
    </n-modal>

  </div>
</template>

<style scoped>
.ai-head { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
.ai-head h2 { margin: 0; font-size: 20px; }
.ai-bar { display: flex; align-items: center; justify-content: space-between; gap: 10px; flex-wrap: wrap; margin: 10px 0 12px; }
.ai-tools { display: flex; gap: 8px; }
.ai-file { display: none; }

.ai-switch { display: inline-flex; gap: 2px; padding: 3px; border-radius: var(--lr-radius); background: rgba(127, 127, 127, .12); }
.ai-seg {
  appearance: none; border: 0; background: transparent; cursor: pointer;
  font: inherit; font-size: 13px; font-weight: 600; color: var(--lr-fg-muted);
  height: 32px; padding: 0 16px; border-radius: calc(var(--lr-radius) - 4px);
}
.ai-seg.on { background: var(--lr-bg-elevated); color: var(--lr-accent); box-shadow: 0 1px 3px rgba(0, 0, 0, .14); }

.ai-empty { padding: 40px 0; }

.ai-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(300px, 1fr)); gap: 10px; }
.ai-card {
  display: flex; flex-direction: column; gap: 8px; padding: 12px;
  background: var(--lr-bg-elevated); border: 1px solid rgba(127, 127, 127, .14);
  border-radius: var(--lr-radius);
}
.ai-card.on { border-color: var(--lr-accent); }
.ai-top { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }
.ai-name { font-weight: 600; font-size: 15px; margin-right: auto; word-break: break-all; }
.ai-meta { display: flex; flex-direction: column; gap: 4px; }
.ai-row { display: flex; align-items: baseline; gap: 8px; font-size: 12px; }
.ai-k { flex: none; color: var(--lr-fg-muted); }
.ai-v { min-width: 0; font-family: ui-monospace, monospace; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.ai-ops { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; margin-top: 2px; }
/* 全局给 .n-button 定了 44px 触控高度,卡片里的操作行要压回小尺寸。 */
.ai-btn { min-height: 28px; height: 28px; }

.ai-hint { font-size: 12px; color: var(--lr-fg-muted); line-height: 1.5; }
.form { display: flex; flex-direction: column; gap: 8px; }
.form label { font-size: 12px; color: var(--lr-fg-muted); }
.ai-foot { display: flex; justify-content: flex-end; gap: 8px; }
.ai-modal { width: min(620px, calc(100vw - 24px)); }
.mono :deep(textarea) { font-family: ui-monospace, monospace; font-size: 12px; }
</style>
