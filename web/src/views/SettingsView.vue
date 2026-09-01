<script setup lang="ts">
// 设置:系统信息 + 备份(WebDAV)任务管理。
import { ref, onMounted, computed } from 'vue'
import { NButton, NInput, NInputNumber, NList, NListItem, NEmpty, NSpin, NModal,
  NTag, NPopconfirm, useMessage, useDialog, NTabs, NTabPane, NSwitch, NSlider } from 'naive-ui'
import { api, type SysInfo, type BackupJob, type BackupSnapshot, type PushStatus } from '@/api/client'

const message = useMessage()
const dialog = useDialog()
const tab = ref('backup')
const sys = ref<SysInfo | null>(null)
// percent 为 -1 表示平台不支持采集,进度条按 0 画。
const cpuPct = computed(() => Math.max(0, Math.min(100, sys.value?.cpu.percent ?? 0)))

// ---- Web Push ----
const pushStatus = ref<PushStatus | null>(null)
const pushEnabled = ref(false)
const pushBusy = ref(false)
const permission = ref<NotificationPermission>('default')

// ---- 备份 ----
const jobs = ref<BackupJob[]>([])
const snapshots = ref<Record<string, BackupSnapshot[]>>({})
const loadingJobs = ref(false)
const loadingSnaps = ref('')
const editModal = ref(false)
const editing = ref(false)
const editId = ref('')
const editName = ref('')
const editSource = ref('')
const editUrl = ref('')
const editUser = ref('')
const editPass = ref('')
const editSchedule = ref('')
const editExcludes = ref('')
const editRetention = ref(3)
const editAutoRestore = ref(false)
const editEnabled = ref(true)

function fmtBytes(n?: number): string {
  if (n == null) return '-'
  if (n < 1024) return n + ' B'
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB'
  return (n / 1024 / 1024).toFixed(1) + ' MB'
}

async function loadJobs() {
  loadingJobs.value = true
  try {
    jobs.value = await api.backupJobs()
  } catch (e: any) {
    message.error(e?.message || '加载备份任务失败')
  } finally {
    loadingJobs.value = false
  }
}

async function loadSnaps(id: string) {
  try {
    snapshots.value[id] = await api.backupSnapshots(id)
  } catch { snapshots.value[id] = [] }
}

async function load() {
  loadJobs()
  try { sys.value = await api.sysinfo() } catch {}
  try {
    const ps = await api.pushStatus()
    pushStatus.value = ps
    if (ps.configured) {
      permission.value = Notification.permission
      const reg = await navigator.serviceWorker.getRegistration('/sw.js')
      if (reg) {
        const sub = await reg.pushManager.getSubscription()
        pushEnabled.value = !!sub
      }
    }
  } catch { /* Web Push 未启用 */ }
}

// base64url → Uint8Array(pushManager 需要的 applicationServerKey 格式)。
function urlBase64ToUint8Array(base64: string): Uint8Array<ArrayBuffer> {
  const padding = '='.repeat((4 - (base64.length % 4)) % 4)
  const b64 = (base64 + padding).replace(/-/g, '+').replace(/_/g, '/')
  const raw = atob(b64)
  const bytes = new Uint8Array(raw.length)
  for (let i = 0; i < raw.length; i++) bytes[i] = raw.charCodeAt(i)
  return bytes
}

async function enablePush() {
  if (!pushStatus.value?.vapidPublicKey) return
  pushBusy.value = true
  try {
    const perm = await Notification.requestPermission()
    permission.value = perm
    if (perm !== 'granted') { message.warning('通知权限未授予'); return }
    const reg = await navigator.serviceWorker.register('/sw.js')
    const sub = await reg.pushManager.subscribe({
      userVisibleOnly: true,
      applicationServerKey: urlBase64ToUint8Array(pushStatus.value.vapidPublicKey),
    })
    await api.pushSubscribe(sub.toJSON() as any)
    pushEnabled.value = true
    message.success('已启用浏览器通知')
  } catch (e: any) {
    message.error(e?.message || '启用推送失败(需 HTTPS 或 localhost)')
  } finally {
    pushBusy.value = false
  }
}

async function disablePush() {
  pushBusy.value = true
  try {
    const reg = await navigator.serviceWorker.getRegistration('/sw.js')
    const sub = reg ? await reg.pushManager.getSubscription() : null
    const endpoint = sub?.endpoint
    if (sub) await sub.unsubscribe()
    await api.pushUnsubscribe(endpoint)
    pushEnabled.value = false
    message.success('已关闭浏览器通知')
  } catch (e: any) {
    message.error(e?.message || '关闭推送失败')
  } finally {
    pushBusy.value = false
  }
}

