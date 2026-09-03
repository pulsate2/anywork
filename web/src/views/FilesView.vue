<script setup lang="ts">
// 文件浏览:列表/读写/上传下载/搜索/压缩解压。桌面+移动自适应。
import { ref, onMounted, computed, watch, nextTick, h, type Component } from "vue"
import { useRouter } from "vue-router"
import {
	NButton, NIcon, NEmpty, NSpin,
	NInput, NModal, useMessage, useDialog,
	NTag, NList, NListItem, NSelect, NRadioGroup, NRadioButton, NCheckbox,
	NDropdown, type DropdownOption, type DropdownDividerOption,
} from "naive-ui"
import { ArrowBackOutline,
	CreateOutline, TrashOutline, SearchOutline, DownloadOutline,
	CloudUploadOutline, AddOutline, ArrowUpOutline, ArrowDownOutline,
	SwapHorizontalOutline, CloseOutline, EllipsisVerticalOutline,
	CopyOutline, ArrowForwardOutline, ClipboardOutline,
	CheckboxOutline, ArchiveOutline, FolderOpenOutline } from "@vicons/ionicons5"
import { api, type FsEntry, type FsOp, type FsSearchResult } from "@/api/client"
import { fileIcon, isArchivePath, isImagePath } from "@/utils/fileIcon"
import { copyText } from "@/utils/clipboard"
import { useWorkspaceStore } from "@/stores/workspace"
import DirTreePicker from "@/components/DirTreePicker.vue"

const message = useMessage()
const dialog = useDialog()
const store = useWorkspaceStore()
const router = useRouter()
const cwd = ref("/")
const entries = ref<FsEntry[]>([])
const loading = ref(false)

// 后端返回的都是 root 内的绝对路径(正斜杠)。root 去掉尾斜杠便于前缀比较。
const rootPath = computed(() => trimSlash(store.root))
const atRoot = computed(() => trimSlash(cwd.value) === rootPath.value)
// 面包屑分段:每段带上自己对应的绝对路径,点击即可跳到该层。
const crumbs = computed(() => {
	const base = rootPath.value
	const c = trimSlash(cwd.value)
	const rel = c.startsWith(base) ? c.slice(base.length) : c
	const out = [{ name: "/", path: base || "/" }]
	let acc = base
	for (const part of rel.split("/").filter(Boolean)) {
		acc = (acc === "/" ? "" : acc) + "/" + part
		out.push({ name: part, path: acc })
	}
	return out
})

