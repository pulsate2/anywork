<script setup lang="ts">
// 文件浏览:列表/读写/上传下载/搜索/压缩解压。桌面+移动自适应。
import { ref, onMounted, computed, watch } from "vue"
import {
	NButton, NIcon, NEmpty, NSpin,
	NInput, NModal, useMessage, useDialog,
	NTag, NList, NListItem, NSelect, NRadioGroup, NRadioButton,
} from "naive-ui"
import { FolderOpenOutline, DocumentOutline, ArrowBackOutline,
	CreateOutline, TrashOutline, SearchOutline, DownloadOutline,
	CloudUploadOutline, AddOutline, ArrowUpOutline, ArrowDownOutline,
	SwapHorizontalOutline, CloseOutline } from "@vicons/ionicons5"
import { api, type FsEntry, type FsSearchResult } from "@/api/client"
import { useWorkspaceStore } from "@/stores/workspace"

const message = useMessage()
const dialog = useDialog()
const store = useWorkspaceStore()
const cwd = ref("/")
const entries = ref<FsEntry[]>([])
const loading = ref(false)

// 后端返回的都是 root 内的绝对路径(正斜杠)。root 去掉尾斜杠便于前缀比较。
const rootPath = computed(() => trimSlash(store.root))
const atRoot = computed(() => trimSlash(cwd.value) === rootPath.value)
// 面包屑只显示 root 之下的部分。
const relPath = computed(() => {
	const c = trimSlash(cwd.value)
	return c.startsWith(rootPath.value) ? c.slice(rootPath.value.length) || "/" : c
})

function trimSlash(p: string) {
	return p.length > 1 ? p.replace(/\/+$/, "") : p
}
function parentOf(p: string) {
	const t = trimSlash(p)
	const i = t.lastIndexOf("/")
	return i <= 0 ? t : t.slice(0, i)
}
function joinPath(dir: string, name: string) {
	return trimSlash(dir) + "/" + name
}

async function load(dir: string) {
	loading.value = true
	cwd.value = dir || store.currentPath
	try {
		entries.value = await api.fsList(cwd.value)
	} catch (e: any) {
		message.error(e?.message || "加载失败")
	} finally {
		loading.value = false
	}
}

function openEntry(e: FsEntry) {
	if (e.dir) load(e.path)
	else openEditor(e)
}

// ---- 编辑器 ----
const editor = ref<FsEntry | null>(null)
const showEditor = ref(false)
const editText = ref("")
const editSaving = ref(false)
async function openEditor(e: FsEntry) {
	if (e.size > 512 * 1024) {
		message.warning("文件过大,仅支持编辑 512KB 内")
		return
	}
	try {
		const text = await api.fsRead(e.path)
		editor.value = e
		editText.value = text
		showEditor.value = true
	} catch (e: any) {
		message.error(e?.message || "读取失败(可能是二进制)")
	}
}
async function saveEditor() {
	if (!editor.value) return
	editSaving.value = true
	try {
		await api.fsWrite(editor.value.path, editText.value)
		message.success("已保存")
		editor.value = null
		showEditor.value = false
		load(cwd.value)
	} finally {
		editSaving.value = false
	}
}

// ---- 文件操作 ----
async function doOp(op: any) {
	try {
		await api.fsOp(op)
		message.success("完成")
		load(cwd.value)
	} catch (e: any) {
		message.error(e?.message || "操作失败")
	}
}
function remove(e: FsEntry) {
	dialog.warning({
		title: "删除",
		content: "确定删除「" + e.name + "」?",
		positiveText: "删除",
		negativeText: "取消",
		onPositiveClick: () => doOp({ op: "delete", path: e.path }),
	})
}
function mkdirPrompt() {
	const name = window.prompt("新目录名:")
	if (name) doOp({ op: "mkdir", path: joinPath(cwd.value, name) })
}

