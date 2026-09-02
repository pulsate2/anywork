// 文件类型图标:参照 VSCode(seti 主题)的观感,用 Font Awesome 的品牌图标 + 品牌色。
// 查表顺序与 highlight.ts 的 langFromPath 一致:先整个文件名精确匹配(Dockerfile、
// package.json 这类无扩展名或全名有意义的),再退回小写扩展名。
// @vicons/fa 标了 sideEffects: false,按名导入会被 Vite 摇树,只打包用到的图标。
import type { Component } from 'vue'
import {
  Docker, Js, React, Python, Java, Php, Rust, Html5, Css3, Sass, Less, Vuejs,
  Markdown, Git, Gitlab, Npm, Yarn, Database, Terminal, Cog, Key, Lock, Font,
  Swift, Gem, Hashtag, Code, Copyright, Table, Cube, Feather,
  FileArchive, FileImage, FilePdf, FileVideo, FileAudio, FileExcel, FileWord,
  FileCode, FileAlt,
} from '@vicons/fa'
import { FolderOpenOutline } from '@vicons/ionicons5'

export interface FileIcon {
  icon: Component
  color: string
}

const DIR: FileIcon = { icon: FolderOpenOutline, color: 'var(--lr-accent)' }
const FALLBACK: FileIcon = { icon: FileAlt, color: 'var(--lr-fg-muted)' }

const GO: FileIcon = { icon: FileCode, color: '#00ADD8' } // FA5 没有 Go gopher,用代码图标配 Go 官方青色
const TS: FileIcon = { icon: FileCode, color: '#3178C6' }
const JS: FileIcon = { icon: Js, color: '#F1E05A' }
const JSON_: FileIcon = { icon: Code, color: '#CBCB41' }
const SHELL: FileIcon = { icon: Terminal, color: '#4EAA25' }
const CONF: FileIcon = { icon: Cog, color: '#6D8086' }
const ARCHIVE: FileIcon = { icon: FileArchive, color: '#EFB236' }
const IMAGE: FileIcon = { icon: FileImage, color: '#B180D7' }
const GITICON: FileIcon = { icon: Git, color: '#F05032' }
const NPMICON: FileIcon = { icon: Npm, color: '#CB3837' }
const LICENSE: FileIcon = { icon: Copyright, color: '#D4AF37' }
const CFAMILY: FileIcon = { icon: FileCode, color: '#519ABA' }

// 整个文件名精确匹配(比较时统一转小写,所以键也全小写)。
const nameMap: Record<string, FileIcon> = {
  'dockerfile': { icon: Docker, color: '#2496ED' },
  'dockerfile.dev': { icon: Docker, color: '#2496ED' },
  '.dockerignore': { icon: Docker, color: '#2496ED' },
  'docker-compose.yml': { icon: Docker, color: '#2496ED' },
  'docker-compose.yaml': { icon: Docker, color: '#2496ED' },
  '.gitignore': GITICON,
  '.gitattributes': GITICON,
  '.gitmodules': GITICON,
  '.gitkeep': GITICON,
  '.gitlab-ci.yml': { icon: Gitlab, color: '#FC6D26' },
  'package.json': NPMICON,
  'package-lock.json': NPMICON,
  '.npmrc': NPMICON,
  '.npmignore': NPMICON,
  'yarn.lock': { icon: Yarn, color: '#2C8EBB' },
  'pnpm-lock.yaml': NPMICON,
  'go.mod': GO,
  'go.sum': GO,
  'go.work': GO,
  'cargo.toml': { icon: Rust, color: '#DEA584' },
  'cargo.lock': { icon: Lock, color: '#DEA584' },
  'gemfile': { icon: Gem, color: '#CC342D' },
  'gemfile.lock': { icon: Gem, color: '#CC342D' },
  'makefile': { icon: Cog, color: '#6D8086' },
  'license': LICENSE,
  'license.md': LICENSE,
  'license.txt': LICENSE,
  'copying': LICENSE,
  'readme': { icon: Markdown, color: '#519ABA' },
  'readme.md': { icon: Markdown, color: '#519ABA' },
  '.env': { icon: Key, color: '#DFB13B' },
  '.editorconfig': CONF,
  'requirements.txt': { icon: Python, color: '#3776AB' },
  'pyproject.toml': { icon: Python, color: '#3776AB' },
}

