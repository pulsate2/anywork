<script setup lang="ts">
// 设置:系统信息(含任务管理器式进程列表)+ 备份(WebDAV)任务管理。
import { ref, onMounted, onUnmounted, computed, watch } from 'vue'
import { NButton, NInput, NInputNumber, NList, NListItem, NEmpty, NSpin, NModal,
  NTag, NPopconfirm, useMessage, useDialog, NTabs, NTabPane, NSwitch, NSlider, NSelect,
  NTooltip } from 'naive-ui'
import { api, type SysInfo, type ProcInfo, type BackupJob, type BackupSnapshot, type PushStatus } from '@/api/client'
import { useWorkspaceStore } from '@/stores/workspace'
import DirTreePicker from '@/components/DirTreePicker.vue'

const message = useMessage()
const dialog = useDialog()
const store = useWorkspaceStore()
const tab = ref('backup')
const sys = ref<SysInfo | null>(null)
// percent 为 -1 表示平台不支持采集,进度条按 0 画。
const cpuPct = computed(() => Math.max(0, Math.min(100, sys.value?.cpu.percent ?? 0)))
// swap 可能整体缺失(机器没配 / 老版本后端没这个字段),进度条按 0 画。
const swapPct = computed(() => Math.max(0, Math.min(100, sys.value?.swap?.usedPct ?? 0)))

// ---- 系统面板实时刷新 ----
// 只在"系统"标签页可见时轮询:切走或页面进后台就停,不给服务器和手机电池添活。
const SYS_KEY = 'lr.sys.'
const procSort = ref<'cpu' | 'mem'>(localStorage.getItem(SYS_KEY + 'sort') === 'mem' ? 'mem' : 'cpu')
const procLimit = ref(Number(localStorage.getItem(SYS_KEY + 'limit')) || 20)
const sysInterval = ref(Number(localStorage.getItem(SYS_KEY + 'interval')) || 2000)
const sysAuto = ref(localStorage.getItem(SYS_KEY + 'auto') !== '0')
const sysErr = ref('')
// 后端没进程可报时 processes 是 null,兜底成数组免得模板里 .length 崩掉。
const procs = computed(() => sys.value?.processes ?? [])
// killOpen 是"结束进程"确认气泡开在哪一行(pid),null = 没开。它同时是轮询的闸门,
// 所以声明在这里而不是下面的结束进程小节:表格每两秒按占用重排一次,气泡开着时
// 手指底下的行会换成别的进程 —— 对不可撤销的操作来说那是会杀错人的。
const killOpen = ref<number | null>(null)
// busy 是并发闸门:上一次还没回来就不再发,慢网络下不会堆成一串请求。
let sysBusy = false
let sysTimer: number | null = null
// killTimer 是结束进程后那次补刷。存起来是为了在离开页面时能取消掉:定时器里的
// restartSys 会重新排下一次轮询,组件都卸载了还在打接口。
let killTimer: number | null = null

const intervalOptions = [
  { label: '1 秒', value: 1000 },
  { label: '2 秒', value: 2000 },
  { label: '3 秒', value: 3000 },
  { label: '5 秒', value: 5000 },
  { label: '10 秒', value: 10000 },
]
const limitOptions = [10, 20, 50, 100].map(n => ({ label: `前 ${n} 条`, value: n }))

function fmtMB(n: number): string {
  if (n >= 1024) return (n / 1024).toFixed(2) + ' GB'
  return n >= 10 ? n.toFixed(0) + ' MB' : n.toFixed(1) + ' MB'
}

async function pullSys(withProcs = true) {
  if (sysBusy) return
  sysBusy = true
  try {
    sys.value = await api.sysinfo({ procs: withProcs ? procLimit.value : 0, sort: procSort.value })
    sysErr.value = ''
  } catch (e: any) {
    sysErr.value = e?.message || '采集失败'
  } finally {
    sysBusy = false
  }
}

function stopSys() {
  if (sysTimer !== null) { clearTimeout(sysTimer); sysTimer = null }
  if (killTimer !== null) { clearTimeout(killTimer); killTimer = null }
}

// 用 setTimeout 串起来而不是 setInterval:一次采集慢了,下一次从它结束时才开始算。
function scheduleSys() {
  stopSys()
  if (tab.value !== 'sys' || !sysAuto.value || document.hidden) return
  // 结束进程的确认气泡开着时不排下一次:这个判断放在这里而不是只在打开时 stopSys(),
  // 因为打开的瞬间可能正有一次采集在途,它回来后会自己再排一次,把刚停下的轮询接上。
  if (killOpen.value !== null) return
  sysTimer = window.setTimeout(async () => {
    await pullSys()
    scheduleSys()
  }, sysInterval.value)
}