function renamePrompt(e: FsEntry) {
	const name = window.prompt("新名称:", e.name)
	if (name && name !== e.name) {
		doOp({ op: "rename", from: e.path, to: joinPath(parentOf(e.path), name) })
	}
}
function download(e: FsEntry) {
	window.open(api.fsDownloadUrl(e.path), "_blank")
}
function downloadZip() {
	window.open(api.fsArchiveUrl(cwd.value))
}
// ---- 上传 ----
const uploadShowing = ref(false)
const uploadFiles = ref<File[]>([])
async function upload() {
	if (!uploadFiles.value.length) return
	for (const f of uploadFiles.value) {
		await api.fsUpload(cwd.value, f)
	}
	message.success("上传完成")
	load(cwd.value)
	uploadFiles.value = []
}
function onFilePick(e: any) {
	uploadFiles.value = Array.from(e.target.files || [])
	upload()
}
// ---- 排序 ----
// 目录恒定排在文件前面(不随升降序翻转),排序键只作用在同类之间。
type SortKey = "name" | "size" | "mtime"
const sortKey = ref<SortKey>("name")
const sortAsc = ref(true)
const sortOptions = [
	{ label: "名称", value: "name" },
	{ label: "大小", value: "size" },
	{ label: "修改时间", value: "mtime" },
]
const sortedEntries = computed(() => {
	const list = [...entries.value]
	list.sort((a, b) => {
		if (a.dir !== b.dir) return a.dir ? -1 : 1
		let d: number
		if (sortKey.value === "size") d = a.size - b.size
		else if (sortKey.value === "mtime") d = a.mtime.localeCompare(b.mtime)
		// numeric 让 f2 排在 f10 前面,中文按拼音。
		else d = a.name.localeCompare(b.name, "zh-Hans-CN", { numeric: true, sensitivity: "base" })
		return sortAsc.value ? d : -d
	})
	return list
})

// ---- 搜索 ----
const searchQ = ref("")
const searchMode = ref<"content" | "name">("content")
const searchRegex = ref(false)
const searchCase = ref(false)
const searching = ref(false)
const searchDone = ref(false)
const searchTruncated = ref(false)
const searchResults = ref<FsSearchResult[]>([])
const replaceText = ref("")
const showReplace = ref(false)

// 命中按文件分组:内容模式下一个文件常有多行命中。
const searchGroups = computed(() => {
	const map = new Map<string, { path: string; rel: string; dir: boolean; size: number; hits: FsSearchResult[] }>()
	for (const r of searchResults.value) {
		let g = map.get(r.path)
		if (!g) {
			g = { path: r.path, rel: r.rel, dir: r.dir, size: r.size, hits: [] }
			map.set(r.path, g)
		}
		if (r.line) g.hits.push(r)
	}
	return [...map.values()]
})

async function search() {
	if (!searchQ.value) return
	searching.value = true
	try {
		const out = await api.fsSearch({
			path: cwd.value,
			q: searchQ.value,
			mode: searchMode.value,
			regex: searchRegex.value,
			caseSensitive: searchCase.value,
			limit: 200,
		})
		searchResults.value = out.results
		searchTruncated.value = out.truncated
		searchDone.value = true
	} catch (e: any) {
		message.error(e?.message || "搜索失败")
	} finally {
		searching.value = false
	}
}

function clearSearch() {
	searchQ.value = ""
	searchResults.value = []
	searchDone.value = false
	searchTruncated.value = false
	showReplace.value = false
}

// 把命中位置切成 [普通, 高亮, ...] 片段渲染,避免 v-html。
// col/len 是后端换算好的 UTF-16 下标,可直接 slice。
function segments(r: FsSearchResult) {
	const text = r.text ?? ""
	const ms = r.matches ?? []
	if (!ms.length) return [{ text, hit: false }]
	const out: { text: string; hit: boolean }[] = []
	let pos = 0
	for (const m of ms) {
		if (m.col > pos) out.push({ text: text.slice(pos, m.col), hit: false })
		out.push({ text: text.slice(m.col, m.col + m.len), hit: true })
		pos = m.col + m.len
	}
	if (pos < text.length) out.push({ text: text.slice(pos), hit: false })
	return out
}