// 小写扩展名匹配。
const extMap: Record<string, FileIcon> = {
  // 前端
  'js': JS, 'mjs': JS, 'cjs': JS,
  'jsx': { icon: React, color: '#61DAFB' }, 'tsx': { icon: React, color: '#61DAFB' },
  'ts': TS, 'mts': TS, 'cts': TS,
  'vue': { icon: Vuejs, color: '#42B883' },
  'json': JSON_, 'jsonc': JSON_, 'json5': JSON_,
  'html': { icon: Html5, color: '#E34F26' }, 'htm': { icon: Html5, color: '#E34F26' },
  'css': { icon: Css3, color: '#1572B6' },
  'scss': { icon: Sass, color: '#CC6699' }, 'sass': { icon: Sass, color: '#CC6699' },
  'less': { icon: Less, color: '#2B5E91' },
  // 后端 / 系统
  'go': GO,
  'py': { icon: Python, color: '#3776AB' }, 'pyw': { icon: Python, color: '#3776AB' },
  'java': { icon: Java, color: '#E76F00' }, 'jar': { icon: Java, color: '#E76F00' },
  'php': { icon: Php, color: '#777BB4' },
  'rs': { icon: Rust, color: '#DEA584' },
  'rb': { icon: Gem, color: '#CC342D' },
  'swift': { icon: Swift, color: '#F05138' },
  'cs': { icon: Hashtag, color: '#68217A' },
  'c': CFAMILY, 'h': CFAMILY, 'cpp': CFAMILY, 'cc': CFAMILY, 'cxx': CFAMILY,
  'hpp': CFAMILY, 'hh': CFAMILY,
  'kt': { icon: FileCode, color: '#A97BFF' },
  'lua': { icon: FileCode, color: '#2C2D72' },
  'sh': SHELL, 'bash': SHELL, 'zsh': SHELL, 'fish': SHELL,
  'ps1': { icon: Terminal, color: '#5391FE' }, 'bat': SHELL, 'cmd': SHELL,
  // 配置 / 数据
  'yml': CONF, 'yaml': CONF, 'toml': CONF, 'ini': CONF, 'conf': CONF,
  'cfg': CONF, 'properties': CONF, 'env': { icon: Key, color: '#DFB13B' },
  'sql': { icon: Database, color: '#DAD8D8' },
  'db': { icon: Database, color: '#DAD8D8' }, 'sqlite': { icon: Database, color: '#DAD8D8' },
  'sqlite3': { icon: Database, color: '#DAD8D8' },
  'csv': { icon: Table, color: '#207245' }, 'tsv': { icon: Table, color: '#207245' },
  'xml': { icon: Code, color: '#E37933' }, 'svg': { icon: FileImage, color: '#FFB13B' },
  // 文档
  'md': { icon: Markdown, color: '#519ABA' }, 'markdown': { icon: Markdown, color: '#519ABA' },
  'pdf': { icon: FilePdf, color: '#E5252A' },
  'doc': { icon: FileWord, color: '#2B579A' }, 'docx': { icon: FileWord, color: '#2B579A' },
  'xls': { icon: FileExcel, color: '#207245' }, 'xlsx': { icon: FileExcel, color: '#207245' },
  'txt': { icon: FileAlt, color: 'var(--lr-fg-muted)' },
  'log': { icon: Feather, color: '#8C9196' },
  'diff': GITICON, 'patch': GITICON,
  // 媒体
  'png': IMAGE, 'jpg': IMAGE, 'jpeg': IMAGE, 'gif': IMAGE, 'webp': IMAGE,
  'bmp': IMAGE, 'ico': IMAGE, 'avif': IMAGE,
  'mp4': { icon: FileVideo, color: '#FD971F' }, 'mkv': { icon: FileVideo, color: '#FD971F' },
  'mov': { icon: FileVideo, color: '#FD971F' }, 'avi': { icon: FileVideo, color: '#FD971F' },
  'webm': { icon: FileVideo, color: '#FD971F' },
  'mp3': { icon: FileAudio, color: '#F16529' }, 'wav': { icon: FileAudio, color: '#F16529' },
  'flac': { icon: FileAudio, color: '#F16529' }, 'ogg': { icon: FileAudio, color: '#F16529' },
  'm4a': { icon: FileAudio, color: '#F16529' },
  'ttf': { icon: Font, color: '#F06529' }, 'otf': { icon: Font, color: '#F06529' },
  'woff': { icon: Font, color: '#F06529' }, 'woff2': { icon: Font, color: '#F06529' },
  // 压缩包 / 二进制 / 证书
  'zip': ARCHIVE, 'tar': ARCHIVE, 'gz': ARCHIVE, 'tgz': ARCHIVE, 'bz2': ARCHIVE,
  'xz': ARCHIVE, 'rar': ARCHIVE, '7z': ARCHIVE,
  'exe': { icon: Cube, color: '#8C9196' }, 'dll': { icon: Cube, color: '#8C9196' },
  'so': { icon: Cube, color: '#8C9196' }, 'wasm': { icon: Cube, color: '#654FF0' },
  'pem': { icon: Key, color: '#D4AF37' }, 'key': { icon: Key, color: '#D4AF37' },
  'crt': { icon: Key, color: '#D4AF37' }, 'cer': { icon: Key, color: '#D4AF37' },
  'lock': { icon: Lock, color: '#8C9196' },
}

// 图片扩展名单独导出:F2a 图片预览要靠它决定走 <img> 还是走文本编辑器。
const IMAGE_EXTS = new Set(['png', 'jpg', 'jpeg', 'gif', 'webp', 'bmp', 'ico', 'avif', 'svg'])
// 压缩包同理:F2b 走条目列表而不是文本。tar.gz 只取最后一段扩展名 gz,已在集合里。
const ARCHIVE_EXTS = new Set(['zip', 'tar', 'gz', 'tgz', 'bz2', 'xz', 'rar', '7z'])

function basename(path: string): string {
  return path.split(/[/\\]/).filter(Boolean).pop() || path
}

// 取小写扩展名;无扩展名(或以点开头的纯隐藏文件如 .env)返回 ''。
export function extOf(path: string): string {
  const base = basename(path).toLowerCase()
  const dot = base.lastIndexOf('.')
  if (dot <= 0) return ''
  return base.slice(dot + 1)
}

export function isImagePath(path: string): boolean {
  return IMAGE_EXTS.has(extOf(path))
}

export function isArchivePath(path: string): boolean {
  return ARCHIVE_EXTS.has(extOf(path))
}

export function isMarkdownPath(path: string): boolean {
  const ext = extOf(path)
  return ext === 'md' || ext === 'markdown'
}

export function fileIcon(path: string, isDir = false): FileIcon {
  if (isDir) return DIR
  const base = basename(path).toLowerCase()
  return nameMap[base] || extMap[extOf(path)] || FALLBACK
}
