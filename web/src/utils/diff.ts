// 把 unified diff 文本解析成"每文件一块"的结构,供 Git 一级列表与二级差异视图共用。

export interface DiffLine {
  text: string
  kind: 'add' | 'del' | 'hunk' | 'meta' | 'ctx'
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

// 把 unified diff 拆成每文件一块,每行标出类型用于着色。
export function parseDiff(text: string): DiffBlock[] {
  const files: DiffBlock[] = []
  let cur: DiffBlock | null = null
  let inHunk = false
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
      cur.lines.push({ text: line, kind: 'hunk' })
    } else if (!inHunk) {
      // 文件头丢掉:路径和增删数已经在块标题里了。但不能把 hunk 之前的行一律丢掉——
      // 二进制文件("Binary files ... differ")和子模块("Subproject commit ...")
      // 整块只有这类说明行,丢了就成空 diff。
      if (!diffHeaderRe.test(line) && line !== '') cur.lines.push({ text: line, kind: 'meta' })
    } else if (line.startsWith('+')) {
      cur.adds++
      cur.lines.push({ text: line, kind: 'add' })
    } else if (line.startsWith('-')) {
      cur.dels++
      cur.lines.push({ text: line, kind: 'del' })
    } else {
      cur.lines.push({ text: line, kind: 'ctx' })
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