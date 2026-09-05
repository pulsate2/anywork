//go:build linux

package sysmon

import (
	"os"
	"strings"
	"testing"
)

// TestProcName comm 与 argv[0] 不一致时该信谁。
func TestProcName(t *testing.T) {
	cases := []struct {
		name, comm, argv0, want string
	}{
		// 起因:Node 把主线程改名叫 MainThread,一排 node 程序全显示成同一个名字。
		{"Node 的线程名不能当进程名", "MainThread", "node", "node"},
		{"argv0 带路径只取基名", "MainThread", "/usr/bin/node", "node"},
		// comm 是 argv[0] 基名的一部分:comm 更干净,保留。
		{"登录 shell 的前导横线", "bash", "-bash", "bash"},
		{"systemd 风格的 @ 前缀", "dbus-daemon", "@dbus-daemon", "dbus-daemon"},
		{"comm 被 15 字上限截断", "systemd-journal", "systemd-journald", "systemd-journal"},
		{"服务把 argv0 改成状态串", "sshd", "sshd: root@pts/0", "sshd"},
		{"一致时原样返回", "go", "/usr/local/go/bin/go", "go"},
		// 缺一边就用另一边,不要返回空。
		{"没有 argv0", "bash", "", "bash"},
		{"没有 comm", "", "/usr/bin/node", "node"},
	}
	for _, c := range cases {
		if got := procName(c.comm, c.argv0); got != c.want {
			t.Errorf("%s: procName(%q, %q) = %q, 期望 %q", c.name, c.comm, c.argv0, got, c.want)
		}
	}
}

// TestEnrichSelfName 本进程(go test 编出来的二进制)的名字要跟 argv[0] 基名对得上,
// 顺带验证 enrich 真的读到了 /proc 而不是在原地打转。
func TestEnrichSelfName(t *testing.T) {
	want := os.Args[0]
	if i := strings.LastIndexByte(want, '/'); i >= 0 {
		want = want[i+1:]
	}
	p := Process{PID: os.Getpid(), Name: "MainThread"}
	enrich(&p)
	if p.Name != want {
		t.Errorf("Name = %q, 期望 %q", p.Name, want)
	}
	if p.Cmd == "" {
		t.Error("Cmd 没补全")
	}
}
