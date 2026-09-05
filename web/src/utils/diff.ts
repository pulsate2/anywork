// 把 unified diff 文本解析成"每文件一块"的结构,供 Git 一级列表与二级差异视图共用。

export interface DiffLine {
  text: string
  kind: 'add' | 'del' | 'hunk' | 'meta' | 'ctx'
  // 行号。加行/上下文取新文件的号,删行取旧文件的号 —— 统一 diff 里这两个号不会同时
  // 显示在一列上,窄屏一列就够。hunk/meta 没有行号。
  no?: number
}

export interface DiffBlock {
  path: string
  adds: number
  dels: number
  lines: DiffLine[]
  open: boolean
}

// git 的文件头行。只在 hunk 之前匹配,所以不会误伤以 --- / +++ 开头的内容行。
const diffHeaderRe = /^(index |--- |\+\+\+ |old mode |new mode |new file mode |deleted file mode |similarity index |dissimilarity index |rename |copy )/

// hunk 头:@@ -旧起始[,行数] +新起始[,行数] @@ 后面可能跟函数名。
const hunkRe = /^@@+ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/

// 把 unified diff 拆成每文件一块,每行标出类型用于着色。
export function parseDiff(text: string): DiffBlock[] {
  const files: DiffBlock[] = []
  let cur: DiffBlock | null = null
  let inHunk = false
  // 当前 hunk 里下一行的旧/新文件行号,逐行推进。
  let oldNo = 0
  let newNo = 0
  for (const raw of text.split('\n')) {
    const line = raw.replace(/\r$/, '')
    if (line.startsWith('diff --git ')) {
      cur = { path: headerPath(line), adds: 0, dels: 0, lines: [], open: true }
      inHunk = false
      files.push(cur)
      continue
    }
    if (!cur) continue
    if (line.startsWith('@@')) {
      inHunk = true
      // 解析不出来(理论上不该发生)就把计数归零,后面的行不显示行号,总比显示错的好。
      const m = hunkRe.exec(line)
      oldNo = m ? Number(m[1]) : 0
      newNo = m ? Number(m[2]) : 0
      cur.lines.push({ text: line, kind: 'hunk' })
    } else if (!inHunk) {
      // 文件头丢掉:路径和增删数已经在块标题里了。但不能把 hunk 之前的行一律丢掉——
      // 二进制文件("Binary files ... differ")和子模块("Subproject commit ...")
      // 整块只有这类说明行,丢了就成空 diff。
      if (!diffHeaderRe.test(line) && line !== '') cur.lines.push({ text: line, kind: 'meta' })
    } else if (line.startsWith('+')) {
      cur.adds++
      cur.lines.push({ text: line, kind: 'add', no: newNo || undefined })
      if (newNo) newNo++
    } else if (line.startsWith('-')) {
      cur.dels++
      cur.lines.push({ text: line, kind: 'del', no: oldNo || undefined })
      if (oldNo) oldNo++
    } else if (line.startsWith('\\')) {
      // "\ No newline at end of file" —— 不是内容行,不占行号。
      cur.lines.push({ text: line, kind: 'meta' })
    } else {
      cur.lines.push({ text: line, kind: 'ctx', no: newNo || undefined })
      if (oldNo) oldNo++
      if (newNo) newNo++
    }
  }
  // 文件多时默认折叠,免得一次渲染上万行卡住手机。
  if (files.length > 3) files.forEach((f) => (f.open = false))
  return files
}

// `diff --git a/x b/x` → 取 b/ 之后的新路径(重命名时正是要看的那个)。
function headerPath(l: string): string {
  const s = l.slice('diff --git '.length)
  const i = s.indexOf(' b/')
  return (i >= 0 ? s.slice(i + 3) : s).replace(/^"(.*)"$/, '$1')
}