// 点命中项:目录进目录,文件开编辑器(沿用 512KB 上限)。
function openHit(g: { path: string; rel: string; dir: boolean; size: number }) {
	if (g.dir) {
		load(g.path)
		clearSearch()
		return
	}
	const name = g.rel.split("/").pop() || g.rel
	openEditor({ name, path: g.path, dir: false, size: g.size, mtime: "", mode: "", symlink: false })
}

function doReplace() {
	const files = searchGroups.value.filter((g) => !g.dir).map((g) => g.path)
	if (!files.length) return
	const extra = searchTruncated.value ? "\n结果已被截断,只会替换列出的文件。" : ""
	dialog.warning({
		title: "批量替换",
		content: `将在 ${files.length} 个文件中把「${searchQ.value}」替换为「${replaceText.value}」。此操作不可撤销。${extra}`,
		positiveText: "替换",
		negativeText: "取消",
		onPositiveClick: async () => {
			try {
				const res = await api.fsReplace({
					files,
					q: searchQ.value,
					replace: replaceText.value,
					regex: searchRegex.value,
					case: searchCase.value,
				})
				message.success(`已替换 ${res.count} 处,涉及 ${res.files} 个文件`)
				search()
			} catch (e: any) {
				message.error(e?.message || "替换失败")
			}
		},
	})
}
onMounted(async () => {
	await store.ensure()
	load(store.currentPath)
})
// 在别处切换工作区后回到本页,目录跟着走。
watch(() => store.currentPath, (p) => load(p))

function sizeHuman(n: number) {
	if (n < 1024) return n + " B"
	if (n < 1024 * 1024) return (n / 1024).toFixed(1) + " KB"
	return (n / 1024 / 1024).toFixed(1) + " MB"
}
function up() {
	if (atRoot.value) return
	load(parentOf(cwd.value))
}
</script>

