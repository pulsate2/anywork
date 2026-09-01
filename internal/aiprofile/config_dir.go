package aiprofile

import (
	"io"
	"os"
	"path/filepath"
)

// cloneConfig 把配置目录复制进新档案。from 为 "home" 时从 ~/.claude、~/.codex
// 克隆,否则从已有档案名克隆。
func (s *service) cloneConfig(from, name string) error {
	dst := s.profileDir(name)
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return err
	}
	if from == "home" {
		home, _ := os.UserHomeDir()
		if err := copyDirIfExists(filepath.Join(home, ".claude"),
			filepath.Join(dst, "claude")); err != nil {
			return err
		}
		if err := copyDirIfExists(filepath.Join(home, ".codex"),
			filepath.Join(dst, "codex")); err != nil {
			return err
		}
		return nil
	}
	// 从已有档案克隆。
	src := s.profileDir(from)
	if err := copyDirIfExists(filepath.Join(src, "claude"),
		filepath.Join(dst, "claude")); err != nil {
		return err
	}
	return copyDirIfExists(filepath.Join(src, "codex"),
		filepath.Join(dst, "codex"))
}

// copyDirIfExists 复制目录;源不存在时为 no-op。
func copyDirIfExists(src, dst string) error {
	fi, err := os.Stat(src)
	if err != nil || !fi.IsDir() {
		return nil
	}
	return copyDir(src, dst)
}

func copyDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		sp := filepath.Join(src, e.Name())
		dp := filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := copyDir(sp, dp); err != nil {
				return err
			}
			continue
		}
		if err := copyFile(sp, dp); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
