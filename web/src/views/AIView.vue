<script setup lang="ts">
// AI 配置档案:多套 Claude/Codex 环境配置,切换后新终端生效。
import { ref, onMounted, computed } from 'vue'
import { NButton, NInput, NList, NListItem, NEmpty, NSpin, NModal, NTag,
  NInputNumber, useMessage, useDialog, NRadio, NRadioGroup, NPopconfirm } from 'naive-ui'
import { api, type AiProfile } from '@/api/client'

const message = useMessage()
const dialog = useDialog()
const profiles = ref<AiProfile[]>([])
const active = ref('')
const loading = ref(false)
const editModal = ref(false)
const editing = ref(false)
const editName = ref('')
const editPreset = ref('')
const cloneFrom = ref('')
const envText = ref('')
const importFile = ref<File | null>(null)

async function load() {
  loading.value = true
  try {
    const [list, act] = await Promise.all([api.aiProfiles(), api.aiActive()])
    profiles.value = list
    active.value = act?.name || ''
  } catch (e: any) {
    message.error(e?.message || '加载失败')
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editing.value = false
  editName.value = ''
  editPreset.value = 'direct'
  cloneFrom.value = ''
  envText.value = ''
  editModal.value = true
}

function openEdit(p: AiProfile) {
  editing.value = true
  editName.value = p.name
  editPreset.value = ''
  envText.value = Object.entries(p.env || {}).map(([k, v]) => `${k}=${v}`).join('\n')
  editModal.value = true
}

function parseEnv(text: string): Record<string, string> {
  const env: Record<string, string> = {}
  for (const line of text.split('\n')) {
    const t = line.trim()
    if (!t || t.startsWith('#')) continue
    const i = t.indexOf('=')
    if (i > 0) env[t.slice(0, i).trim()] = t.slice(i + 1).trim()
  }
  return env
}

async function save() {
  if (!editName.value.trim()) { message.warning('请输入档案名'); return }
  try {
    if (editing.value) {
      await api.aiUpdate(editName.value, parseEnv(envText.value))
      message.success('已更新')
    } else {
      await api.aiCreate({
        name: editName.value,
        env: parseEnv(envText.value),
        preset: editPreset.value || undefined,
        cloneFrom: cloneFrom.value || undefined,
      })
      message.success('已创建')
    }
    editModal.value = false
    await load()
  } catch (e: any) {
    message.error(e?.message || '保存失败')
  }
}

async function setActive(name: string) {
  try {
    await api.aiSetActive(name)
    active.value = name
    message.success(`已切换到 ${name}。新终端会话生效,已运行进程不受影响。`)
  } catch (e: any) {
    message.error(e?.message || '切换失败')
  }
}

async function remove(name: string) {
  try {
    await api.aiDelete(name)
    if (active.value === name) active.value = ''
    await load()
    message.success('已删除')
  } catch (e: any) {
    message.error(e?.message || '删除失败')
  }
}

function onImportPick(ev: Event) {
  const input = ev.target as HTMLInputElement
  const f = input.files?.[0]
  if (!f) return
  const name = f.name.replace(/\.(tar\.gz|tgz)$/i, '')
  importFile.value = f
  if (!name) { message.warning('文件名需形如 profile.tar.gz'); input.value = ''; return }
  api.aiImport(name, f).then(() => {
    message.success('导入成功')
    input.value = ''
    importFile.value = null
    load()
  }).catch((e: any) => message.error(e?.message || '导入失败'))
}

const sorted = computed(() => profiles.value.slice().sort((a, b) => (a.name === active.value ? -1 : 0) - (b.name === active.value ? -1 : 0)))

onMounted(load)
</script>

<template>
  <div class="page-content">
    <div class="ai-header">
      <h2>AI 配置档案</h2>
      <div class="ai-actions">
        <label class="file-btn">
          导入
          <input type="file" accept=".tar.gz,.tgz" style="display:none" @change="onImportPick" />
        </label>
        <n-button size="small" type="primary" @click="openCreate">新建档案</n-button>
      </div>
    </div>
    <p class="ai-note">切换档案后<b>新开</b>的终端会话使用该配置(注入 CLAUDE_CONFIG_DIR / CODEX_HOME 等环境变量);已运行的终端进程不受影响。</p>

    <n-spin :show="loading">
      <n-empty v-if="!profiles.length" description="暂无档案,点击“新建档案”开始" style="padding:40px" />
      <n-list v-else>
        <n-list-item v-for="p in sorted" :key="p.name">
          <div class="ai-card">
            <div class="ai-top">
              <span class="ai-name">{{ p.name }}</span>
              <n-tag v-if="p.name === active" type="success" size="small" :bordered="false">当前</n-tag>
              <n-tag v-if="p.hasClaude" size="small" :bordered="false">Claude</n-tag>
              <n-tag v-if="p.hasCodex" size="small" :bordered="false">Codex</n-tag>
            </div>
            <div v-if="Object.keys(p.env || {}).length" class="ai-env">
              <div v-for="(v, k) in p.env" :key="k" class="ai-env-row">
                <span class="ai-env-key">{{ k }}</span>=<span class="ai-env-val">{{ v }}</span>
              </div>
            </div>
            <div class="ai-ops">
              <n-button v-if="p.name !== active" size="tiny" type="primary" quaternary @click="setActive(p.name)">设为当前</n-button>
              <n-button size="tiny" quaternary @click="openEdit(p)">编辑</n-button>
              <a :href="api.aiExportUrl(p.name)" download class="ai-dl">导出</a>
              <n-popconfirm @positive-click="remove(p.name)">
                <template #trigger><n-button size="tiny" quaternary type="error">删除</n-button></template>
                确定删除档案 {{ p.name }}?
              </n-popconfirm>
            </div>
          </div>
        </n-list-item>
      </n-list>
    </n-spin>

    <!-- 新建/编辑弹窗 -->
    <n-modal v-model:show="editModal" preset="card" :title="editing ? '编辑档案' : '新建档案'" style="width:520px">
      <div class="form">
        <label>档案名</label>
        <n-input v-model:value="editName" placeholder="如 work / claude" :disabled="editing" />
        <template v-if="!editing">
          <label>预设</label>
          <n-radio-group v-model:value="editPreset">
            <n-radio value="direct">直连</n-radio>
            <n-radio value="proxy">代理</n-radio>
            <n-radio value="custom">自定义</n-radio>
          </n-radio-group>
          <label>从已有档案/主目录克隆(可选)</label>
          <n-input v-model:value="cloneFrom" placeholder="档案名,或 home(克隆 ~/.claude、~/.codex)" />
        </template>
        <label>环境变量(每行 KEY=VALUE)</label>
        <n-input v-model:value="envText" type="textarea" :autosize="{minRows:4,maxRows:10}"
          placeholder="ANTHROPIC_API_KEY=sk-...&#10;CODEX_API_KEY=..." />
      </div>
      <template #footer>
        <n-button @click="editModal = false">取消</n-button>
        <n-button type="primary" @click="save">{{ editing ? '保存' : '创建' }}</n-button>
      </template>
    </n-modal>
  </div>
</template>

<style scoped>
.ai-header { display: flex; align-items: center; justify-content: space-between; margin-bottom: 8px; }
.ai-header h2 { margin: 0; font-size: 20px; }
.ai-actions { display: flex; gap: 8px; align-items: center; }
.ai-note { color: var(--lr-fg-muted); font-size: 12px; margin: 0 0 12px; }
.file-btn { font-size: 13px; color: var(--lr-primary, #2563eb); cursor: pointer; }
.ai-card { display: flex; flex-direction: column; gap: 6px; }
.ai-top { display: flex; align-items: center; gap: 8px; }
.ai-name { font-weight: 600; font-size: 15px; }
.ai-env { display: flex; flex-direction: column; gap: 2px; font-size: 12px; }
.ai-env-row { font-family: monospace; }
.ai-env-key { color: var(--lr-primary, #2563eb); }
.ai-env-val { color: var(--lr-fg-muted); }
.ai-ops { display: flex; gap: 8px; align-items: center; }
.ai-dl { font-size: 13px; color: var(--lr-primary, #2563eb); }
.form { display: flex; flex-direction: column; gap: 8px; }
.form label { font-size: 12px; color: var(--lr-fg-muted); }
</style>