const crumbEl = ref<HTMLElement | null>(null)
// 长路径横向滚动:目录变化后滚到末尾,保证当前目录始终可见。
watch(crumbs, () => {
	nextTick(() => {
		if (crumbEl.value) crumbEl.value.scrollLeft = crumbEl.value.scrollWidth
	})
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
	// root 本身就是 "/" 时不能再补一道斜杠,否则拼出 "//x",复制/移动的同路径判断会对不上。
	const d = trimSlash(dir)
	return (d === "/" ? "" : d) + "/" + name
}

// 恢复上次访问的目录:必须仍在当前工作区边界之内,否则丢弃记录。
function initialDir(): string {
	const last = store.lastDir()
	if (!last) return store.currentPath
	const base = trimSlash(store.currentPath)
	const t = trimSlash(last)
	if (t === base || t.startsWith(base === "/" ? "/" : base + "/")) return last
	store.clearLastDir()
	return store.currentPath
}

async function load(dir: string) {
	const fallback = store.currentPath
	let target = dir || fallback
	loading.value = true
	try {
		try {
			entries.value = await api.fsList(target)
		} catch (e) {
			// 记住的目录可能已经被删掉了:静默回落到工作区根,别把用户留在错误页上。
			if (target === fallback) throw e
			store.clearLastDir()
			target = fallback
			entries.value = await api.fsList(target)
		}
		cwd.value = target
		// 勾选存的是绝对 path,换目录(或刷新当前目录)后旧的全部失效,留着会删错东西。
		selected.value.clear()
		store.setLastDir(target)
	} catch (e: any) {
		message.error(e?.message || "加载失败")
	} finally {
		loading.value = false
	}
}

function openEntry(e: FsEntry) {
	if (e.dir) load(e.path)
	else openFilePage(e)
}

// 文件点进单独预览/编辑页(二级页面,不再用模态编辑器)。
// 大小提示只对文本有意义:图片和压缩包在二级页本来就只能预览,提了反而像是本该能编辑。
function openFilePage(e: { path: string; name: string; size: number }) {
	const editable = !isImagePath(e.path) && !isArchivePath(e.path)
	if (editable && e.size > 512 * 1024) {
		message.warning("文件过大,仅支持预览,不可编辑(512KB 内)")
	}
	router.push({ path: "/files/file", query: { path: e.path, name: e.name } })
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
// ---- 批量执行 ----
// doOp 每次都弹 toast + reload,放进循环会刷屏也会刷爆列表。批量走这条:
// 串行跑完再汇总一次。串行而不并发:后端是同一个进程在动同一棵目录树,
// 并发只会让"哪几项失败了"更难归因,几十项的量级串行也够快。
const batchRunning = ref(false)
const batchDone = ref(0)
const batchTotal = ref(0)

// 名字最多列 3 个,再多了只报数 —— toast 太长在手机上会糊满半屏。
function names(list: string[]) {
	return list.length > 3 ? `${list.slice(0, 3).join("、")} 等 ${list.length} 项` : list.join("、")
}

async function runBatch(verb: string, items: FsEntry[], make: (e: FsEntry) => FsOp) {
	if (!items.length || batchRunning.value) return
	batchRunning.value = true
	batchDone.value = 0
	batchTotal.value = items.length
	const failed: string[] = []
	let firstErr = ""
	for (const e of items) {
		try {
			await api.fsOp(make(e))
		} catch (err: any) {
			failed.push(e.name)
			if (!firstErr) firstErr = err?.message || ""
		}
		batchDone.value++
	}
	batchRunning.value = false
	const ok = items.length - failed.length
	if (!failed.length) {
		message.success(items.length === 1 ? `已${verb}「${items[0].name}」` : `已${verb} ${ok} 项`)
	} else if (items.length === 1) {
		// 单条时把后端的原话透出来,这是用户唯一能拿到的线索。
		message.error(firstErr || `${verb}失败`)
	} else if (!ok) {
		message.error(`${verb}失败:${names(failed)}${firstErr ? `(${firstErr})` : ""}`)
	} else {
		message.warning(`已${verb} ${ok} 项,${failed.length} 项失败:${names(failed)}`)
	}
	load(cwd.value)
}

// ---- 删除 ----
// 后端是 os.RemoveAll(internal/fs/ops.go),目录连整棵子树一起删,没有回收站也没有 undo,
// 所以和 Git 的「撤回改动」对齐,过两道:第一道说清删的是什么,第二道只强调不可撤销。
// 不去数目录里有多少条:那要多打一次 fsList 还得处理它失败,文案点明"连里面的全部内容"
// 已经把风险讲清了。
function deleteScope(items: FsEntry[]) {
	if (items.length === 1) {
		const e = items[0]
		return e.dir
			? `将删除文件夹「${e.name}」,连里面的全部内容一起删。`
			: `将删除「${e.name}」。`
	}
	const dirs = items.filter((e) => e.dir).length
	const files = items.length - dirs
	const parts: string[] = []
	if (files) parts.push(`${files} 个文件`)
	if (dirs) parts.push(`${dirs} 个文件夹`)
	return `将删除 ${parts.join("、")}${dirs ? "(文件夹会连里面的全部内容一起删)" : ""}。`
}

function confirmDelete(items: FsEntry[]) {
	if (!items.length) return
	const what = items.length === 1 ? `「${items[0].name}」` : `这 ${items.length} 项`
	dialog.warning({
		title: "删除",
		content: deleteScope(items) + "删除后无法恢复。",
		positiveText: "继续",
		negativeText: "取消",
		onPositiveClick: () => {
			dialog.error({
				title: "再次确认",
				content: `确定删除${what}?此操作不可撤销。`,
				positiveText: "确定删除",
				negativeText: "返回",
				onPositiveClick: () => runBatch("删除", items, (e) => ({ op: "delete", path: e.path })),
			})
		},
	})
}
// ---- 新建 / 重命名 ----
// 用自定义弹窗而不是 window.prompt:系统弹窗在移动端会打断页面焦点、样式也不可控。
// 新建与重命名共用一个弹窗,差别只有标题、是否显示类型选择、以及提交时走哪个 op。
const nameShowing = ref(false)
const nameMode = ref<"new" | "rename">("new")
const nameKind = ref<"dir" | "file">("dir")
const nameValue = ref("")
const nameTarget = ref<FsEntry | null>(null)
const nameInput = ref<InstanceType<typeof NInput> | null>(null)
// 名称里出现分隔符会绕过 cwd 拼接跑到别的目录去,直接拦掉(后端 Resolve 兜底,这里只是即时反馈)。
const nameInvalid = computed(() => /[\\/]/.test(nameValue.value))

function openNew() {
	nameMode.value = "new"
	nameKind.value = "dir"
	nameValue.value = ""
	nameTarget.value = null
	nameShowing.value = true
}

function openRename(e: FsEntry) {
	nameMode.value = "rename"
	nameValue.value = e.name
	nameTarget.value = e
	nameShowing.value = true
}

// 弹窗有进场动画,挂载完成前 focus 会落空,所以挂到 n-modal 的 after-enter 上。
function focusName() {
	nameInput.value?.focus()
}

function submitName() {
	const name = nameValue.value.trim()
	if (!name || nameInvalid.value) return
	nameShowing.value = false
	if (nameMode.value === "rename") {
		const e = nameTarget.value
		if (!e || name === e.name) return
		doOp({ op: "rename", from: e.path, to: joinPath(parentOf(e.path), name) })
		return
	}
	doOp({ op: nameKind.value === "dir" ? "mkdir" : "touch", path: joinPath(cwd.value, name) })
}
function download(e: FsEntry) {
	window.open(api.fsDownloadUrl(e.path), "_blank")
}
function downloadZip() {
	window.open(api.fsArchiveUrl(cwd.value))
}
// ---- 复制路径 ----
// 相对路径以工作区根为基准:终端就是在这个目录起的,Git 也以它为仓库根,
// 复制出来的路径粘到命令行里直接能用。没选工作区时它等于服务端根目录。
// 面包屑能一路走到工作区上面去,那时用 ../ 回退 —— 一个叫"相对路径"的菜单项
// 不该突然吐出绝对路径。
function relativeTo(base: string, target: string): string {
	const b = trimSlash(base).split("/").filter(Boolean)
	const t = trimSlash(target).split("/").filter(Boolean)
	let i = 0
	while (i < b.length && i < t.length && b[i] === t[i]) i++
	const up: string[] = Array(b.length - i).fill("..")
	return [...up, ...t.slice(i)].join("/") || "."
}

async function copyPath(e: FsEntry, kind: "rel" | "abs") {
	const text = kind === "abs" ? trimSlash(e.path) : relativeTo(store.currentPath, e.path)
	const label = kind === "abs" ? "绝对路径" : "相对路径"
	if (await copyText(text)) message.success(`已复制${label}`)
	// 写不进剪贴板时(非安全上下文 + execCommand 也被拦)至少把内容摆出来。
	else message.warning(`浏览器不给写剪贴板:${text}`)
}

// ---- 批量选择 ----
// 入口是工具栏那个开关钮。开着的时候整行点击变成勾选、三点菜单收起来,
// 一行仍然只有一个触控目标(手指粗,别在一行里塞两个热区)。
const selectMode = ref(false)
const selected = ref<Set<string>>(new Set())
// 只认当前目录里还在的项:选完之后别人删掉了文件,刷新后勾选自动作废。
const selectedEntries = computed(() => sortedEntries.value.filter((e) => selected.value.has(e.path)))
const allSelected = computed(() =>
	sortedEntries.value.length > 0 && selectedEntries.value.length === sortedEntries.value.length)
const batchDisabled = computed(() => !selectedEntries.value.length || batchRunning.value)

function toggleSelectMode() {
	selectMode.value = !selectMode.value
	if (!selectMode.value) selected.value.clear()
}
function toggleSelect(e: FsEntry) {
	if (selected.value.has(e.path)) selected.value.delete(e.path)
	else selected.value.add(e.path)
}
function toggleSelectAll() {
	if (allSelected.value) selected.value.clear()
	else for (const e of sortedEntries.value) selected.value.add(e.path)
}
function onRowClick(e: FsEntry) {
	if (selectMode.value) toggleSelect(e)
	else openEntry(e)
}

// ---- 行操作菜单 ----
// 全部收进三点菜单:一行只留一个触控目标,移动端不会误点到删除。
// 只读的(下载/复制路径)放前面,改动文件的放后面,中间用分隔线隔开。
function rowMenu(e: FsEntry): Array<DropdownOption | DropdownDividerOption> {
	const icon = (c: Component, color?: string) => () => h(NIcon, { component: c, color })
	const out: Array<DropdownOption | DropdownDividerOption> = []
	if (!e.dir) out.push({ key: "download", label: "下载", icon: icon(DownloadOutline) })
	out.push({ key: "copy-rel", label: "复制相对路径", icon: icon(ClipboardOutline) })
	out.push({ key: "copy-abs", label: "复制绝对路径", icon: icon(ClipboardOutline) })
	out.push({ key: "sep-1", type: "divider" })
	out.push({ key: "rename", label: "重命名", icon: icon(CreateOutline) })
	out.push({ key: "copy", label: "复制到…", icon: icon(CopyOutline) })
	out.push({ key: "move", label: "移动到…", icon: icon(ArrowForwardOutline) })
	// 解压只对认得的压缩包给;压缩只对文件夹给 —— 单个文件单独压意义不大,
	// 真要压就进选择模式勾上它,批量那条路本来就通。
	if (!e.dir && isArchivePath(e.path)) out.push({ key: "extract", label: "解压", icon: icon(FolderOpenOutline) })
	if (e.dir) out.push({ key: "zip", label: "压缩…", icon: icon(ArchiveOutline) })
	out.push({ key: "sep-2", type: "divider" })
	// 弹层 teleport 到 body,scoped 样式选不中,危险色只能内联。
	out.push({
		key: "delete", label: "删除",
		icon: icon(TrashOutline, "var(--lr-danger)"),
		props: { style: { color: "var(--lr-danger)" } },
	})
	return out
}

function onRowMenu(key: string | number, e: FsEntry) {
	if (key === "download") download(e)
	else if (key === "copy-rel") copyPath(e, "rel")
	else if (key === "copy-abs") copyPath(e, "abs")
	else if (key === "rename") openRename(e)
	else if (key === "copy") openTransfer("copy", [e])
	else if (key === "move") openTransfer("move", [e])
	else if (key === "extract") extract(e)
	else if (key === "zip") openZip([e])
	else if (key === "delete") confirmDelete([e])
}

// ---- 复制 / 移动 ----
// 选目标目录用的是新建工作区那套目录树,默认展开到当前目录。
// 单条(行菜单)和批量(操作条)都走这里,单条就是长度 1 的一组,只留一条代码路径。
const transferShowing = ref(false)
const transferMode = ref<"copy" | "move">("copy")
const transferItems = ref<FsEntry[]>([])
const transferDir = ref("")
const transferChecking = ref(false)

function openTransfer(mode: "copy" | "move", items: FsEntry[]) {
	if (!items.length) return
	transferMode.value = mode
	transferItems.value = [...items]
	transferDir.value = cwd.value
	transferShowing.value = true
}

const transferDest = computed(() => trimSlash(transferDir.value.trim()))
const transferNames = computed(() => names(transferItems.value.map((e) => e.name)))
// 后端 Copy/Move 的 dst 是含最终名字的完整路径,不是父目录:每项各自拼一个。
// 一项时把落点全路径显示出来,多项时只报目标目录 —— 逐条铺开占不下。
const transferTo = computed(() => {
	const dir = transferDest.value
	const list = transferItems.value
	if (!dir || !list.length) return ""
	return list.length === 1 ? joinPath(dir, list[0].name) : dir
})

// 后端不挡这两种:原地复制会被 os.Create 截成空文件;目录进自己的子树会无限递归。
// 批量里只要有一项犯规就整个禁掉,并指名是哪一项 —— 否则用户只能一个个试。
const transferError = computed(() => {
	const dir = transferDest.value
	const list = transferItems.value
	if (!dir || !list.length) return ""
	const who = (e: FsEntry) => (list.length === 1 ? "" : `(${e.name})`)
	for (const e of list) {
		const from = trimSlash(e.path)
		const to = joinPath(dir, e.name)
		if (to === from) return "目标目录就是当前位置" + who(e)
		if (e.dir && to.startsWith(from + "/")) return "不能放进自己的子目录" + who(e)
	}
	return ""
})

async function submitTransfer() {
	const list = transferItems.value
	const dir = transferDest.value
	if (!list.length || !dir || transferError.value) return
	// 同名会被直接盖掉(copy 走 os.Create,move 在 Linux 上也是覆盖),先问一句。
	// 只打一次 fsList:拿目标目录的名字集跟所选名字求交集。
	transferChecking.value = true
	let clash: string[] = []
	try {
		const there = new Set((await api.fsList(dir)).map((it) => it.name))
		clash = list.filter((e) => there.has(e.name)).map((e) => e.name)
	} catch {
		// 目标目录读不到就别拦,让后端去报真正的错
	} finally {
		transferChecking.value = false
	}
	const verb = transferMode.value === "copy" ? "复制" : "移动"
	const op = transferMode.value
	const run = () => runBatch(verb, list, (e) => ({ op, from: e.path, to: joinPath(dir, e.name) }))
	transferShowing.value = false
	if (!clash.length) {
		run()
		return
	}
	dialog.warning({
		title: "目标已存在",
		content: clash.length === 1
			? `目标目录里已有「${clash[0]}」,继续会覆盖它。`
			: `目标目录里已有 ${clash.length} 个同名项(${names(clash)}),继续会覆盖它们。`,
		positiveText: "覆盖" + verb,
		negativeText: "取消",
		onPositiveClick: run,
	})
}
// ---- 压缩 ----
// 行菜单里只给文件夹,批量选择时在操作条上给。落点是当前目录,名字和格式可选。
// 后端一次收下所有源(不像复制/移动那样一项一个请求):包只能一次写完,
// 中途失败就整个不留,没有"压了一半"这种状态。
//
// 能压的格式比能解的少:bz2 只有解码器,7z/rar 没有能用的纯 Go 写入实现。
// 后端认的是 dest 的后缀,所以这里选中的格式就是补在名字后面的那截后缀。
const zipFormats = ["zip", "tar.gz", "tar.xz", "tar"] as const
const zipShowing = ref(false)
const zipItems = ref<FsEntry[]>([])
const zipName = ref("")
// 不随弹窗重置:连着压几个包时通常是同一种格式。
const zipFormat = ref<(typeof zipFormats)[number]>("zip")
const zipRunning = ref(false)
const zipInput = ref<InstanceType<typeof NInput> | null>(null)
const zipInvalid = computed(() => /[\\/]/.test(zipName.value))
const zipItemNames = computed(() => names(zipItems.value.map((e) => e.name)))
// 输入框里只放名字,后缀由格式决定 —— 名字和实际格式不会打架。
const zipDest = computed(() => {
	const n = stripArchiveExt(zipName.value.trim())
	return n ? joinPath(cwd.value, n + "." + zipFormat.value) : ""
})

// 只去掉认得的包后缀:note.txt 原样留着(压出 note.txt.zip,和 macOS 一致),
// old.tar.gz 改压成 zip 时不会变成 old.tar.gz.zip。双后缀要排在单后缀前面。
const archiveExts = [
	".tar.gz", ".tar.bz2", ".tar.xz", ".tgz", ".tbz2", ".tbz", ".txz",
	".zip", ".tar", ".7z", ".rar", ".gz", ".bz2", ".xz",
]
function stripArchiveExt(name: string) {
	const l = name.toLowerCase()
	for (const ext of archiveExts) {
		if (l.endsWith(ext) && name.length > ext.length) return name.slice(0, -ext.length)
	}
	return name
}

function openZip(items: FsEntry[]) {
	if (!items.length || zipRunning.value) return
	zipItems.value = [...items]
	// 一项用它自己的名字,多项用当前目录名 —— 跟桌面文管的习惯一致。
	zipName.value = items.length === 1
		? items[0].name
		: trimSlash(cwd.value).split("/").filter(Boolean).pop() || "archive"
	zipShowing.value = true
}

function focusZip() {
	zipInput.value?.focus()
}

function submitZip() {
	const items = zipItems.value
	const dest = zipDest.value
	if (!items.length || !dest || zipInvalid.value || zipRunning.value) return
	const name = dest.slice(dest.lastIndexOf("/") + 1)
	zipShowing.value = false
	const run = async () => {
		zipRunning.value = true
		loading.value = true
		try {
			await api.fsCompress(dest, items.map((e) => e.path))
			message.success(`已压缩到「${name}」`)
		} catch (e: any) {
			message.error(e?.message || "压缩失败")
		} finally {
			zipRunning.value = false
			loading.value = false
		}
		load(cwd.value)
	}
	// 包名撞上现有条目:同名文件会被换掉,同名文件夹后端根本写不进去。
	const clash = entries.value.find((e) => e.name === name)
	if (!clash) {
		run()
		return
	}
	if (clash.dir) {
		message.error(`同名文件夹「${name}」已存在,换个包名`)
		return
	}
	dialog.warning({
		title: "目标已存在",
		content: `当前目录里已有「${name}」,继续会覆盖它。`,
		positiveText: "覆盖",
		negativeText: "取消",
		onPositiveClick: run,
	})
}

// ---- 解压 ----
// 解到同目录下一个以包名命名的文件夹里(note.tar.gz → note/),不直接铺在当前目录:
// 包里若有几十个顶层文件,铺开就收不回来了。单文件压缩(note.txt.gz)例外,
// 里面就一个文件,套一层反而多余 —— 和 gunzip 的行为一致。
const extracting = ref(false)

// 双后缀要整段去掉,否则 note.tar.gz 会解到 note.tar/ 里。
function archiveBase(name: string) {
	const lower = name.toLowerCase()
	for (const s of [".tar.gz", ".tar.bz2", ".tar.xz"]) {
		if (lower.endsWith(s)) return name.slice(0, -s.length)
	}
	const dot = name.lastIndexOf(".")
	return dot > 0 ? name.slice(0, dot) : name
}

// .gz/.bz2/.xz 是"压一个文件",.tar.gz / .tgz 这些是"压一包文件"。
function isSingleFileArchive(name: string) {
	const l = name.toLowerCase()
	if (/\.tar\.(gz|bz2|xz)$/.test(l)) return false
	return /\.(gz|bz2|xz)$/.test(l)
}

// 单文件压缩解出来的名字:和后端 extractSingle 一致(去掉最后一段后缀,空则 data)。
function singleTarget(name: string) {
	const dot = name.lastIndexOf(".")
	return (dot > 0 ? name.slice(0, dot) : "") || "data"
}

async function runExtract(e: FsEntry, dest: string, label: string) {
	if (extracting.value) return
	extracting.value = true
	// 行菜单里没有按钮可以挂 loading,借列表那圈 spin 当进度提示。
	loading.value = true
	try {
		await api.fsExtract(dest, e.path)
		message.success(`已解压到「${label}」`)
	} catch (err: any) {
		message.error(err?.message || "解压失败")
	} finally {
		extracting.value = false
		loading.value = false
	}
	// 失败也刷:解压不是原子的,可能已经落了一部分文件,列表得说实话。
	load(cwd.value)
}

function extract(e: FsEntry) {
	if (extracting.value) return
	if (isSingleFileArchive(e.name)) {
		const target = singleTarget(e.name)
		const clash = entries.value.find((it) => it.name === target)
		if (clash?.dir) {
			message.error(`同名文件夹「${target}」已存在,请先重命名它`)
			return
		}
		if (!clash) {
			runExtract(e, cwd.value, target)
			return
		}
		dialog.warning({
			title: "文件已存在",
			content: `解压会覆盖当前目录里的「${target}」。`,
			positiveText: "覆盖",
			negativeText: "取消",
			onPositiveClick: () => runExtract(e, cwd.value, target),
		})
		return
	}
	const folder = archiveBase(e.name)
	const dest = joinPath(cwd.value, folder)
	const clash = entries.value.find((it) => it.name === folder)
	// 同名文件挡在那儿:后端要在这个名字上建目录,只会报错,不如提前说清。
	if (clash && !clash.dir) {
		message.error(`同名文件「${folder}」已存在,请先重命名它`)
		return
	}
	if (!clash) {
		runExtract(e, dest, folder + "/")
		return
	}
	dialog.warning({
		title: "文件夹已存在",
		content: `将解压到已有的「${folder}」里,里面的同名文件会被覆盖。`,
		positiveText: "继续解压",
		negativeText: "取消",
		onPositiveClick: () => runExtract(e, dest, folder + "/"),
	})
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

// 点命中项:目录进目录,文件跳单独文件页(带上行号让它定位到命中行)。
// 关键词和开关一并带过去:二级页要把命中标出来,而不是只把视口滚到那一行。
// 只有内容模式才带 —— 文件名模式的关键词未必出现在正文里,标出来只会误导。
function openHit(g: { path: string; rel: string; dir: boolean; size: number }, line?: number) {
	if (g.dir) {
		load(g.path)
		clearSearch()
		return
	}
	const query: Record<string, string> = { path: g.path, name: g.rel }
	if (line) query.line = String(line)
	if (searchMode.value === "content" && searchQ.value) {
		query.q = searchQ.value
		if (searchRegex.value) query.regex = "1"
		if (searchCase.value) query.case = "1"
	}
	router.push({ path: "/files/file", query })
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
	load(initialDir())
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
				<n-button quaternary size="small" :type="selectMode ? 'primary' : 'default'"
					:title="selectMode ? '退出选择' : '批量选择'" :aria-label="selectMode ? '退出选择' : '批量选择'"
					:aria-pressed="selectMode" @click="toggleSelectMode">
					<template #icon><n-icon :component="CheckboxOutline" /></template>
				</n-button>
				<n-button quaternary size="small" aria-label="新建" @click="openNew">
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
		<div ref="crumbEl" class="fs-crumb" :title="cwd">
			<template v-for="(c, i) in crumbs" :key="c.path">
				<span v-if="i > 1" class="crumb-sep">/</span>
				<button v-if="i < crumbs.length - 1" class="crumb-seg" @click="load(c.path)">{{ c.name }}</button>
				<span v-else class="crumb-seg current">{{ c.name }}</span>
			</template>
		</div>

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
						<n-icon :component="fileIcon(g.path, g.dir).icon" :color="fileIcon(g.path, g.dir).color" />
						<span class="search-rel">{{ g.rel }}</span>
						<span v-if="g.hits.length" class="search-count">{{ g.hits.length }}</span>
					</div>
					<div v-for="h in g.hits" :key="h.line" class="search-line" @click="openHit(g, h.line)">
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

		<!-- 批量操作条:吸顶在列表上方,长列表滚下去按钮也还在手边 -->
		<div v-if="selectMode" class="fs-batch">
			<n-checkbox :checked="allSelected" :indeterminate="!allSelected && selectedEntries.length > 0"
				:disabled="!sortedEntries.length || batchRunning" @update:checked="toggleSelectAll">全选</n-checkbox>
			<span class="fs-batch-n">{{ batchRunning
				? `${batchDone}/${batchTotal}`
				: `已选 ${selectedEntries.length} 项` }}</span>
			<div class="spacer"></div>
			<div class="fs-batch-ops">
				<n-button size="tiny" secondary :disabled="batchDisabled" @click="openTransfer('copy', selectedEntries)">
					<template #icon><n-icon :component="CopyOutline" /></template>复制
				</n-button>
				<n-button size="tiny" secondary :disabled="batchDisabled" @click="openTransfer('move', selectedEntries)">
					<template #icon><n-icon :component="ArrowForwardOutline" /></template>移动
				</n-button>
				<n-button size="tiny" secondary :disabled="batchDisabled || zipRunning" @click="openZip(selectedEntries)">
					<template #icon><n-icon :component="ArchiveOutline" /></template>压缩
				</n-button>
				<n-button size="tiny" type="error" secondary :disabled="batchDisabled" @click="confirmDelete(selectedEntries)">
					<template #icon><n-icon :component="TrashOutline" /></template>删除
				</n-button>
			</div>
		</div>

		<n-spin :show="loading">
			<div v-if="sortedEntries.length" class="fs-list">
				<div v-for="e in sortedEntries" :key="e.path" class="fs-item"
					:class="{ picked: selectMode && selected.has(e.path) }"
					:role="selectMode ? 'checkbox' : 'button'"
					:aria-checked="selectMode ? selected.has(e.path) : undefined"
					tabindex="0" @click="onRowClick(e)" @keydown.enter="onRowClick(e)">
					<!-- 纯视觉指示:pointer-events 关掉,点哪儿都由整行接管,免得点在框上切两次 -->
					<n-checkbox v-if="selectMode" class="fs-check" :checked="selected.has(e.path)" :focusable="false" />
					<n-icon class="fs-ico" :component="fileIcon(e.path, e.dir).icon" :color="fileIcon(e.path, e.dir).color" />
					<div class="fs-name">
						<span class="fs-name-text">{{ e.name }}</span>
						<n-tag v-if="e.symlink" size="small" type="warning" :bordered="false">link</n-tag>
					</div>
					<span class="fs-size">{{ e.dir ? "" : sizeHuman(e.size) }}</span>
					<div v-if="!selectMode" class="fs-actions">
						<n-dropdown trigger="click" placement="bottom-end" size="large" :options="rowMenu(e)"
							@select="(k) => onRowMenu(k, e)">
							<n-button class="fs-btn" size="tiny" quaternary aria-label="更多操作" @click.stop>
								<n-icon :component="EllipsisVerticalOutline" /></n-button>
						</n-dropdown>
					</div>
				</div>
			</div>
			<n-empty v-else description="空目录" style="padding: 40px" />
		</n-spin>
		<!-- 上传 -->
		<n-modal v-model:show="uploadShowing" preset="card" title="上传文件">
			<input type="file" :multiple="true" class="file-input" @change="onFilePick" />
			<div v-if="uploadFiles.length" class="upload-list">待上传: {{ uploadFiles.map(f => f.name).join(", ") }}</div>
			<template #footer><n-button @click="uploadShowing = false">关闭</n-button></template>
		</n-modal>
		<!-- 新建 / 重命名 -->
		<n-modal v-model:show="nameShowing" preset="card" class="name-modal"
			:title="nameMode === 'new' ? '新建' : '重命名'" @after-enter="focusName">
			<n-radio-group v-if="nameMode === 'new'" v-model:value="nameKind" size="small" class="name-kind">
				<n-radio-button value="dir">文件夹</n-radio-button>
				<n-radio-button value="file">文件</n-radio-button>
			</n-radio-group>
			<n-input ref="nameInput" v-model:value="nameValue" :status="nameInvalid ? 'error' : undefined"
				:placeholder="nameMode === 'rename' ? '新名称' : nameKind === 'dir' ? '文件夹名' : '文件名,如 note.md'"
				@keydown.enter="submitName" />
			<div v-if="nameInvalid" class="name-err">名称不能包含 / 或 \</div>
			<template #footer>
				<div class="modal-footer">
					<n-button @click="nameShowing = false">取消</n-button>
					<n-button type="primary" :disabled="!nameValue.trim() || nameInvalid" @click="submitName">确定</n-button>
				</div>
			</template>
		</n-modal>
		<!-- 复制 / 移动 -->
		<n-modal v-model:show="transferShowing" preset="card" class="dir-modal"
			:title="transferMode === 'copy' ? '复制到' : '移动到'">
			<div class="transfer-src">{{ transferItems.length === 1
				? transferItems[0].name
				: `已选 ${transferItems.length} 项:${transferNames}` }}</div>
			<dir-tree-picker v-model="transferDir" placeholder="目标目录" default-open />
			<div class="transfer-to" :class="{ bad: !!transferError }">{{ transferError || transferTo }}</div>
			<template #footer>
				<div class="modal-footer">
					<n-button @click="transferShowing = false">取消</n-button>
					<n-button type="primary" :loading="transferChecking" :disabled="!transferTo || !!transferError"
						@click="submitTransfer">{{ transferMode === 'copy' ? '复制' : '移动' }}</n-button>
				</div>
			</template>
		</n-modal>
		<!-- 压缩 -->
		<n-modal v-model:show="zipShowing" preset="card" class="name-modal" title="压缩" @after-enter="focusZip">
			<div class="transfer-src">{{ zipItems.length === 1
				? zipItems[0].name
				: `已选 ${zipItems.length} 项:${zipItemNames}` }}</div>
			<n-radio-group v-model:value="zipFormat" size="small" class="zip-fmt">
				<n-radio-button v-for="f in zipFormats" :key="f" :value="f">{{ f }}</n-radio-button>
			</n-radio-group>
			<n-input ref="zipInput" v-model:value="zipName" :status="zipInvalid ? 'error' : undefined"
				placeholder="包名(后缀按格式自动补)" @keydown.enter="submitZip" />
			<div v-if="zipInvalid" class="name-err">名称不能包含 / 或 \</div>
			<div v-else class="transfer-to">{{ zipDest }}</div>
			<template #footer>
				<div class="modal-footer">
					<n-button @click="zipShowing = false">取消</n-button>
					<n-button type="primary" :disabled="!zipName.trim() || zipInvalid" @click="submitZip">压缩</n-button>
				</div>
			</template>
		</n-modal>
	</div>
</template>

<style scoped>
.fs-header { display: flex; align-items: flex-start; justify-content: space-between; margin-bottom: 8px; }
.fs-header h2 { margin: 0; font-size: 20px; }
.fs-ws { color: var(--lr-fg-muted); font-size: 12px; margin-top: 2px; }
.fs-toolbar { display: flex; gap: 2px; }
.fs-crumb {
	display: flex; align-items: center; gap: 2px;
	margin: 0 0 8px; padding-bottom: 2px;
	font-size: 12px; font-family: ui-monospace, monospace;
	overflow-x: auto; white-space: nowrap;
	scrollbar-width: none;
}
.fs-crumb::-webkit-scrollbar { display: none; }
/* 当前段是 <span>、可点的段是 <button>:button 的 UA 样式会把文字在自己的盒子里居中,
   span 撑到 min-height 后文字却贴在顶上,两者就一上一下。统一按 flex 居中。 */
.crumb-seg {
	flex: none; display: flex; align-items: center;
	min-height: 32px; padding: 0 6px;
	border: 0; background: none; border-radius: 6px;
	font: inherit; color: var(--lr-accent); cursor: pointer;
}
.crumb-seg.current { color: var(--lr-fg-muted); cursor: default; }
.crumb-seg:active:not(.current) { background: rgba(127, 127, 127, 0.18); }
.crumb-sep { flex: none; color: var(--lr-fg-muted); }
.fs-list { display: flex; flex-direction: column; gap: 6px; margin-top: 10px; }
.fs-item {
	display: flex; align-items: center; gap: 10px;
	padding: 10px 12px;
	background: var(--lr-bg-elevated);
	border: 1px solid rgba(127, 127, 127, 0.14);
	border-radius: var(--lr-radius);
	cursor: pointer;
}
/* 勾中的行:边框 + 淡底色。移动端看不见 hover,选中态得足够显眼。 */
.fs-item.picked { border-color: var(--lr-accent); background: rgba(37, 99, 235, 0.1); }
.fs-ico { flex: none; font-size: 18px; }
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
/* 批量操作条:整页是文档滚动(App 里没有 overflow 容器),所以 top: 0 就贴在视口顶上。
   窄屏放不下三个按钮时靠 flex-wrap 折成两行,而不是把按钮挤成图标。 */
.fs-batch {
	position: sticky; top: 0; z-index: 5;
	display: flex; align-items: center; flex-wrap: wrap; gap: 6px;
	margin-top: 8px; padding: 6px 8px;
	background: var(--lr-bg-elevated);
	border: 1px solid rgba(127, 127, 127, 0.2);
	border-radius: var(--lr-radius);
}
.fs-batch-n { color: var(--lr-fg-muted); font-size: 12px; }
.fs-batch-ops { display: flex; align-items: center; gap: 6px; }
/* 同 .fs-btn:压掉全局 .n-button 的 44px 下限,否则吸顶条会顶掉一大截列表 */
.fs-batch .n-button { min-height: 34px; height: 34px; }
.fs-check { flex: none; pointer-events: none; }
.modal-footer { display: flex; justify-content: flex-end; gap: 8px; }
.name-modal { width: min(420px, calc(100vw - 32px)); }
.dir-modal { width: min(460px, calc(100vw - 32px)); }
.transfer-src {
	margin-bottom: 8px; font-weight: 600;
	overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.transfer-to {
	margin-top: 8px; font-size: 12px;
	color: var(--lr-fg-muted); font-family: ui-monospace, monospace;
	word-break: break-all;
}
.transfer-to.bad { color: var(--lr-danger); font-family: inherit; }
.name-kind { margin-bottom: 10px; }
.zip-fmt { margin-bottom: 10px; }
.name-err { margin-top: 6px; color: var(--lr-danger); font-size: 12px; }
.file-input { font-size: 13px; }
.upload-list { margin-top: 8px; color: var(--lr-fg-muted); font-size: 12px; }
</style>
