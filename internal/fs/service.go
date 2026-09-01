// Package fs 实现文件操作:列表/读写/流式上传下载/搜索/压缩解压,
// 全部流式、路径安全(root 边界 + zip-slip 防护)。
package fs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ErrForbidden 表示路径越出 root 边界。
var ErrForbidden = errors.New("path outside root")

// Service 持有 root 边界,是所有文件操作的入口。
type Service struct {
	Root     string
	ReadOnly bool
}

func NewService(root string, readonly bool) *Service {
	return &Service{Root: root, ReadOnly: readonly}
}

// Resolve 将用户提供的绝对/相对路径归一化到 root 内的安全绝对路径。
// 语义:入参优先按"相对 root"解释(与前端 cwd = "/" 一致);若入参本身就是
// root 边界内的绝对路径(工作区表里存的就是绝对路径),则直接采用。
func (s *Service) Resolve(p string) (string, error) {
	root := filepath.Clean(s.Root)
	if p == "" || p == "/" || p == "." {
		return root, nil
	}
	in := filepath.FromSlash(p)
	// 旧数据/前端可能给出 "/D:/x" 这种带前导斜杠的盘符路径,还原成 "D:\x"。
	if trimmed := strings.TrimPrefix(in, string(filepath.Separator)); filepath.VolumeName(trimmed) != "" {
		in = trimmed
	}
	// 单独的盘符("D:")在 Windows 语义里是"该盘当前目录",Clean 会得到 "D:.";
	// 前端从 D:/projects 上溯到盘根就是这个形状,补上分隔符当盘根处理。
	if vol := filepath.VolumeName(in); vol != "" && vol == in {
		in += string(filepath.Separator)
	}
	clean := filepath.Clean(in)
	if filepath.IsAbs(clean) && within(clean, root) {
		return clean, nil
	}
	// 带盘符/UNC 前缀的入参只可能是绝对路径:上面没通过就是越界,
	// 不能再当成 root 相对路径去 Join(否则拼出 C:\D:\x 这种非法路径)。
	if filepath.VolumeName(clean) != "" {
		return "", ErrForbidden
	}
	rel := strings.TrimPrefix(clean, string(filepath.Separator))
	abs := filepath.Join(root, rel)
	// 校验仍在 root 边界内(防 /../ 逃逸)。
	if within(abs, root) {
		return abs, nil
	}
	return "", ErrForbidden
}

// within 判断 abs 是否等于 root 或在 root 之下。root 可能自带尾分隔符(如 "/" 或 "D:\")。
func within(abs, root string) bool {
	if abs == root {
		return true
	}
	sep := string(filepath.Separator)
	prefix := root
	if !strings.HasSuffix(prefix, sep) {
		prefix += sep
	}
	return strings.HasPrefix(abs, prefix)
}

// allowWrite 只读模式下拒绝写操作。
func (s *Service) allowWrite() error {
	if s.ReadOnly {
		return errors.New("readonly mode")
	}
	return nil
}

// ListEntry 目录列表的单条记录。
type ListEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Dir     bool   `json:"dir"`
	Size    int64  `json:"size"`
	Mtime   string `json:"mtime"`
	Mode    string `json:"mode"`
	Symlink bool   `json:"symlink"`
	Target  string `json:"target,omitempty"`
}

// List 列出目录,目录优先排序。
func (s *Service) List(dir string) ([]ListEntry, error) {
	abs, err := s.Resolve(dir)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}
	out := make([]ListEntry, 0, len(entries))
	for _, e := range entries {
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		le := ListEntry{
			Name: e.Name(),
			// 统一返回 root 内的绝对路径、正斜杠形式:前端只做字符串切分即可上溯,
			// 且能原样回传给任何 fs/git 接口(Resolve 接受 root 内绝对路径)。
			Path:  filepath.ToSlash(filepath.Join(abs, e.Name())),
			Size:  info.Size(),
			Mtime: info.ModTime().UTC().Format("2006-01-02T15:04:05Z"),
			Mode:  info.Mode().String(),
		}
		if e.Type()&os.ModeSymlink != 0 {
			le.Symlink = true
			if t, terr := os.Readlink(filepath.Join(abs, e.Name())); terr == nil {
				le.Target = t
			}
			if fi, serr := os.Stat(filepath.Join(abs, e.Name())); serr == nil {
				le.Dir = fi.IsDir()
				le.Size = fi.Size()
			}
		} else {
			le.Dir = e.IsDir()
		}
		out = append(out, le)
	}
	// 目录优先,其次名称。
	sortEntries(out)
	return out, nil
}

func sortEntries(list []ListEntry) {
	// 稳定排序:目录优先,再按名称(忽略大小写)。
	for i := 1; i < len(list); i++ {
		for j := i; j > 0; j-- {
			a, b := list[j-1], list[j]
			less := false
			if a.Dir != b.Dir {
				less = a.Dir // 目录在前
			} else {
				less = strings.ToLower(a.Name) < strings.ToLower(b.Name)
			}
			if less {
				list[j-1], list[j] = list[j], list[j-1]
			} else {
				break
			}
		}
	}
}

// ReadInfo 供读文件:判断二进制。
func (s *Service) ReadInfo(p string) (*os.File, int64, bool, error) {
	abs, err := s.Resolve(p)
	if err != nil {
		return nil, 0, false, err
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, 0, false, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, false, err
	}
	if fi.IsDir() {
		f.Close()
		return nil, 0, false, errors.New("is a directory")
	}
	// 探测前 1024 字节是否有 NUL(二进制启发).
	buf := make([]byte, 1024)
	n, _ := f.Read(buf)
	f.Seek(0, 0)
	binary := containsNUL(buf[:n])
	return f, fi.Size(), binary, nil
}

func containsNUL(b []byte) bool {
	for _, c := range b {
		if c == 0 {
			return true
		}
	}
	return false
}