async function onPushToggle(v: boolean) {
  if (v) await enablePush()
  else await disablePush()
}

async function sendTest() {
  try {
    const r = await api.pushTest()
    if (r.sent > 0) message.success(`已发送 ${r.sent} 条测试通知`)
    else if (r.failed > 0) message.error(`发送失败 ${r.failed} 条`)
    else message.warning('无订阅设备')
  } catch (e: any) {
    message.error(e?.message || '发送测试失败')
  }
}

function openCreate() {
  editing.value = false
  editId.value = ''
  editName.value = ''
  editSource.value = ''
  editUrl.value = ''
  editUser.value = ''
  editPass.value = ''
  editSchedule.value = ''
  editExcludes.value = ''
  editRetention.value = 3
  editAutoRestore.value = false
  editEnabled.value = true
  editModal.value = true
}

function openEdit(j: BackupJob) {
  editing.value = true
  editId.value = j.id
  editName.value = j.name
  editSource.value = j.sourceDir
  editUrl.value = j.webdavUrl
  editUser.value = j.webdavUser || ''
  editPass.value = j.webdavPass || ''
  editSchedule.value = j.schedule || ''
  editExcludes.value = (j.excludes || []).join('\n')
  editRetention.value = j.retention || 3
  editAutoRestore.value = !!j.autoRestore
  editEnabled.value = !!j.enabled
  editModal.value = true
}

function parseExcludes(text: string): string[] {
  return text.split('\n').map(s => s.trim()).filter(Boolean)
}

async function save() {
  if (!editName.value.trim() || !editSource.value.trim() || !editUrl.value.trim()) {
    message.warning('名称、源目录、WebDAV 地址必填'); return
  }
  try {
    await api.backupSave({
      id: editId.value || undefined,
      name: editName.value,
      sourceDir: editSource.value,
      webdavUrl: editUrl.value,
      webdavUser: editUser.value,
      webdavPass: editPass.value,
      schedule: editSchedule.value,
      excludes: parseExcludes(editExcludes.value),
      retention: editRetention.value,
      autoRestore: editAutoRestore.value,
      enabled: editEnabled.value,
    })
    message.success(editing.value ? '已保存' : '已创建')
    editModal.value = false
    await loadJobs()
  } catch (e: any) {
    message.error(e?.message || '保存失败')
  }
}

async function runJob(id: string) {
  try {
    await api.backupRun(id)
    message.success('已触发备份')
    setTimeout(() => { loadJobs(); loadSnaps(id) }, 500)
  } catch (e: any) {
    message.error(e?.message || '触发失败')
  }
}

async function removeJob(id: string) {
  try {
    await api.backupDelete(id)
    delete snapshots.value[id]
    message.success('已删除')
    await loadJobs()
  } catch (e: any) {
    message.error(e?.message || '删除失败')
  }
}

async function restoreSnap(j: BackupJob, snap?: string) {
  dialog.warning({
    title: '确认恢复',
    content: snap
      ? `将从快照 ${snap} 恢复目录 ${j.sourceDir}。仅覆盖已存在文件,不删除额外文件。确定?`
      : `将恢复到 ${j.sourceDir} 最近一次成功备份。仅覆盖已存在文件,不删除额外文件。确定?`,
    positiveText: '恢复',
    negativeText: '取消',
    onPositiveClick: async () => {
      try {
        await api.backupRestore(j.id, snap)
        message.success('恢复完成')
      } catch (e: any) {
        message.error(e?.message || '恢复失败')
      }
    },
  })
}

function toggleSnaps(j: BackupJob) {
  if (snapshots.value[j.id]) { delete snapshots.value[j.id]; return }
  loadSnaps(j.id)
}

function fmtTime(t?: string): string {
  if (!t) return '-'
  const d = new Date(t)
  return isNaN(d.getTime()) ? t : d.toLocaleString()
}

onMounted(load)
</script>

