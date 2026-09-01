package backup

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// extractTarGz 把 tar.gz 解压到 dir(遵循 matcher 排除)。
func extractTarGz(src, dir string, matcher *ignoreMatcher) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := filepath.ToSlash(hdr.Name)
		if matcher != nil && matcher.shouldIgnore(name) {
			continue
		}
		dest := filepath.Join(dir, name)
		if !within(dest, dir) {
			continue // 防逃逸
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			_ = os.MkdirAll(dest, 0o755)
		case tar.TypeReg, tar.TypeRegA:
			_ = os.MkdirAll(filepath.Dir(dest), 0o755)
			out, err := os.Create(dest)
			if err != nil {
				continue
			}
			io.Copy(out, tr)
			out.Close()
		}
	}
	return nil
}

// mergeDir 把 staged 内容覆盖式合并进 dst。
func mergeDir(staged, dst string, matcher *ignoreMatcher) error {
	return filepath.Walk(staged, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(staged, p)
		relSlash := filepath.ToSlash(rel)
		if matcher != nil && matcher.shouldIgnore(relSlash) {
			if fi.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if rel == "." {
			return nil
		}
		dest := filepath.Join(dst, rel)
		if fi.IsDir() {
			return os.MkdirAll(dest, 0o755)
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return nil
		}
		return copyFileAtomic(p, dest)
	})
}

func copyFileAtomic(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	out.Close()
	return os.Rename(tmp, dst)
}

// within 校验 dest 在 base 边界内。
func within(dest, base string) bool {
	dest = filepath.Clean(dest)
	base = filepath.Clean(base)
	if dest == base {
		return true
	}
	return strings.HasPrefix(dest, base+string(filepath.Separator))
}

// dirAllowed 校验目录在文件根边界内。
func (m *Manager) dirAllowed(dir string) bool {
	if m.root == "" || m.root == "/" {
		return true
	}
	root := filepath.Clean(m.root)
	dir = filepath.Clean(dir)
	return dir == root || strings.HasPrefix(dir, root+string(filepath.Separator))
}
