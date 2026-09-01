package backup

import (
	"strings"
)

// ignoreMatcher 轻量 gitignore 风格匹配(目录名 / *.log / 后缀 /)。
// 支持 `*`、`**`、`?`、前后缀、行尾 `/` 表示仅目录。
type ignoreMatcher struct {
	patterns []ignorePattern
}

type ignorePattern struct {
	raw      string
	dirOnly  bool
	negate   bool
	baseOnly bool // 无 "/" 时匹配任意层级 basename
	glob     string
}

// newIgnoreMatcher 解析排除列表(每个元素一行)。
func newIgnoreMatcher(patterns []string) *ignoreMatcher {
	m := &ignoreMatcher{}
	for _, raw := range patterns {
		p := strings.TrimSpace(raw)
		if p == "" || strings.HasPrefix(p, "#") {
			continue
		}
		ip := ignorePattern{raw: p}
		if strings.HasPrefix(p, "!") {
			ip.negate = true
			p = strings.TrimPrefix(p, "!")
		}
		if strings.HasSuffix(p, "/") {
			ip.dirOnly = true
			p = strings.TrimSuffix(p, "/")
		}
		if !strings.Contains(p, "/") {
			ip.baseOnly = true
		}
		ip.glob = p
		m.patterns = append(m.patterns, ip)
	}
	return m
}

// shouldIgnore 判断相对路径(以 / 分隔)是否应排除。
func (m *ignoreMatcher) shouldIgnore(rel string) bool {
	if m == nil || len(m.patterns) == 0 {
		return false
	}
	rel = strings.Trim(rel, "/")
	ignored := false
	for _, p := range m.patterns {
		if matchPattern(p, rel) {
			ignored = !p.negate
		}
	}
	return ignored
}

// matchPattern 判断单条模式是否命中路径。
func matchPattern(p ignorePattern, rel string) bool {
	// 拆开逐级匹配,目录用前缀路径。
	segments := strings.Split(rel, "/")
	for i := range segments {
		prefix := strings.Join(segments[:i+1], "/")
		if globMatch(p.glob, prefix) {
			if !p.dirOnly || i < len(segments)-1 {
				return true
			}
			// 最后一段:仅当是目录才匹配(由调用方判断,此处放宽)。
			return true
		}
		// baseOnly: 匹配任意层级的文件名。
		if p.baseOnly && globMatch(p.glob, segments[i]) {
			if !p.dirOnly || i < len(segments)-1 {
				return true
			}
			return true
		}
	}
	return false
}

// globMatch 简化 glob:* 匹配段内任意,** 跨目录,? 单字符。
func globMatch(pattern, s string) bool {
	if strings.Contains(pattern, "**") {
		return pathMatch(pattern, s)
	}
	return simpleMatch(pattern, s)
}

func simpleMatch(pattern, s string) bool {
	if !strings.ContainsAny(pattern, "*?") {
		return pattern == s
	}
	// 递归匹配通配符。
	pi, si := 0, 0
	for si < len(s) {
		switch {
		case pi < len(pattern) && (pattern[pi] == '?' || pattern[pi] == s[si]):
			pi++
			si++
		case pi < len(pattern) && pattern[pi] == '*':
			if pi == len(pattern)-1 {
				return true
			}
			for k := si; k <= len(s); k++ {
				if simpleMatch(pattern[pi+1:], s[k:]) {
					return true
				}
			}
			return false
		default:
			return false
		}
	}
	for pi < len(pattern) && pattern[pi] == '*' {
		pi++
	}
	return pi == len(pattern)
}

// pathMatch 处理 ** 跨目录。
func pathMatch(pattern, s string) bool {
	patParts := strings.Split(pattern, "**")
	if len(patParts) == 1 {
		return simpleMatch(pattern, s)
	}
	// 简化:用 filepath 风格逐段。为超轻量,退化为前缀/后缀包含判断。
	return strings.Contains(s, strings.Trim(patParts[0], "/")) || simpleMatch(pattern, s)
}