function restartSys() {
  stopSys()
  if (tab.value !== 'sys') return
  pullSys()
  scheduleSys()
}

function onVisibility() {
  if (document.hidden) stopSys()
  else restartSys()
}

watch(tab, () => restartSys())
watch(sysAuto, v => { localStorage.setItem(SYS_KEY + 'auto', v ? '1' : '0'); scheduleSys() })
watch(sysInterval, v => { localStorage.setItem(SYS_KEY + 'interval', String(v)); scheduleSys() })
// 排序和条数由服务端算,改了就立刻重取一次,不用等下一个周期。
watch(procSort, v => { localStorage.setItem(SYS_KEY + 'sort', v); restartSys() })
watch(procLimit, v => { localStorage.setItem(SYS_KEY + 'limit', String(v)); restartSys() })

// ---- 结束进程 ----
// killing 是正在发请求的那个 pid,只用来在按钮上转圈。
const killing = ref<number | null>(null)
// Windows 上没有"礼貌地请你退出",TerminateProcess 是唯一的路,所以不给强杀选项,
// 改在确认框里说清后果。
const forceOnly = computed(() => !!sys.value?.killForceOnly)

function killTip(p: ProcInfo): string {
  const who = `${p.name} (pid ${p.pid})`
  if (forceOnly.value) return `强制结束 ${who}?进程没有保存或清理的机会。`
  return `结束 ${who}?"结束"先发 SIGTERM 让它自己退出,"强制"直接抹掉。`
}

// onKillShow 只认自己那一行的关闭事件。点开 A 时,B 的"点到外面了"也会触发一次关闭,
// 而两个回调的先后顺序不定 —— 不加这道判断,B 的关闭可能落在 A 的打开之后,把刚开的气泡关掉。
function onKillShow(pid: number, show: boolean) {
  if (show) {
    killOpen.value = pid
    stopSys()
  } else if (killOpen.value === pid) {
    killOpen.value = null
    scheduleSys()
  }
}

// 气泡开着的那一行有可能被一次"在途"的采集刷掉(进程退出了,或掉出了前 N 条)。
// 行一卸载,组件就再也不会发关闭事件,killOpen 会卡住不放 —— 而它是轮询的闸门,
// 卡住就等于面板永久停止刷新。所以这里兜一道:目标行没了就自己松闸。
watch(procs, list => {
  if (killOpen.value !== null && !list.some(p => p.pid === killOpen.value)) {
    killOpen.value = null
    scheduleSys()
  }
})

async function doKill(p: ProcInfo, force: boolean) {
  onKillShow(p.pid, false)
  killing.value = p.pid
  try {
    // 把表里显示的名字一起传回去:从渲染到点击之间目标可能已经退出、pid 被复用,
    // 后端对不上名字就拒绝,免得杀错进程。
    await api.killProc(p.pid, p.name, force)
    message.success(force ? `已强制结束 ${p.name}` : `已请求结束 ${p.name}`)
  } catch (e: any) {
    // 后端把原因写在响应正文里(权限不足 / 进程不存在 / pid 被复用 / 只读模式),照搬即可。
    message.error(e?.message || '结束进程失败')
  } finally {
    killing.value = null
  }
  // SIGTERM 到进程真的消失之间有个清理窗口,立刻重取往往还能看到它;失败时也要刷,
  // 因为"进程不存在"这类报错本身就说明表是旧的。
  killTimer = window.setTimeout(() => { killTimer = null; restartSys() }, 400)
}

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

// 来源目录必须落在服务端根目录内(后端 dirAllowed 会直接拒),所以提示和树都以 root 为界。
// root 就是 "/" 时那句边界说明是废话,省掉。
const rootDir = computed(() => (store.root || '/').replace(/\/+$/, '') || '/')
const srcNote = computed(() => (rootDir.value === '/' ? '' : `,须在 ${rootDir.value} 之内`))
const srcPlaceholder = computed(() => (rootDir.value === '/' ? '/data/www' : rootDir.value + '/www'))

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
  // 概览卡先出来;进程列表等用户真的切到"系统"页再拉。
  pullSys(false)
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

onMounted(() => {
  load()
  // 目录树和来源提示都以 root 为界。直接刷新在设置页时 store 还是空的(root 由首页
  // 那次 load 填),这里补一次,免得树根显示成 "/"。已加载过就不再要一遍。
  if (!store.loaded) store.load().catch(() => {})
  document.addEventListener('visibilitychange', onVisibility)
})