<template>
  <div class="page-content">
    <h2>设置</h2>

    <n-tabs v-model:value="tab" type="line" size="small">
      <!-- 备份 -->
      <n-tab-pane name="backup" tab="备份">
        <div class="set-row">
          <n-button size="small" type="primary" @click="openCreate">新建备份任务</n-button>
          <span class="hint">备份到 WebDAV(如坚果云/Nextcloud),支持定时与版本保留。</span>
        </div>

        <n-spin :show="loadingJobs">
          <n-empty v-if="!jobs.length" description="暂无备份任务" style="padding:30px" />
          <n-list v-else>
            <n-list-item v-for="j in jobs" :key="j.id">
              <div class="job-card">
                <div class="job-top">
                  <span class="job-name">{{ j.name }}</span>
                  <n-tag size="tiny" :type="j.enabled ? 'success' : 'default'" :bordered="false">
                    {{ j.enabled ? '启用' : '停用' }}
                  </n-tag>
                  <n-tag v-if="j.running" size="tiny" type="warning" :bordered="false">备份中</n-tag>
                  <n-tag v-else-if="j.lastRun" size="tiny" :type="j.lastOk ? 'success' : 'error'" :bordered="false">
                    {{ j.lastOk ? '成功' : '失败' }}
                  </n-tag>
                </div>
                <div class="job-info">
                  <div>{{ j.sourceDir }} → {{ j.webdavUrl }}</div>
                  <div v-if="j.schedule" class="job-meta">定时: {{ j.schedule }} · 保留: {{ j.retention }} 份</div>
                  <div v-else class="job-meta">手动 · 保留 {{ j.retention }} 份</div>
                  <div v-if="j.lastRun" class="job-meta">上次: {{ fmtTime(j.lastRun) }}</div>
                  <div v-if="j.lastErr" class="job-err">{{ j.lastErr }}</div>
                </div>
                <div v-if="j.excludes?.length" class="job-ex">
                  <n-tag v-for="e in j.excludes" :key="e" size="tiny" :bordered="false">{{ e }}</n-tag>
                </div>
                <div class="job-ops">
                  <n-button size="tiny" @click="runJob(j.id)" :disabled="j.running">立即备份</n-button>
                  <n-button size="tiny" quaternary @click="toggleSnaps(j)">快照</n-button>
                  <n-button size="tiny" quaternary @click="restoreSnap(j)">恢复最近</n-button>
                  <n-button size="tiny" quaternary @click="openEdit(j)">编辑</n-button>
                  <n-popconfirm @positive-click="removeJob(j.id)">
                    <template #trigger><n-button size="tiny" quaternary type="error">删除</n-button></template>
                    删除任务 {{ j.name }}? (远端快照不受影响)
                  </n-popconfirm>
                </div>

                <!-- 快照列表 -->
                <div v-if="snapshots[j.id]" class="snap-list">
                  <n-empty v-if="!snapshots[j.id].length" description="暂无快照" />
                  <div v-for="s in snapshots[j.id]" :key="s.name" class="snap-row">
                    <span class="snap-name">{{ s.name }}</span>
                    <span class="snap-size">{{ fmtBytes(s.size) }}</span>
                    <n-button size="tiny" quaternary @click="restoreSnap(j, s.name)">恢复</n-button>
                    <a :href="api.backupDownloadUrl(j.id, s.name)" download class="snap-dl">下载</a>
                  </div>
                </div>
              </div>
            </n-list-item>
          </n-list>
        </n-spin>
      </n-tab-pane>

      <!-- 系统信息 -->
      <n-tab-pane name="sys" tab="系统">
        <div v-if="sys" class="sys-grid">
          <div class="sys-card">
            <h4>CPU</h4>
            <div class="bar"><div class="bar-fill" :style="{ width: cpuPct + '%' }" /></div>
            <div class="sys-line">
              {{ sys.cpu.percent < 0 ? '使用率不可用' : `使用率: ${sys.cpu.percent.toFixed(0)}%` }}
              · 核数: {{ sys.cpu.cores }}
              <template v-if="sys.cpu.load > 0"> · 负载: {{ sys.cpu.load.toFixed(2) }}</template>
            </div>
          </div>
          <div class="sys-card">
            <h4>内存</h4>
            <div class="bar"><div class="bar-fill" :style="{ width: Math.min(100, sys.memory.usedPct) + '%' }" /></div>
            <div class="sys-line">{{ sys.memory.usedMB }} MB / {{ sys.memory.totalMB }} MB ({{ sys.memory.usedPct }}%)</div>
          </div>
          <div class="sys-card">
            <h4>磁盘</h4>
            <div class="bar"><div class="bar-fill" :style="{ width: Math.min(100, sys.disk.usedPct) + '%' }" /></div>
            <div class="sys-line">{{ sys.disk.usedGB }} GB / {{ sys.disk.totalGB }} GB ({{ sys.disk.usedPct }}%)</div>
          </div>
        </div>
        <n-empty v-else description="系统信息不可用" style="padding:30px" />
      </n-tab-pane>

      <!-- Web Push 通知 -->
      <n-tab-pane name="push" tab="推送">
        <div v-if="!pushStatus || !pushStatus.configured" class="hint">
          Web Push 未配置服务端(缺少 VAPID 密钥)。
        </div>
        <div v-else class="push-pane">
          <label class="switch-row">
            <n-switch v-model:value="pushEnabled" :disabled="pushBusy" @update:value="onPushToggle" />
            启用浏览器通知
          </label>
          <div class="hint">当前权限: {{ permission }}。启用后,长命令静默完成时发送通知(终端安静 {{ pushStatus.idleSeconds }} 秒)。</div>
          <div class="push-actions">
            <n-button size="small" :disabled="!pushEnabled || pushBusy" @click="sendTest">发送测试通知</n-button>
          </div>
          <div class="hint">已订阅设备: {{ pushStatus.count }}</div>
        </div>
      </n-tab-pane>
    </n-tabs>

    <!-- 任务编辑弹窗 -->
    <n-modal v-model:show="editModal" preset="card" :title="editing ? '编辑备份任务' : '新建备份任务'" style="width:540px">
      <div class="form">
        <label>名称</label>
        <n-input v-model:value="editName" placeholder="如 网站数据" />
        <label>源目录(服务器绝对路径)</label>
        <n-input v-model:value="editSource" placeholder="/data/www" />
        <label>WebDAV 地址</label>
        <n-input v-model:value="editUrl" placeholder="https://dav.example.com/remote.php/dav/files/user/backup" />
        <label>WebDAV 用户名 / 密码</label>
        <div class="row2">
          <n-input v-model:value="editUser" placeholder="用户名" />
          <n-input v-model:value="editPass" type="password" placeholder="密码" />
        </div>
        <label>Cron 定时(留空=仅手动,如 0 3 * * * 每天3点)</label>
        <n-input v-model:value="editSchedule" placeholder="分 时 日 月 周" />
        <label>保留份数</label>
        <n-input-number v-model:value="editRetention" :min="1" :max="100" style="width:100%" />
        <label>排除(每行一个 gitignore 模式,如 *.log)</label>
        <n-input v-model:value="editExcludes" type="textarea" :autosize="{minRows:2,maxRows:5}" />
        <div class="row2">
          <label class="switch-row"><n-switch v-model:value="editEnabled" /> 启用</label>
          <label class="switch-row"><n-switch v-model:value="editAutoRestore" /> 启动自动恢复</label>
        </div>
      </div>
      <template #footer>
        <n-button @click="editModal = false">取消</n-button>
        <n-button type="primary" @click="save">保存</n-button>
      </template>
    </n-modal>
  </div>
