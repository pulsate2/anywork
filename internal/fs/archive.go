package fs

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/bodgit/sevenzip"
	"github.com/nwaples/rardecode/v2"
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
	// 压缩包本身也必须在 root 内:否则可以把 root 外的任意归档解到可见目录里。
	srcAbs, err := s.Resolve(archivePath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(destAbs, 0o755); err != nil {
		return err
	}
	if strings.HasSuffix(strings.ToLower(srcAbs), ".zip") {
		return extractZip(destAbs, srcAbs)
	}
	return extractTarGz(destAbs, srcAbs)
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
			w, cerr := os.Create(target)
			if cerr != nil {
				return cerr
			}
			_, werr := io.Copy(w, tr)
			w.Close()
			if werr != nil {
				return werr
			}
		}
	}
	return nil
}

// ---- 压缩包预览:只列条目,不解压 ----

// ArchiveEntry 压缩包内的一条记录。Name 是包内相对路径(正斜杠)。
type ArchiveEntry struct {
	Name  string `json:"name"`
	Dir   bool   `json:"dir"`
	Size  int64  `json:"size"`
	Mtime string `json:"mtime"`
}

// 超大包不全量返回,截断后由前端提示。
const archiveListLimit = 2000

// ListArchive 列出压缩包内的条目。纯读操作,不解压到磁盘。
// 第二个返回值表示是否因超过 limit 被截断。
func (s *Service) ListArchive(archivePath string, limit int) ([]ArchiveEntry, bool, error) {
	abs, err := s.Resolve(archivePath)
	if err != nil {
		return nil, false, err
	}
	if limit <= 0 || limit > archiveListLimit {
		limit = archiveListLimit
	}
	lower := strings.ToLower(abs)
	switch {
	case strings.HasSuffix(lower, ".zip"):
		return listZip(abs, limit)
	case strings.HasSuffix(lower, ".tar"):
		return listTar(abs, false, limit)
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return listTar(abs, true, limit)
	case strings.HasSuffix(lower, ".rar"):
		return listRar(abs, limit)
	case strings.HasSuffix(lower, ".7z"):
		return listSevenZip(abs, limit)
	}
	return nil, false, fmt.Errorf("%w: 仅支持预览 zip / tar / tar.gz / rar / 7z", ErrBadQuery)
}

func listZip(abs string, limit int) ([]ArchiveEntry, bool, error) {
	r, err := zip.OpenReader(abs)
	if err != nil {
		return nil, false, err
	}
	defer r.Close()
	out := make([]ArchiveEntry, 0, min(len(r.File), limit))
	for _, f := range r.File {
		if len(out) >= limit {
			return out, true, nil
		}
		out = append(out, ArchiveEntry{
			Name:  filepath.ToSlash(f.Name),
			Dir:   f.FileInfo().IsDir(),
			Size:  int64(f.UncompressedSize64),
			Mtime: f.Modified.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	return out, false, nil
}

func listTar(abs string, gzipped bool, limit int) ([]ArchiveEntry, bool, error) {
	f, err := os.Open(abs)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	var src io.Reader = f
	if gzipped {
		gz, gerr := gzip.NewReader(f)
		if gerr != nil {
			return nil, false, gerr
		}
		defer gz.Close()
		src = gz
	}
	tr := tar.NewReader(src)
	out := []ArchiveEntry{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, false, err
		}
		if len(out) >= limit {
			return out, true, nil
		}
		out = append(out, ArchiveEntry{
			Name:  filepath.ToSlash(hdr.Name),
			Dir:   hdr.Typeflag == tar.TypeDir,
			Size:  hdr.Size,
			Mtime: hdr.ModTime.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	return out, false, nil
}

// listRar 顺序读 rar 的文件头。分卷包由 OpenReader 自动续读后续卷。
func listRar(abs string, limit int) ([]ArchiveEntry, bool, error) {
	rc, err := rardecode.OpenReader(abs)
	if err != nil {
		return nil, false, err
	}
	defer rc.Close()
	out := []ArchiveEntry{}
	for {
		hdr, err := rc.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, false, err
		}
		if len(out) >= limit {
			return out, true, nil
		}
		out = append(out, ArchiveEntry{
			Name:  filepath.ToSlash(hdr.Name),
			Dir:   hdr.IsDir,
			Size:  hdr.UnPackedSize,
			Mtime: hdr.ModificationTime.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	return out, false, nil
}

// listSevenZip 只读 7z 的头部条目表。头部本身可能是压缩的,解码由库内部完成,
// 但文件内容不会被解压。
func listSevenZip(abs string, limit int) ([]ArchiveEntry, bool, error) {
	r, err := sevenzip.OpenReader(abs)
	if err != nil {
		return nil, false, err
	}
	defer r.Close()
	out := make([]ArchiveEntry, 0, min(len(r.File), limit))
	for _, f := range r.File {
		if len(out) >= limit {
			return out, true, nil
		}
		out = append(out, ArchiveEntry{
			Name:  filepath.ToSlash(f.Name),
			Dir:   f.FileInfo().IsDir(),
			Size:  int64(f.UncompressedSize),
			Mtime: f.Modified.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	return out, false, nil
}