onUnmounted(() => {
  stopSys()
  document.removeEventListener('visibilitychange', onVisibility)
})
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
        <div class="sys-bar">
          <label class="switch-row">
            <n-switch v-model:value="sysAuto" size="small" /> 自动刷新
          </label>
          <n-select v-model:value="sysInterval" :options="intervalOptions" size="tiny"
            :disabled="!sysAuto" style="width:88px" />
          <n-button size="tiny" quaternary @click="restartSys">刷新</n-button>
          <span class="sys-spacer" />
          <span v-if="sysErr" class="sys-err">{{ sysErr }}</span>
          <span v-else-if="sys" class="hint">采样窗口 {{ sys.sampleMs }}ms</span>
        </div>

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
            <h4>Swap</h4>
            <div class="bar"><div class="bar-fill" :style="{ width: swapPct + '%' }" /></div>
            <!-- 没配 swap 时说明"未启用":有没有交换区本身就是要看的信息,
                 显示成 0 MB / 0 MB 会让人以为是采集失败。 -->
            <div v-if="sys.swap?.totalMB" class="sys-line">
              {{ sys.swap.usedMB }} MB / {{ sys.swap.totalMB }} MB ({{ sys.swap.usedPct }}%)
            </div>
            <div v-else class="sys-line">未启用</div>
          </div>
          <div class="sys-card">
            <h4>磁盘</h4>
            <div class="bar"><div class="bar-fill" :style="{ width: Math.min(100, sys.disk.usedPct) + '%' }" /></div>
            <div class="sys-line">{{ sys.disk.usedGB }} GB / {{ sys.disk.totalGB }} GB ({{ sys.disk.usedPct }}%)</div>
          </div>
        </div>
        <n-empty v-else description="系统信息不可用" style="padding:30px" />

        <!-- 任务管理器:占用最高的进程 -->
        <div v-if="sys && !sys.procSupported" class="hint proc-note">当前平台不支持进程列表(仅 Linux / Windows 可用)。</div>
        <div v-else-if="sys" class="proc-pane">
          <div class="sys-bar">
            <span class="proc-title">进程</span>
            <n-button size="tiny" :type="procSort === 'cpu' ? 'primary' : 'default'"
              :quaternary="procSort !== 'cpu'" @click="procSort = 'cpu'">按 CPU</n-button>
            <n-button size="tiny" :type="procSort === 'mem' ? 'primary' : 'default'"
              :quaternary="procSort !== 'mem'" @click="procSort = 'mem'">按内存</n-button>
            <n-select v-model:value="procLimit" :options="limitOptions" size="tiny" style="width:96px" />
            <span class="sys-spacer" />
            <span class="hint">共 {{ sys.procTotal }} 个</span>
          </div>
          <div class="proc-table" :class="{ 'has-act': sys.canKill }">
            <div class="proc-row proc-head">
              <span class="c-name">名称</span>
              <span class="c-pid opt">PID</span>
              <span class="c-user opt">用户</span>
              <span class="c-thr opt">线程</span>
              <span class="c-num">CPU</span>
              <span class="c-num">内存</span>
              <span v-if="sys.canKill" class="c-act">操作</span>
            </div>
            <div v-for="p in procs" :key="p.pid" class="proc-row">
              <!-- 命令行一行放不下,缩略显示;点一下弹出完整路径(手机没有 hover,
                   所以用 click 而不是原生 title)。 -->
              <n-tooltip trigger="click">
                <template #trigger>
                  <span class="c-name">
                    <b>{{ p.name }}</b>
                    <em>{{ p.cmd }}</em>
                  </span>
                </template>
                <div class="proc-cmd">{{ p.cmd }}</div>
              </n-tooltip>
              <span class="c-pid opt">{{ p.pid }}</span>
              <span class="c-user opt">{{ p.user || '-' }}</span>
              <span class="c-thr opt">{{ p.threads || '-' }}</span>
              <span class="c-num" :class="{ hot: p.cpu >= 50 }">{{ p.cpu.toFixed(1) }}%</span>
              <span class="c-num">{{ fmtMB(p.memMB) }}<i>{{ p.memPct.toFixed(1) }}%</i></span>
              <!-- 结束进程不可撤销,所以必须先确认。强杀藏在确认框里当第二个按钮:
                   常规结束(SIGTERM)才是默认,免得手一抖就把别人的活儿掐断。
                   气泡开着期间轮询暂停,否则表格会在手指底下重排。 -->
              <span v-if="sys.canKill" class="c-act">
                <n-popconfirm :show="killOpen === p.pid" placement="left-end"
                  @update:show="(v: boolean) => onKillShow(p.pid, v)">
                  <template #trigger>
                    <n-button class="kill-btn" size="tiny" quaternary type="error"
                      :loading="killing === p.pid" :disabled="killing !== null">结束</n-button>
                  </template>
                  <template #action>
                    <n-button size="tiny" quaternary @click="onKillShow(p.pid, false)">取消</n-button>
                    <n-button size="tiny" :type="forceOnly ? 'error' : 'primary'" @click="doKill(p, forceOnly)">
                      {{ forceOnly ? '强制结束' : '结束' }}
                    </n-button>
                    <n-button v-if="!forceOnly" size="tiny" type="error" @click="doKill(p, true)">强制</n-button>
                  </template>
                  <div class="kill-tip">{{ killTip(p) }}</div>
                </n-popconfirm>
              </span>
            </div>
            <n-empty v-if="!procs.length" description="暂无进程数据" style="padding:20px" />
          </div>
        </div>
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
        <label>源目录(服务器绝对路径{{ srcNote }})</label>
        <dir-tree-picker v-model="editSource" :placeholder="srcPlaceholder" />
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
/* 加上 Swap 是四张卡了。min 从 200 收到 150:手机上排成 2×2,不然四张竖着堆
   要划半屏才看到进程表。 */