</template>

<style scoped>
h2 { font-size: 20px; margin: 0 0 8px; }
.set-row { display: flex; align-items: center; gap: 12px; margin-bottom: 12px; }
.hint { color: var(--lr-fg-muted); font-size: 12px; }
.job-card { display: flex; flex-direction: column; gap: 6px; }
.job-top { display: flex; align-items: center; gap: 8px; }
.job-name { font-weight: 600; font-size: 15px; }
.job-info { font-size: 13px; display: flex; flex-direction: column; gap: 2px; }
.job-meta { color: var(--lr-fg-muted); font-size: 12px; }
.job-err { color: #d03050; font-size: 12px; }
.job-ex { display: flex; gap: 6px; flex-wrap: wrap; }
.job-ops { display: flex; gap: 8px; align-items: center; }
.snap-list { margin-top: 6px; border-top: 1px solid var(--lr-border, #eee); padding-top: 6px; display: flex; flex-direction: column; gap: 4px; }
.snap-row { display: flex; align-items: center; gap: 8px; font-size: 13px; }
.snap-name { font-family: monospace; }
.snap-size { color: var(--lr-fg-muted); font-size: 12px; }
.snap-dl { font-size: 13px; color: var(--lr-primary, #2563eb); }
.sys-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); gap: 12px; }
.sys-card { border: 1px solid var(--lr-border, #eee); border-radius: 8px; padding: 12px; }
.sys-card h4 { margin: 0 0 8px; font-size: 14px; }
.bar { height: 8px; background: #eef0f4; border-radius: 4px; overflow: hidden; margin-bottom: 8px; }
.bar-fill { height: 100%; background: var(--lr-primary, #2563eb); border-radius: 4px; }
.sys-line { font-size: 12px; color: var(--lr-fg-muted); }
.form { display: flex; flex-direction: column; gap: 8px; }
.form label { font-size: 12px; color: var(--lr-fg-muted); }
.row2 { display: flex; gap: 8px; align-items: center; }
.switch-row { display: flex; align-items: center; gap: 6px; }
.push-pane { display: flex; flex-direction: column; gap: 10px; max-width: 480px; }
.push-actions { display: flex; gap: 8px; }
</style>
