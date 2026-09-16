package backup

import (
	"strings"
)

// ignoreMatcher 轻量 gitignore 风格匹配:目录名 / *.log / 后缀 / ** 通配 / 前导 / 锚定。
type ignoreMatcher struct {
	patterns []ignorePattern
}

type ignorePattern struct {
	raw      string
	dirOnly  bool // 尾 / :仅目录
	negate   bool // ! 前缀:反选
	anchored bool // 前导 / 或含中间 /:只从根匹配;裸名匹配任意层级
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
		// 前导 / 或含中间 /:锚定到根。注意先判断再剥离前导 /,
		// 否则单段锚定(/dist)剥完就丢了锚定信息。
		ip.anchored = strings.HasPrefix(p, "/") || strings.Contains(p, "/")
		p = strings.TrimPrefix(p, "/")
		ip.glob = p
		m.patterns = append(m.patterns, ip)
	}
	return m
}

// shouldIgnore 判断相对路径(以 / 分隔)是否应排除。isDir 由调用方告知,
// dirOnly 模式只在目录命中时生效。
func (m *ignoreMatcher) shouldIgnore(rel string, isDir bool) bool {
	if m == nil || len(m.patterns) == 0 {
		return false
	}
	rel = strings.Trim(rel, "/")
	if rel == "" || rel == "." {
		return false
	}
	segments := strings.Split(rel, "/")
	ignored := false
	for _, p := range m.patterns {
		if matchPattern(p, segments, isDir) {
			ignored = !p.negate
		}
	}
	return ignored
}

// hasNegate 是否存在反选(!)模式。有反选时调用方不可对目录做 SkipDir
// 剪枝——排除目录里的文件可能被 ! 救回,剪掉就永远评估不到了。
func (m *ignoreMatcher) hasNegate() bool {
	if m == nil {
		return false
	}
	for _, p := range m.patterns {
		if p.negate {
			return true
		}
	}
	return false
}

// matchPattern 判断单条模式是否命中路径分段。
func matchPattern(p ignorePattern, segments []string, isDir bool) bool {
	if p.anchored {
		return matchPrefix(p, segments, isDir)
	}
	// 裸名(单段):匹配任意层级的某一段。非末段必为目录祖先,
	// dirOnly 也命中(如 dist/ 排掉嵌套 apps/web/dist/ 下所有内容)。
	for i := range segments {
		if !matchSegments(strings.Split(p.glob, "/"), segments[i:i+1]) {
			continue
		}
		if i < len(segments)-1 {
			return true
		}
		return !p.dirOnly || isDir
	}
	return false
}

// matchPrefix 判断 segments 是否整体命中模式(模式可能含 ** 跨段、段内 * / ?)。
func matchPrefix(p ignorePattern, segments []string, isDir bool) bool {
	for end := 1; end <= len(segments); end++ {
		if !matchSegments(strings.Split(p.glob, "/"), segments[:end]) {
			continue
		}
		// 命中的是 segments[:end]:若不是全部段,它是个目录前缀。
		if end < len(segments) {
			return true
		}
		// 全段命中:dirOnly 仅目录生效。
		return !p.dirOnly || isDir
	}
	return false
}

// matchSegments 递归匹配模式段与路径段:** 匹配零或多段,其余段 simpleMatch。
func matchSegments(pat, segs []string) bool {
	if len(pat) == 0 {
		return len(segs) == 0
	}
	if pat[0] == "**" {
		// ** 吃掉 0..len(segs) 段。尾随 ** 至少吃一段:
		// foo/** 匹配 foo 下所有内容但不匹配 foo 本身(与 git 一致)。
		trailing := len(pat) == 1
		for skip := 0; skip <= len(segs); skip++ {
			if trailing && skip == 0 {
				continue
			}
			if matchSegments(pat[1:], segs[skip:]) {
				return true
			}
		}
		return false
	}
	if len(segs) == 0 || !simpleMatch(pat[0], segs[0]) {
		return false
	}
	return matchSegments(pat[1:], segs[1:])
}

func simpleMatch(pattern, s string) bool {
	// 段内匹配:模式含分隔符属 malformed(调用方已按 / 拆段),不假装能匹配。
	if strings.Contains(pattern, "/") {
		return false
	}
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
