package git

import (
	"strconv"
	"strings"
)

// grabInt 从形如 "..., ahead 3 behind 2]" 的文本中提取 count 数值。
func grabInt(s, key string) int {
	idx := strings.Index(s, key)
	if idx < 0 {
		return 0
	}
	rest := s[idx+len(key):]
	rest = strings.TrimLeft(rest, " ,")
	var b strings.Builder
	for _, c := range rest {
		if c >= '0' && c <= '9' {
			b.WriteRune(c)
		} else {
			break
		}
	}
	n, _ := strconv.Atoi(b.String())
	return n
}

func strconvUnquote(s string) (string, error) {
	return strconv.Unquote(s)
}
