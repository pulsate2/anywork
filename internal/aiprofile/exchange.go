package aiprofile

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// ExportWriter 把档案以 tar.gz 流式写入 w。包含 env.json + claude/ + codex/。
func (s *service) ExportWriter(name string, w io.Writer) error {
	if !nameOK(name) {
		return ErrNotFound
	}
	dir := s.profileDir(name)
	if _, err := os.Stat(dir); err != nil {
		return ErrNotFound
	}
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	err := addDir(tw, dir, "")
	if err != nil {
		tw.Close()
		gz.Close()
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

func addDir(tw *tar.Writer, dir, prefix string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		name := e.Name()
		if prefix != "" {
			name = prefix + "/" + e.Name()
		}
		if e.IsDir() {
			if err := tw.WriteHeader(&tar.Header{
				Name: name + "/", Typeflag: tar.TypeDir, Mode: 0o700}); err != nil {
				return err
			}
			if err := addDir(tw, p, name); err != nil {
				return err
			}
			continue
		}
		fi, err := os.Stat(p)
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o600, Size: fi.Size(), ModTime: fi.ModTime()}); err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		if _, err := io.Copy(tw, f); err != nil {
			f.Close()
			return err
		}
		f.Close()
	}
	return nil
}

// Import 从 r 读入 tar.gz 档案内容到 name。覆盖已有档案。
// 顶层条目可能带目录前缀,统一解压到 profiles/<name>/ 下。
func (s *service) Import(name string, r io.Reader) error {
	if !nameOK(name) {
		return errorsNew("invalid profile name")
	}
	dir := s.profileDir(name)
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	gz, err := gzip.NewReader(r)
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
		clean := sanitizePath(hdr.Name)
		if clean == "" {
			continue
		}
		dest := filepath.Join(dir, clean)
		if !withinWithin(dest, dir) {
			return errorsNew("archive path escapes")
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			_ = os.MkdirAll(dest, 0o700)
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
				return err
			}
			f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		}
	}
	return nil
}

func errorsNew(msg string) error { return errors.New(msg) }