<template>
	<div class="page-content">
		<div class="fs-header">
			<div class="fs-title">
				<h2>文件</h2>
				<div class="fs-ws">{{ store.current?.name || '根目录' }}</div>
			</div>
			<div class="fs-toolbar">
				<n-button quaternary size="small" :disabled="atRoot" aria-label="上一级" @click="up">
					<template #icon><n-icon :component="ArrowBackOutline" /></template>
				</n-button>
				<n-button quaternary size="small" aria-label="新建目录" @click="mkdirPrompt">
					<template #icon><n-icon :component="AddOutline" /></template>
				</n-button>
				<n-button quaternary size="small" aria-label="下载当前目录" @click="downloadZip">
					<template #icon><n-icon :component="DownloadOutline" /></template>
				</n-button>
				<n-button quaternary size="small" aria-label="上传" @click="uploadShowing = true">
					<template #icon><n-icon :component="CloudUploadOutline" /></template>
				</n-button>
			</div>
		</div>
		<div class="fs-crumb" :title="cwd">{{ relPath }}</div>

		<div class="fs-search">
			<n-input v-model:value="searchQ" placeholder="搜索该目录" clearable
				@keydown.enter="search" @clear="clearSearch">
				<template #prefix><n-icon :component="SearchOutline" /></template>
			</n-input>
			<div class="fs-search-opts">
				<n-radio-group v-model:value="searchMode" size="small">
					<n-radio-button value="content">内容</n-radio-button>
					<n-radio-button value="name">文件名</n-radio-button>
				</n-radio-group>
				<n-button class="opt-btn" size="tiny" :secondary="!searchCase" :type="searchCase ? 'primary' : 'default'"
					title="区分大小写" @click="searchCase = !searchCase">Aa</n-button>
				<n-button class="opt-btn" size="tiny" :secondary="!searchRegex" :type="searchRegex ? 'primary' : 'default'"
					title="正则表达式" @click="searchRegex = !searchRegex">.*</n-button>
				<div class="spacer"></div>
				<n-button size="tiny" type="primary" :loading="searching" @click="search">搜索</n-button>
			</div>
		</div>

		<div v-if="searchDone" class="search-block">
			<div class="search-head">
				<span>命中 {{ searchResults.length }} 条 / {{ searchGroups.length }} 个文件{{ searchTruncated ? ' (已截断)' : '' }}</span>
				<n-button v-if="searchMode === 'content' && searchGroups.length" size="tiny" quaternary
					@click="showReplace = !showReplace">
					<template #icon><n-icon :component="SwapHorizontalOutline" /></template>
					替换
				</n-button>
				<n-button size="tiny" quaternary aria-label="清空搜索" @click="clearSearch">
					<template #icon><n-icon :component="CloseOutline" /></template>
				</n-button>
			</div>
			<div v-if="showReplace" class="search-replace">
				<n-input v-model:value="replaceText" size="small" placeholder="替换为(留空则删除)" />
				<n-button size="small" type="error" secondary @click="doReplace">全部替换</n-button>
			</div>
			<n-empty v-if="!searchGroups.length" description="没有命中" style="padding: 16px 0" />
			<n-list v-else clickable>
				<n-list-item v-for="g in searchGroups" :key="g.path" class="search-item">
					<div class="search-path" @click="openHit(g)">
						<n-icon :component="g.dir ? FolderOpenOutline : DocumentOutline" />
						<span class="search-rel">{{ g.rel }}</span>
						<span v-if="g.hits.length" class="search-count">{{ g.hits.length }}</span>
					</div>
					<div v-for="h in g.hits" :key="h.line" class="search-line" @click="openHit(g)">
						<span class="search-lno">{{ h.line }}</span>
						<span class="search-text"><span v-for="(s, i) in segments(h)" :key="i"
							:class="{ hit: s.hit }">{{ s.text }}</span></span>
					</div>
				</n-list-item>
			</n-list>
		</div>

		<div class="fs-sort">
			<n-select v-model:value="sortKey" size="tiny" :options="sortOptions" style="width: 104px" />
			<n-button size="tiny" quaternary :aria-label="sortAsc ? '升序' : '降序'" @click="sortAsc = !sortAsc">
				<template #icon><n-icon :component="sortAsc ? ArrowUpOutline : ArrowDownOutline" /></template>
			</n-button>
			<div class="spacer"></div>
			<span class="fs-count">{{ entries.length }} 项</span>
		</div>

		<n-spin :show="loading">
			<div v-if="sortedEntries.length" class="fs-list">
				<div v-for="e in sortedEntries" :key="e.path" class="fs-item" role="button" tabindex="0"
					@click="openEntry(e)" @keydown.enter="openEntry(e)">
					<n-icon class="fs-ico" :component="e.dir ? FolderOpenOutline : DocumentOutline" />
					<div class="fs-name">
						<span class="fs-name-text">{{ e.name }}</span>
						<n-tag v-if="e.symlink" size="small" type="warning" :bordered="false">link</n-tag>
					</div>
					<span class="fs-size">{{ e.dir ? "" : sizeHuman(e.size) }}</span>
					<div class="fs-actions">
						<n-button v-if="!e.dir" class="fs-btn" size="tiny" quaternary aria-label="下载" @click.stop="download(e)">
							<n-icon :component="DownloadOutline" /></n-button>
						<n-button class="fs-btn" size="tiny" quaternary aria-label="重命名" @click.stop="renamePrompt(e)">
							<n-icon :component="CreateOutline" /></n-button>
						<n-button class="fs-btn" size="tiny" quaternary type="error" aria-label="删除" @click.stop="remove(e)">
							<n-icon :component="TrashOutline" /></n-button>
					</div>
				</div>
			</div>
			<n-empty v-else description="空目录" style="padding: 40px" />
		</n-spin>
		<!-- 编辑器 -->
		<n-modal v-model:show="showEditor" preset="card" :title="editor?.name">
			<n-input v-model:value="editText" type="textarea" :autosize="{ minRows: 12, maxRows: 20 }" />
			<template #footer>
				<div class="modal-footer">
					<n-button @click="showEditor = false">关闭</n-button>
					<n-button type="primary" :loading="editSaving" @click="saveEditor">保存</n-button>
				</div>
			</template>
		</n-modal>

		<!-- 上传 -->
		<n-modal v-model:show="uploadShowing" preset="card" title="上传文件">
			<input type="file" :multiple="true" class="file-input" @change="onFilePick" />
			<div v-if="uploadFiles.length" class="upload-list">待上传: {{ uploadFiles.map(f => f.name).join(", ") }}</div>
			<template #footer><n-button @click="uploadShowing = false">关闭</n-button></template>
		</n-modal>
	</div>