.sys-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(150px, 1fr)); gap: 12px; }
.sys-card { border: 1px solid var(--lr-border, #eee); border-radius: 8px; padding: 12px; }
.sys-card h4 { margin: 0 0 8px; font-size: 14px; }
.bar { height: 8px; background: #eef0f4; border-radius: 4px; overflow: hidden; margin-bottom: 8px; }
.bar-fill { height: 100%; background: var(--lr-primary, #2563eb); border-radius: 4px; }
.sys-line { font-size: 12px; color: var(--lr-fg-muted); }
.sys-bar { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; margin-bottom: 10px; }
.sys-spacer { flex: 1; }
.sys-err { color: #d03050; font-size: 12px; }
.proc-note { display: block; margin-top: 14px; }
.proc-pane { margin-top: 16px; }
.proc-title { font-size: 13px; font-weight: 600; }
.proc-table { border: 1px solid var(--lr-border, #eee); border-radius: 8px; overflow: hidden; }
.proc-row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 64px 96px 48px 62px 104px;
  align-items: center;
  gap: 6px;
  padding: 5px 10px;
  font-size: 12px;
  border-top: 1px solid var(--lr-border, #f1f1f1);
}
.proc-row:first-child { border-top: none; }
/* 多一列给"结束"按钮。只在按钮真的画出来时加(只读模式/不支持的平台没有这一列),
   不然表头和数据行会差一格。 */
.proc-table.has-act .proc-row { grid-template-columns: minmax(0, 1fr) 64px 96px 48px 62px 104px 52px; }
.proc-head { color: var(--lr-fg-muted); font-weight: 600; background: rgba(127, 127, 127, 0.06); }
.c-name { min-width: 0; display: flex; flex-direction: column; cursor: pointer; }
.c-name b { font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.c-name em {
  font-style: normal; color: var(--lr-fg-muted); font-size: 11px;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
/* 弹层里的完整命令行。teleport 出去的元素仍带 scoped 标记,这条能生效。
   长路径没有空格可断,得允许硬折,否则弹层会被撑到屏幕外。 */
.proc-cmd {
  max-width: min(78vw, 420px);
  font-size: 12px; line-height: 1.5;
  word-break: break-all;
}
.proc-head .c-name { display: block; cursor: default; }
.c-pid, .c-thr { color: var(--lr-fg-muted); font-family: monospace; }
.c-user { color: var(--lr-fg-muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.c-num { text-align: right; font-variant-numeric: tabular-nums; }
.c-num i { font-style: normal; color: var(--lr-fg-muted); font-size: 11px; margin-left: 5px; }
.c-num.hot { color: #d03050; font-weight: 600; }
.c-act { text-align: right; }
/* 全局给 .n-button 的 44px 触控下限会把进程行撑成两倍高,一屏就少看好几个进程。
   这里按本仓库其他密排列表的惯例用 28px;点错也只是弹确认框,不会直接杀。 */
.kill-btn { min-height: 28px; height: 28px; padding: 0 6px; }
/* 确认气泡里的说明文字。进程名可能很长,给个上限免得把气泡撑出屏幕。 */
.kill-tip { max-width: min(70vw, 300px); font-size: 12px; line-height: 1.5; word-break: break-all; }
/* 窄屏(手机)只留名称 + 两个数字,PID/用户/线程折掉。 */
@media (max-width: 640px) {
  .proc-row { grid-template-columns: minmax(0, 1fr) 56px 92px; }
  .proc-table.has-act .proc-row { grid-template-columns: minmax(0, 1fr) 50px 84px 48px; }
  .proc-row .opt { display: none; }
}
.form { display: flex; flex-direction: column; gap: 8px; }
.form label { font-size: 12px; color: var(--lr-fg-muted); }
.row2 { display: flex; gap: 8px; align-items: center; }
.switch-row { display: flex; align-items: center; gap: 6px; }
.push-pane { display: flex; flex-direction: column; gap: 10px; max-width: 480px; }
.push-actions { display: flex; gap: 8px; }
</style>
