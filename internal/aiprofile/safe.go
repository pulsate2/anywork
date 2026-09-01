package aiprofile

import (
	"path/filepath"
	"strings"
)

// sanitizePath 移除 tar 条目中的绝对/父级前缀,防目录穿越。
func sanitizePath(p string) string {
	p = filepath.ToSlash(p)
	p = strings.TrimPrefix(p, "/")
	parts := []string{}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			continue
		}
		parts = append(parts, seg)
	}
	return strings.Join(parts, "/")
}

// withinWithin 校验 dest 在 base 边界内。
func withinWithin(dest, base string) bool {
	dest = filepath.Clean(dest)
	base = filepath.Clean(base)
	return dest == base || strings.HasPrefix(dest, base+string(filepath.Separator))
}
