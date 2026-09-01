package fs

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	iofs "io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// errStopWalk 是提前结束 WalkDir 的哨兵(触顶后不再遍历)。
var errStopWalk = errors.New("stop walk")

// buildMatcher 把查询统一编译成正则:字面量模式先 QuoteMeta,
// 这样字面量与正则、区分与不区分大小写共用一条匹配路径。
// (?m) 让 ^ $ 在替换(整文件匹配)时也按行生效,与按行搜索的语义一致。
func buildMatcher(query string, useRegex, caseSensitive bool) (*regexp.Regexp, error) {
	expr := query
	if !useRegex {
		expr = regexp.QuoteMeta(query)
	}
	flags := "(?m)"
	if !caseSensitive {
		flags += "(?i)"
	}
	return regexp.Compile(flags + expr)
}

// walkSearch 遍历目录树做文件名/内容匹配。
func walkSearch(root string, re *regexp.Regexp, content bool, limit int) *SearchOutcome {
	out := &SearchOutcome{Results: []SearchResult{}}
	deadline := time.Now().Add(searchTimeout)
	_ = filepath.WalkDir(root, func(p string, d iofs.DirEntry, err error) error {
		if err != nil {
			// 权限不足/被占用的目录跳过就好,不能中断整次搜索。
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() && p != root && skipDirName(d.Name()) {
			return filepath.SkipDir
		}
		out.Scanned++
		if len(out.Results) >= limit || out.Scanned >= searchMaxScan || time.Now().After(deadline) {
			out.Truncated = true
			return errStopWalk
		}
		if !content {
			if re.MatchString(d.Name()) {
				r := SearchResult{
					Path: filepath.ToSlash(p), Rel: relTo(p, root), Dir: d.IsDir(),
				}
				if info, ierr := d.Info(); ierr == nil && !d.IsDir() {
					r.Size = info.Size()
				}
				out.Results = append(out.Results, r)
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > searchMaxFileSize {
			return nil
		}
		room := min(limit-len(out.Results), searchMaxPerFile)
		for _, hit := range scanFileContent(p, re, room) {
			hit.Path = filepath.ToSlash(p)
			hit.Rel = relTo(p, root)
			hit.Size = info.Size()
			out.Results = append(out.Results, hit)
		}
		return nil
	})
	return out
}

// scanFileContent 逐行匹配一个文件,返回不超过 max 条命中行。
func scanFileContent(p string, re *regexp.Regexp, max int) []SearchResult {
	if max <= 0 {
		return nil
	}
	f, err := os.Open(p)
	if err != nil {
		return nil
	}
	defer f.Close()
	head := make([]byte, 1024)
	n, _ := f.Read(head)
	if containsNUL(head[:n]) {
		return nil
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil
	}
	var hits []SearchResult
	sc := bufio.NewScanner(f)
	// 默认 64KB 上限对压缩过的 js 不够,单行放宽到 1MB。
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Text()
		ranges := re.FindAllStringIndex(line, searchMaxPerLine)
		if len(ranges) == 0 {
			continue
		}
		text, matches := buildLineHit(line, ranges)
		hits = append(hits, SearchResult{Line: lineNo, Text: text, Matches: matches})
		if len(hits) >= max {
			break
		}
	}
	return hits
}

// buildLineHit 截取第一处命中附近的窗口,并把字节偏移换算成窗口内的 UTF-16 下标。
// 超长行(压缩代码)整行回传会把响应撑爆,窗口外的命中位置一并丢弃。
func buildLineHit(line string, ranges [][]int) (string, []SearchMatch) {
	winStart := 0
	if ranges[0][0] > 60 {
		winStart = ranges[0][0] - 60
		for winStart > 0 && !utf8.RuneStart(line[winStart]) {
			winStart--
		}
	}
	winEnd, count := winStart, 0
	for winEnd < len(line) && count < searchMaxLineOut {
		_, size := utf8.DecodeRuneInString(line[winEnd:])
		winEnd += size
		count++
	}
	matches := make([]SearchMatch, 0, len(ranges))
	for _, r := range ranges {
		if r[0] < winStart || r[1] > winEnd || r[1] == r[0] {
			continue
		}
		matches = append(matches, SearchMatch{
			Col: utf16Len(line[winStart:r[0]]),
			Len: utf16Len(line[r[0]:r[1]]),
		})
	}
	return line[winStart:winEnd], matches
}

// utf16Len 返回 s 在 JS 字符串里占的下标数(BMP 外字符占 2)。
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r > 0xFFFF {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// skipDirName 跳过版本库内部目录、依赖目录和 Windows 系统目录(root 可能是盘根)。
func skipDirName(name string) bool {
	switch strings.ToLower(name) {
	case ".git", ".svn", ".hg", "node_modules", "__pycache__", ".venv",
		"$recycle.bin", "system volume information":
		return true
	}
	return false
}

func relTo(p, root string) string {
	if rel, err := filepath.Rel(root, p); err == nil {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(p)
}

// replaceInFile 整文件读入后替换,返回替换处数;0 表示没命中,不落盘。
func (s *Service) replaceInFile(p string, re *regexp.Regexp, repl string, useRegex bool) (int, error) {
	abs, err := s.Resolve(p)
	if err != nil {
		return 0, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() || info.Size() > searchMaxFileSize {
		return 0, nil
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return 0, err
	}
	if containsNUL(data[:min(len(data), 1024)]) {
		return 0, nil
	}
	n := len(re.FindAllIndex(data, -1))
	if n == 0 {
		return 0, nil
	}
	var out []byte
	if useRegex {
		out = re.ReplaceAll(data, []byte(repl))
	} else {
		out = re.ReplaceAllLiteral(data, []byte(repl))
	}
	if err := s.Write(p, bytes.NewReader(out)); err != nil {
		return 0, err
	}
	return n, nil
}
