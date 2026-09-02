package fs

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func (s *Service) Write(p string, r io.Reader) error {
	if err := s.allowWrite(); err != nil {
		return err
	}
	abs, err := s.Resolve(p)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	perm := os.FileMode(0o644)
	if fi, err := os.Stat(abs); err == nil {
		perm = fi.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(abs), ".lr-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return err
	}
	os.Remove(abs)
	return renameCrossDevice(tmpName, abs)
}

func renameCrossDevice(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Remove(src)
}

func (s *Service) MkDir(p string) error {
	if err := s.allowWrite(); err != nil {
		return err
	}
	abs, err := s.Resolve(p)
	if err != nil {
		return err
	}
	return os.MkdirAll(abs, 0o755)
}

// Touch 新建空文件。O_EXCL 保证不会静默覆盖同名文件 —— 这是它和 Write("") 的区别。
func (s *Service) Touch(p string) error {
	if err := s.allowWrite(); err != nil {
		return err
	}
	abs, err := s.Resolve(p)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(abs, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%w: 同名文件或目录已存在", ErrBadQuery)
		}
		return err
	}
	return f.Close()
}

func (s *Service) Rename(src, dst string) error {
	if err := s.allowWrite(); err != nil {
		return err
	}
	absSrc, err := s.Resolve(src)
	if err != nil {
		return err
	}
	absDst, err := s.Resolve(dst)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(absDst), 0o755); err != nil {
		return err
	}
	return renameCrossDevice(absSrc, absDst)
}

func (s *Service) Copy(src, dst string) error {
	if err := s.allowWrite(); err != nil {
		return err
	}
	absSrc, err := s.Resolve(src)
	if err != nil {
		return err
	}
	absDst, err := s.Resolve(dst)
	if err != nil {
		return err
	}
	fi, err := os.Stat(absSrc)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		return copyDir(absSrc, absDst)
	}
	return copyFile(absSrc, absDst)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func copyDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		s := filepath.Join(src, e.Name())
		d := filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := copyDir(s, d); err != nil {
				return err
			}
		} else {
			if err := copyFile(s, d); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) Move(src, dst string) error {
	// 同目录即 rename;跨目录对目录整体移动也走 rename(同盘)。
	return s.Rename(src, dst)
}

func (s *Service) Delete(p string) error {
	if err := s.allowWrite(); err != nil {
		return err
	}
	abs, err := s.Resolve(p)
	if err != nil {
		return err
	}
	return os.RemoveAll(abs)
}