</template>

<style scoped>
.fs-header { display: flex; align-items: flex-start; justify-content: space-between; margin-bottom: 8px; }
.fs-header h2 { margin: 0; font-size: 20px; }
.fs-ws { color: var(--lr-fg-muted); font-size: 12px; margin-top: 2px; }
.fs-toolbar { display: flex; gap: 2px; }
.fs-crumb {
	color: var(--lr-fg-muted); font-size: 12px; font-family: ui-monospace, monospace;
	margin: 0 0 8px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
	direction: rtl; text-align: left;
}
.fs-list { display: flex; flex-direction: column; gap: 6px; margin-top: 10px; }
.fs-item {
	display: flex; align-items: center; gap: 10px;
	padding: 10px 12px;
	background: var(--lr-bg-elevated);
	border: 1px solid rgba(127, 127, 127, 0.14);
	border-radius: var(--lr-radius);
	cursor: pointer;
}
.fs-ico { flex: none; font-size: 18px; color: var(--lr-accent); }
.fs-name { flex: 1; min-width: 0; display: flex; align-items: center; gap: 6px; font-weight: 500; }
.fs-name-text { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.fs-size { flex: none; color: var(--lr-fg-muted); font-size: 12px; }
.fs-actions { flex: none; display: flex; align-items: center; gap: 2px; }
/* 覆盖全局 .n-button 的 44px 触控下限,否则行会被撑高 */
.fs-btn { min-height: 28px; height: 28px; width: 28px; }
.search-path {
	display: flex; align-items: center; gap: 6px;
	font-weight: 600; font-size: 13px; cursor: pointer;
}
.search-rel { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.search-count {
	flex: none; color: var(--lr-fg-muted); font-size: 11px;
	background: rgba(127,127,127,.16); border-radius: 8px; padding: 0 6px;
}
.search-line {
	display: flex; gap: 8px; align-items: baseline;
	margin-top: 2px; cursor: pointer;
	font-family: ui-monospace, monospace; font-size: 12px;
}
.search-lno { flex: none; color: var(--lr-fg-muted); min-width: 32px; text-align: right; }
.search-text {
	flex: 1; min-width: 0; color: var(--lr-fg-muted);
	overflow: hidden; text-overflow: ellipsis; white-space: pre;
}
.search-text .hit {
	background: rgba(250, 204, 21, .35);
	color: var(--lr-fg); border-radius: 2px; font-weight: 600;
}
.fs-search { display: flex; flex-direction: column; gap: 6px; }
.fs-search-opts { display: flex; align-items: center; gap: 6px; }
.opt-btn { min-height: 24px; height: 24px; font-family: ui-monospace, monospace; }
.spacer { flex: 1; }
.search-block {
	margin-top: 8px; padding: 8px;
	border: 1px solid rgba(127,127,127,.2); border-radius: var(--lr-radius);
	max-height: 46vh; overflow: auto;
}
.search-head {
	display: flex; align-items: center; gap: 6px;
	color: var(--lr-fg-muted); font-size: 12px;
}
.search-head span { flex: 1; }
.search-replace { display: flex; gap: 6px; margin: 6px 0; }
.fs-sort { display: flex; align-items: center; gap: 6px; margin-top: 10px; }
.fs-count { color: var(--lr-fg-muted); font-size: 12px; }
.modal-footer { display: flex; justify-content: flex-end; gap: 8px; }
.file-input { font-size: 13px; }
.upload-list { margin-top: 8px; color: var(--lr-fg-muted); font-size: 12px; }
</style>
