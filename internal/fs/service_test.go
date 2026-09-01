package fs

import (
	"path/filepath"
	"runtime"
	"testing"
)

// Resolve 的路径语义踩过两次坑(盘根带尾分隔符、盘符路径被当成 root 相对),
// 用表驱动把两种平台的边界固定下来。want 为空表示应当拒绝。
func TestResolve(t *testing.T) {
	type tc struct {
		root, in, want string
	}
	var cases []tc
	if runtime.GOOS == "windows" {
		cases = []tc{
			{`D:\`, `D:/projects/x`, `D:\projects\x`},
			{`D:\`, `/D:/projects/x`, `D:\projects\x`}, // 旧数据里的前导斜杠形式
			{`D:\`, `D:`, `D:\`},                       // 上溯到盘根
			{`D:\`, `/`, `D:\`},
			{`D:\`, `projects/x`, `D:\projects\x`},
			{`D:\`, `/projects/x`, `D:\projects\x`},
			{`D:\`, `C:/Windows`, ``}, // 跨盘越界
			{`D:\projects`, `D:/projects/x`, `D:\projects\x`},
			{`D:\projects`, `/x`, `D:\projects\x`},
			{`D:\projects`, `D:/other`, ``},
			{`D:\projects`, `../etc`, ``},
		}
	} else {
		cases = []tc{
			{`/`, `/etc`, `/etc`},
			{`/`, `etc`, `/etc`},
			{`/`, `/`, `/`},
			{`/home/u`, `/home/u/x`, `/home/u/x`},
			{`/home/u`, `/x`, `/home/u/x`}, // 前端 cwd="/" 视为 root 相对
			{`/home/u`, `../etc/passwd`, ``},
			{`/home/u`, `/../etc/passwd`, ``},
		}
	}
	for _, c := range cases {
		s := NewService(c.root, false)
		got, err := s.Resolve(c.in)
		if c.want == "" {
			if err == nil {
				t.Errorf("Resolve(root=%q, %q) = %q, want error", c.root, c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("Resolve(root=%q, %q) error: %v", c.root, c.in, err)
			continue
		}
		if got != filepath.Clean(c.want) {
			t.Errorf("Resolve(root=%q, %q) = %q, want %q", c.root, c.in, got, c.want)
		}
	}
}
