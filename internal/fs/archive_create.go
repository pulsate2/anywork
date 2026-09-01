package fs

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
)

// CreateZip 将 src 目录打成 zip 写入 w。
func (s *Service) CreateZip(src string, w io.Writer) error {
	abs, err := s.Resolve(src)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(w)
	defer zw.Close()
	return filepath.Walk(abs, func(p string, fi os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		name, err := filepath.Rel(abs, p)
		if err != nil {
			return err
		}
		if name == "." {
			return nil
		}
		name = filepath.ToSlash(name)
		hdr, err := zip.FileInfoHeader(fi)
		if err != nil {
			return err
		}
		hdr.Name = name
		if fi.IsDir() {
			hdr.Name += "/"
		} else {
			hdr.Method = zip.Deflate
		}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		if fi.IsDir() {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(w, f)
		return err
	})
}
