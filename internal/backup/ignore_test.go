package backup

import "testing"

// 表驱动用例来自 E2E 实测的 6 种写法 fixture(2026-09-16):
// 顶层/嵌套 node_modules、.next、dist 目录、名为 dist 的文件、反选等。
func TestShouldIgnore(t *testing.T) {
	type c struct {
		name    string
		pattern []string
		rel     string
		isDir   bool
		want    bool
	}
	cases := []c{
		// 裸目录名:任意层级命中(文件或目录)。
		{"裸名顶层目录", []string{"node_modules"}, "node_modules/a/i.js", false, true},
		{"裸名嵌套目录", []string{"node_modules"}, "web/node_modules/b/i.js", false, true},
		{"裸名目录本身", []string{"node_modules"}, "node_modules", true, true},
		{"裸名无关文件", []string{"node_modules"}, "README.md", false, false},
		{"裸名无关目录", []string{"node_modules"}, "web/.next/c.js", false, false},
		{"多个裸名", []string{"node_modules", ".next", "dist", "*.log"}, "apps/api/.next/server.js", false, true},
		{"多个裸名-保留", []string{"node_modules", ".next", "dist", "*.log"}, "src/main.go", false, false},
		{"多个裸名-后缀", []string{"node_modules", ".next", "dist", "*.log"}, "app.log", false, true},

		// 尾 /:仅目录。名为 dist 的文件不再命中(与 gitignore 一致)。
		{"尾斜杠目录", []string{"dist/"}, "apps/web/dist/a/app.css", false, true},
		{"尾斜杠不排同名文件", []string{"dist/"}, "dist", false, false},
		{"裸名排同名文件", []string{"dist"}, "dist", false, true},

		// ** 通配(修复前:任何含 ** 的模式吞掉所有文件)。
		{"双星命中顶层", []string{"**/node_modules"}, "node_modules/a/i.js", false, true},
		{"双星命中嵌套", []string{"**/node_modules"}, "web/node_modules/b/i.js", false, true},
		{"双星不误伤无关", []string{"**/node_modules"}, "README.md", false, false},
		{"双星不误伤其它目录", []string{"**/node_modules"}, "web/.next/c.js", false, false},
		{"目录下双星", []string{"node_modules/**"}, "node_modules/a/i.js", false, true},
		{"目录下双星排其下目录", []string{"node_modules/**"}, "node_modules/a", true, true},
		{"目录下双星不误伤", []string{"node_modules/**"}, "README.md", false, false},

		// 前导 / 锚定:只匹配根(修复前:完全不生效)。
		{"锚定根目录", []string{"/dist"}, "dist", true, true},
		{"锚定根目录下文件", []string{"/dist"}, "dist/a.js", false, true},
		{"锚定不排嵌套", []string{"/dist"}, "apps/web/dist/a/app.css", false, false},
		{"锚定不排嵌套目录", []string{"/dist"}, "packages/lib/dist", true, false},

		// 子路径:含 / 天然锚定。
		{"子路径命中", []string{"web/.next"}, "web/.next/static/app.js", false, true},
		{"子路径不误伤兄弟", []string{"web/.next"}, "apps/api/.next/server.js", false, false},

		// 反选:node_modules 整体排除后救回 important.js。
		{"反选救回", []string{"node_modules", "!important.js"}, "node_modules/important.js", false, false},
		{"反选不影响其他", []string{"node_modules", "!important.js"}, "node_modules/a/i.js", false, true},

		// 边界:空模式、空路径。
		{"空模式", nil, "anything/x.txt", false, false},
		{"空路径", []string{"a"}, "", false, false},
		{"根路径", []string{"a"}, ".", true, false},
	}
	for _, tc := range cases {
		m := newIgnoreMatcher(tc.pattern)
		if got := m.shouldIgnore(tc.rel, tc.isDir); got != tc.want {
			t.Errorf("%s: shouldIgnore(%q, isDir=%v) patterns=%v = %v, want %v",
				tc.name, tc.rel, tc.isDir, tc.pattern, got, tc.want)
		}
	}
}

// SkipDir 剪枝依赖:目录命中时 walk 不再深入,但目录下文件的相对路径
// 也应命中(理论上不会走到,防御性验证)。
func TestShouldIgnoreDirPrefix(t *testing.T) {
	m := newIgnoreMatcher([]string{"node_modules"})
	if !m.shouldIgnore("node_modules", true) {
		t.Fatal("目录本身应命中")
	}
	if !m.shouldIgnore("node_modules/a", true) {
		t.Fatal("子目录应命中(祖先段)")
	}
}

// simpleMatch 段内通配回归。
func TestSimpleMatch(t *testing.T) {
	cases := []struct {
		pat, s string
		want   bool
	}{
		{"*.log", "app.log", true},
		{"*.log", "app.log.bak", false},
		{"a?c", "abc", true},
		{"a?c", "abbc", false},
		{"build", "build", true},
		{"*.min.js", "x.min.js", true},
	}
	for _, tc := range cases {
		if got := simpleMatch(tc.pat, tc.s); got != tc.want {
			t.Errorf("simpleMatch(%q, %q) = %v, want %v", tc.pat, tc.s, got, tc.want)
		}
	}
}
