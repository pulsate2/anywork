package fs

import (
	"errors"
	"fmt"
	"time"
)

// ErrBadQuery 查询为空或正则非法(→ 400)。
var ErrBadQuery = errors.New("bad search query")

// 搜索的几道闸:根目录可能是整个磁盘,必须同时限制单文件大小、扫描条目数和总时长。
const (
	searchMaxFileSize = 2 << 20 // 超过 2MB 的文件不做内容匹配
	searchMaxScan     = 60000   // 最多访问的目录项数
	searchTimeout     = 5 * time.Second
	searchMaxPerLine  = 20  // 单行最多返回的命中位置
	searchMaxPerFile  = 50  // 单文件最多返回的命中行
	searchMaxLineOut  = 400 // 返回行文本的最大字符数(压缩过的代码单行可达几十 KB)
)

// SearchMatch 行内一处命中的位置。单位是 UTF-16 码元,即 JS 字符串下标,
// 前端可直接 text.slice(col, col + len) 取出高亮片段。
type SearchMatch struct {
	Col int `json:"col"`
	Len int `json:"len"`
}

// SearchResult 单条命中。文件名模式下只有 Path/Rel/Dir/Size。
type SearchResult struct {
	// Path 是 root 内的绝对路径(正斜杠),与 List 一致,可原样回传给 read/write。
	Path    string        `json:"path"`
	Rel     string        `json:"rel"`
	Dir     bool          `json:"dir"`
	Size    int64         `json:"size"`
	Line    int           `json:"line,omitempty"`
	Text    string        `json:"text,omitempty"`
	Matches []SearchMatch `json:"matches,omitempty"`
}

// SearchOptions 搜索参数。
type SearchOptions struct {
	Dir     string
	Query   string
	Content bool // true=匹配文件内容,false=匹配文件名
	Regex   bool // false 时 Query 按字面量处理
	Case    bool // 区分大小写
	Limit   int
}

// SearchOutcome 搜索结果集。Truncated 表示命中数/扫描量/时限任一触顶。
type SearchOutcome struct {
	Results   []SearchResult `json:"results"`
	Truncated bool           `json:"truncated"`
	Scanned   int            `json:"scanned"`
}

// Search 在 dir 下递归搜索。原生实现:早先版本 shell out 到 rg/grep,
// 两者在 Windows 上都不存在,于是每次查询都返回空数组。
func (s *Service) Search(opt SearchOptions) (*SearchOutcome, error) {
	if opt.Query == "" {
		return nil, fmt.Errorf("%w: 关键词为空", ErrBadQuery)
	}
	abs, err := s.Resolve(opt.Dir)
	if err != nil {
		return nil, err
	}
	re, err := buildMatcher(opt.Query, opt.Regex, opt.Case)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadQuery, err)
	}
	limit := opt.Limit
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	return walkSearch(abs, re, opt.Content, limit), nil
}

// ReplaceOptions 批量替换参数。Files 是 root 内路径列表,通常直接取搜索结果的 Path。
type ReplaceOptions struct {
	Files   []string
	Query   string
	Replace string
	Regex   bool
	Case    bool
}

// ReplaceOutcome 改动的文件数与替换处数。
type ReplaceOutcome struct {
	Files int `json:"files"`
	Count int `json:"count"`
}

// Replace 在指定文件里把 Query 全部替换成 Replace。Regex 模式下 Replace 支持 $1 引用。
func (s *Service) Replace(opt ReplaceOptions) (*ReplaceOutcome, error) {
	if err := s.allowWrite(); err != nil {
		return nil, err
	}
	if opt.Query == "" {
		return nil, fmt.Errorf("%w: 关键词为空", ErrBadQuery)
	}
	if len(opt.Files) == 0 {
		return nil, fmt.Errorf("%w: 未指定文件", ErrBadQuery)
	}
	re, err := buildMatcher(opt.Query, opt.Regex, opt.Case)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadQuery, err)
	}
	out := &ReplaceOutcome{}
	for _, p := range opt.Files {
		n, err := s.replaceInFile(p, re, opt.Replace, opt.Regex)
		if err != nil {
			return nil, err
		}
		if n > 0 {
			out.Files++
			out.Count += n
		}
	}
	return out, nil
}
