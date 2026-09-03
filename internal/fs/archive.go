package fs

import (
	"archive/tar"
	"archive/zip"
	"compress/bzip2"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/bodgit/sevenzip"
	"github.com/nwaples/rardecode/v2"
	"github.com/ulikunitz/xz"
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

// ExtractArchive 解压 archive 到 dest。格式按文件名认(见 archiveKind)。
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
	mode, comp, ok := archiveKind(filepath.Base(srcAbs))
	if !ok {
		return fmt.Errorf("%w: 不支持解压这种格式(支持 zip / tar(.gz/.bz2/.xz) / rar / 7z / gz / bz2 / xz)", ErrBadQuery)
	}
	// 先认格式再建目录:不支持的格式不该留下一个空目录。
	if err := os.MkdirAll(destAbs, 0o755); err != nil {
		return err
	}
	switch mode {
	case "zip":
		return extractZip(destAbs, srcAbs)
	case "tar":
		return extractTar(destAbs, srcAbs, comp)
	case "rar":
		return extractRar(destAbs, srcAbs)
	case "7z":
		return extractSevenZip(destAbs, srcAbs)
	}
	return extractSingle(destAbs, srcAbs, comp)
}

// archiveKind 从文件名认出解压方式。mode 是容器格式,comp 是外层压缩("" = 没压)。
// 顺序有讲究:.tar.gz 必须在 .gz 之前判,否则会被当成单文件压缩。
func archiveKind(name string) (mode, comp string, ok bool) {
	l := strings.ToLower(name)
	switch {
	case strings.HasSuffix(l, ".zip"):
		return "zip", "", true
	case strings.HasSuffix(l, ".tar"):
		return "tar", "", true
	case strings.HasSuffix(l, ".tar.gz"), strings.HasSuffix(l, ".tgz"):
		return "tar", "gz", true
	case strings.HasSuffix(l, ".tar.bz2"), strings.HasSuffix(l, ".tbz"), strings.HasSuffix(l, ".tbz2"):
		return "tar", "bz2", true
	case strings.HasSuffix(l, ".tar.xz"), strings.HasSuffix(l, ".txz"):
		return "tar", "xz", true
	case strings.HasSuffix(l, ".rar"):
		return "rar", "", true
	case strings.HasSuffix(l, ".7z"):
		return "7z", "", true
	// 到这里 tar.* 都被上面挑走了,剩下的就是裸文件压缩:解开只有一个文件。
	case strings.HasSuffix(l, ".gz"):
		return "single", "gz", true
	case strings.HasSuffix(l, ".bz2"):
		return "single", "bz2", true
	case strings.HasSuffix(l, ".xz"):
		return "single", "xz", true
	}
	return "", "", false
}

// openCompressed 按 comp 在 r 外面套一层解压。comp 为空时原样返回。
// 三者都不持有 OS 资源(底层文件由调用方 Close),所以不用回传 Closer。
func openCompressed(r io.Reader, comp string) (io.Reader, error) {
	switch comp {
	case "":
		return r, nil
	case "gz":
		return gzip.NewReader(r)
	case "bz2":
		return bzip2.NewReader(r), nil
	case "xz":
		return xz.NewReader(r)
	}
	return nil, fmt.Errorf("%w: 未知压缩方式 %s", ErrBadQuery, comp)
}

// writeExtracted 把 r 的内容写到 target(自动补父目录)。
// perm 取包里记的权限(保住可执行位),包里没记就用 0644。
func writeExtracted(target string, r io.Reader, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	perm = perm.Perm()
	if perm == 0 {
		perm = 0o644
	}
	w, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, r); err != nil {
		w.Close()
		return err
	}
	return w.Close()
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
		fi := f.FileInfo()
		if fi.IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		// 只落普通文件。符号链接照搬进来的话,包里一条 "x -> /etc" 加一条 "x/passwd"
		// 就能绕过 safeJoin 写到 root 外面(safeJoin 只看名字,不跟着链接走)。
		if !fi.Mode().IsRegular() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		err = writeExtracted(target, rc, fi.Mode())
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// extractTar 解压 tar,comp 为外层压缩(""/gz/bz2/xz)。
func extractTar(dest, archivePath, comp string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()
	src, err := openCompressed(f, comp)
	if err != nil {
		return err
	}
	tr := tar.NewReader(src)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		target, err := safeJoin(dest, hdr.Name)
		if err != nil {
			return err
		}
		// 目录和普通文件之外(链接、设备节点)一律跳过,理由同 extractZip。
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := writeExtracted(target, tr, hdr.FileInfo().Mode()); err != nil {
				return err
			}
		}
	}
}

// extractRar 顺序读 rar 并落盘。分卷包由 OpenReader 自动续读后续卷;
// 顺序读也是 solid 包唯一能用的读法(逐个 Open 会被库拒绝)。
func extractRar(dest, archivePath string) error {
	rc, err := rardecode.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer rc.Close()
	for {
		hdr, err := rc.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		target, err := safeJoin(dest, hdr.Name)
		if err != nil {
			return err
		}
		if hdr.IsDir {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if !hdr.Mode().IsRegular() {
			continue
		}
		if err := writeExtracted(target, rc, hdr.Mode()); err != nil {
			return err
		}
	}
}

func extractSevenZip(dest, archivePath string) error {
	r, err := sevenzip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		target, err := safeJoin(dest, f.Name)
		if err != nil {
			return err
		}
		fi := f.FileInfo()
		if fi.IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if !fi.Mode().IsRegular() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		err = writeExtracted(target, rc, fi.Mode())
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// extractSingle 解压单文件压缩(.gz/.bz2/.xz):这类格式里没有文件名,
// 去掉压缩后缀当名字(note.txt.gz → note.txt)。
func extractSingle(dest, archivePath, comp string) error {
	base := filepath.Base(archivePath)
	name := strings.TrimSuffix(base, filepath.Ext(base))
	if name == "" {
		name = "data"
	}
	target, err := safeJoin(dest, name)
	if err != nil {
		return err
	}
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()
	src, err := openCompressed(f, comp)
	if err != nil {
		return err
	}
	return writeExtracted(target, src, 0o644)
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
		return listTar(abs, "", limit)
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return listTar(abs, "gz", limit)
	case strings.HasSuffix(lower, ".tar.bz2"), strings.HasSuffix(lower, ".tbz"), strings.HasSuffix(lower, ".tbz2"):
		return listTar(abs, "bz2", limit)
	case strings.HasSuffix(lower, ".tar.xz"), strings.HasSuffix(lower, ".txz"):
		return listTar(abs, "xz", limit)
	case strings.HasSuffix(lower, ".rar"):
		return listRar(abs, limit)
	case strings.HasSuffix(lower, ".7z"):
		return listSevenZip(abs, limit)
	}
	// 单文件压缩(.gz/.bz2/.xz)没有条目表可列 —— 里面就一个文件,只能解开才知道。
	return nil, false, fmt.Errorf("%w: 仅支持预览 zip / tar(.gz/.bz2/.xz) / rar / 7z", ErrBadQuery)
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

func listTar(abs, comp string, limit int) ([]ArchiveEntry, bool, error) {
	f, err := os.Open(abs)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	src, err := openCompressed(f, comp)
	if err != nil {
		return nil, false, err
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
