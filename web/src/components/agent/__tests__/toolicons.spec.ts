// 工具分类图标:pi 的小写工具名也要认(read/edit/grep…),不能一水儿扳手。
import { describe, it, expect } from 'vitest'
import {
  toolCategoryIcon, hasToolCategory,
  EyeIcon, SearchIcon, FileDiffIcon, TerminalIcon, GlobeIcon, WrenchIcon,
} from '@/components/agent/toolIcons'

describe('toolCategoryIcon', () => {
  it('pi 小写工具名归入正确分类', () => {
    expect(toolCategoryIcon('read')).toBe(EyeIcon)
    expect(toolCategoryIcon('grep')).toBe(SearchIcon)
    expect(toolCategoryIcon('ls')).toBe(SearchIcon)
    expect(toolCategoryIcon('write')).toBe(FileDiffIcon)
    expect(toolCategoryIcon('edit')).toBe(FileDiffIcon)
    expect(toolCategoryIcon('replace_match')).toBe(FileDiffIcon)
    expect(toolCategoryIcon('move')).toBe(FileDiffIcon)
    expect(toolCategoryIcon('bash')).toBe(TerminalIcon)
  })

  it('claude/codex 原样命名不受影响', () => {
    expect(toolCategoryIcon('Read')).toBe(EyeIcon)
    expect(toolCategoryIcon('Edit')).toBe(FileDiffIcon)
    expect(toolCategoryIcon('Bash')).toBe(TerminalIcon)
    expect(toolCategoryIcon('WebFetch')).toBe(GlobeIcon)
  })

  it('未知 / MCP / 子 agent 仍兜底扳手,且带名字', () => {
    expect(toolCategoryIcon('mcp__foo__bar')).toBe(WrenchIcon)
    expect(toolCategoryIcon('some_unknown_tool')).toBe(WrenchIcon)
    expect(hasToolCategory('read')).toBe(true)
    expect(hasToolCategory('mcp__foo__bar')).toBe(false)
  })
})
