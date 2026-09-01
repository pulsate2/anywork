package fs

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var errEscape = errors.New("archive path escapes target")

// safeJoin 将 archive 内相对路径安全 join,拒绝逃逸(zip-slip)。
func safeJoin(dest, name string) (string, error) {
	dest = filepath.Clean(dest)
	if filepath.IsAbs(name) {
		return "", errEscape
	}
	joined := filepath.Join(dest, name)
	rel, err := filepath.Rel(dest, joined)
	if err != nil {
		return "", errEscape
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errEscape
	}
	return joined, nil
}

// ExtractArchive 解压 archive 到 dest。按扩展名选 zip 或 tar.gz。
func (s *Service) ExtractArchive(dest, archivePath string) error {
	if err := s.allowWrite(); err != nil {
		return err
	}
	destAbs, err := s.Resolve(dest)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(destAbs, 0o755); err != nil {
		return err
	}
	if strings.HasSuffix(strings.ToLower(archivePath), ".zip") {
		return extractZip(destAbs, archivePath)
	}
	return extractTarGz(destAbs, archivePath)
}

func extractZip(dest, archivePath string) error {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		target, err := safeJoin(dest, f.Name)
		if err != nil {
			return err
		}
		if f.FileInfo().IsDir() {
			_ = os.MkdirAll(target, 0o755)
			continue
		}
		_ = os.MkdirAll(filepath.Dir(target), 0o755)
		rc, _ := f.Open()
		w, _ := os.Create(target)
		io.Copy(w, rc)
		w.Close()
		rc.Close()
	}
	return nil
}
func extractTarGz(dest, archivePath string) error {
	f, err := os.Open(archivePath)
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
		target, err := safeJoin(dest, hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			_ = os.MkdirAll(target, 0o755)
		case tar.TypeReg:
			_ = os.MkdirAll(filepath.Dir(target), 0o755)
			w, _ := os.Create(target)
			w.Close()
		}
	}
	return nil
}
