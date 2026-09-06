package git

import (
	"os"
	"path/filepath"
	"testing"
)

// fillNumstat 的口径:暂存侧的数字来自 --cached(HEAD→索引),工作区侧来自裸 diff
// (索引→工作区)。改名记录的新路径在 status 和 numstat 两侧一致,数字能对上;
// 未跟踪文件 git diff 看不见,保持 0;二进制是 `-`,归成 -1 让前端不显示。
func TestStatusNumstat(t *testing.T) {
	s, root := newEmptyRepo(t)
	write := func(p, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(root, filepath.Dir(p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(p)), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("keep.txt", "a\nb\nc\n")
	write("del.txt", "x\ny\n")
	write("src/ren.txt", "1\n2\n3\n")
	if _, err := s.SetIdentity("/", "张三", "zhang@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := s.StageAdd("/", []string{"keep.txt", "del.txt", "src/ren.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Commit("/", "init", false); err != nil {
		t.Fatal(err)
	}

	write("keep.txt", "a\nb\nc\nd\n")
	os.Remove(filepath.Join(root, "del.txt"))
	// git mv:暂存侧出现一条重命名(内容也改了,mv 前后各 5 行、加 1 行)。
	if _, err := s.run(root, nil, "mv", "src/ren.txt", "src/renamed.txt"); err != nil {
		t.Fatal(err)
	}
	write("src/renamed.txt", "1\n2\n3\n4\n5\n6\n")
	// 未跟踪的文本 + 二进制:git diff 都看不见前者;后者先 add -N 意向跟踪,
	// numstat 里以 `-` 计数 —— 正好钉住二进制的 -1 分支。
	write("untracked.txt", "new\n")
	if err := os.WriteFile(filepath.Join(root, "bin.dat"), []byte{0x00, 0x01}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.StageAdd("/", []string{"bin.dat"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.run(root, nil, "reset", "--quiet", "bin.dat"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.run(root, nil, "add", "--intent-to-add", "--", "bin.dat"); err != nil {
		t.Fatal(err)
	}

	st, err := s.Status("/")
	if err != nil {
		t.Fatal(err)
	}
	num := func(es []StatusEntry, path string) (int, int) {
		t.Helper()
		for _, e := range es {
			if e.Path == path {
				return e.Add, e.Del
			}
		}
		t.Fatalf("找不到 %s: %+v", path, es)
		return 0, 0
	}

	// 暂存侧:mv 后索引里还是原内容,重命名 0 增 0 删。
	if a, d := num(st.Staged, "src/renamed.txt"); a != 0 || d != 0 {
		t.Errorf("暂存侧 renamed.txt = +%d -%d,要 +0 -0", a, d)
	}
	// 工作区侧:keep.txt 追加 +1;bin.dat 二进制 -1。
	// (mv 后又改内容的文件 porcelain 只给一条 RM 行、归到暂存组,工作区侧没有行 —— 数字
	// 跟着各自的"查看"弹窗走,这条限制留给分组本身。)
	if a, d := num(st.Unstaged, "keep.txt"); a != 1 || d != 0 {
		t.Errorf("工作区侧 keep.txt = +%d -%d,要 +1 -0", a, d)
	}
	// bin.dat 意向跟踪后出现在工作区侧,二进制计数是 `-` → -1。
	if a, d := num(st.Unstaged, "bin.dat"); a != -1 || d != -1 {
		t.Errorf("工作区侧 bin.dat = +%d -%d,要 -1 -1(二进制)", a, d)
	}
	// 未跟踪:git diff 看不见它们,numstat 不出数,保持 0(前端不显示)。
	if a, d := num(st.Untracked, "untracked.txt"); a != 0 || d != 0 {
		t.Errorf("未跟踪 untracked.txt = +%d -%d,要 0 0", a, d)
	}
}

// numstat -z 的重命名记录是三段:计数段(路径位空)→ 原路径 → 新路径。
// 这里钉住解析口径:新路径拿到的必须是数字,而不是把原路径当 key。
func TestNumstatRenameFields(t *testing.T) {
	s, root := newEmptyRepo(t)
	writeFile := func(p, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(p)), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeFile("a.txt", "1\n2\n3\n")
	if _, err := s.SetIdentity("/", "张三", "zhang@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := s.StageAdd("/", []string{"a.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Commit("/", "init", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.run(root, nil, "mv", "a.txt", "b.txt"); err != nil {
		t.Fatal(err)
	}
	m := s.numstatSide(root, "--cached")
	if v, ok := m["b.txt"]; !ok || v[0] != "0" || v[1] != "0" {
		t.Errorf("新路径 b.txt 该拿到 0/0,实际 %v(ok=%v)", v, ok)
	}
	if _, ok := m["a.txt"]; ok {
		t.Errorf("原路径 a.txt 不该作为 key 出现")
	}
}
