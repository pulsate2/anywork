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

	"github.com/ulikunitz/xz"
)

// CreateZip 将 src 目录打成 zip 写入 w(用于直接下载:包内以目录内容为顶层,
// 不带 src 自己那一层)。
func (s *Service) CreateZip(src string, w io.Writer) error {
	abs, err := s.Resolve(src)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(w)
	err = filepath.Walk(abs, func(p string, fi os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(abs, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		return writeZipEntry(zw, p, fi, filepath.ToSlash(rel))
	})
	if err != nil {
		zw.Close()
		return err
	}
	// Close 才写中央目录,它的错误不能丢:丢了就会送出一个"看着成功"的坏包。
	return zw.Close()
}

// CreateArchiveFile 把 paths(文件或目录,可多个)打成一个包落盘到 dest。
// 与 CreateZip 的区别:这个是"在服务器上压出一个文件",包内每项以自己的名字为顶层,
// 解开来就是原来那几个条目。
//
// 压成什么格式由 dest 的后缀决定,认名字用的是解压那边同一个 archiveKind ——
// 名字和内容永远对得上,不会压出一个叫 x.tar.gz 的 zip。能压的格式比能解的少:
// bz2 标准库只有解码器,7z/rar 没有能用的纯 Go 写入实现,这三种只能解不能压。
func (s *Service) CreateArchiveFile(dest string, paths []string) error {
	if err := s.allowWrite(); err != nil {
		return err
	}
	if len(paths) == 0 {
		return fmt.Errorf("%w: 没有要压缩的内容", ErrBadQuery)
	}
	destAbs, err := s.Resolve(dest)
	if err != nil {
		return err
	}
	mode, comp, ok := archiveKind(filepath.Base(destAbs))
	if !ok || !canWriteArchive(mode, comp) {
		return fmt.Errorf("%w: 不支持压缩成这种格式(支持 zip / tar / tar.gz / tar.xz)", ErrBadQuery)
	}
	// 先把源路径全解析一遍:任何一个越界或重名就整个不做,不留半个包。
	srcs := make([]string, 0, len(paths))
	seen := make(map[string]bool, len(paths))
	for _, p := range paths {
		abs, err := s.Resolve(p)
		if err != nil {
			return err
		}
		// 顶层同名的两项在包里会撞车(zip 允许重名,但解压方只会看到一个)。
		base := filepath.Base(abs)
		if seen[base] {
			return fmt.Errorf("%w: 有重名项 %s", ErrBadQuery, base)
		}
		seen[base] = true
		srcs = append(srcs, abs)
	}
	if err := os.MkdirAll(filepath.Dir(destAbs), 0o755); err != nil {
		return err
	}
	// 和 Write 一样先写临时文件再改名:中途失败不会留下半个包。临时文件在扫描时
	// 跳过 —— dest 通常就落在被压缩的目录里,不跳就会把正在写的自己也装进去。
	tmp, err := os.CreateTemp(filepath.Dir(destAbs), ".lr-pack-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := writeArchive(tmp, srcs, mode, comp, tmpName); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// CreateTemp 给的是 0600,压出来的包要和普通新文件一样能读。
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	os.Remove(destAbs)
	return renameCrossDevice(tmpName, destAbs)
}

// canWriteArchive 说明哪些 archiveKind 认得的格式是压得出来的。
func canWriteArchive(mode, comp string) bool {
	switch mode {
	case "zip":
		return true
	case "tar":
		// bz2 没有编码器(标准库 compress/bzip2 只能解)。
		return comp == "" || comp == "gz" || comp == "xz"
	}
	// single(裸 .gz/.bz2/.xz)只装得下一个文件,压缩这边一律走容器格式;7z/rar 写不了。
	return false
}

// writeArchive 把 srcs 写成一个包给 w。skip 是要跳过的绝对路径(正在写的临时包自己)。
func writeArchive(w io.Writer, srcs []string, mode, comp, skip string) error {
	if mode == "zip" {
		zw := zip.NewWriter(w)
		if err := walkSources(srcs, skip, func(p string, fi os.FileInfo, name string) error {
			return writeZipEntry(zw, p, fi, name)
		}); err != nil {
			zw.Close()
			return err
		}
		// Close 才写中央目录,它的错误不能丢:丢了就会留下一个"看着成功"的坏包。
		return zw.Close()
	}
	cw, err := newCompressor(w, comp)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(cw)
	if err := walkSources(srcs, skip, func(p string, fi os.FileInfo, name string) error {
		return writeTarEntry(tw, p, fi, name)
	}); err != nil {
		tw.Close()
		cw.Close()
		return err
	}
	// tar 的收尾块和压缩层的 flush 都在各自的 Close 里,两层都得检查。
	if err := tw.Close(); err != nil {
		cw.Close()
		return err
	}
	return cw.Close()
}

// newCompressor 在 w 外面套一层压缩。comp 为空时返回一个 Close 什么都不做的包装,
// 让调用方只有一条收尾路径。
func newCompressor(w io.Writer, comp string) (io.WriteCloser, error) {
	switch comp {
	case "":
		return nopWriteCloser{w}, nil
	case "gz":
		return gzip.NewWriter(w), nil
	case "xz":
		return xz.NewWriter(w)
	}
	return nil, fmt.Errorf("%w: 不支持压缩成 %s", ErrBadQuery, comp)
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// walkSources 遍历每个源(含它自己那一层),把每一项交给 emit。name 是包内路径:
// 顶层就是源自己的名字,不带它所在目录那一层。skip 为空表示不跳过任何路径。
func walkSources(srcs []string, skip string, emit func(p string, fi os.FileInfo, name string) error) error {
	for _, abs := range srcs {
		top := filepath.Base(abs)
		err := filepath.Walk(abs, func(p string, fi os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if skip != "" && p == skip {
				return nil
			}
			rel, err := filepath.Rel(abs, p)
			if err != nil {
				return err
			}
			name := top
			if rel != "." {
				name = top + "/" + filepath.ToSlash(rel)
			}
			return emit(p, fi, name)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// writeTarEntry 往 tw 里写一条,name 是包内路径(正斜杠,目录不用带尾斜杠)。
func writeTarEntry(tw *tar.Writer, p string, fi os.FileInfo, name string) error {
	// 跳过规则同 writeZipEntry:只收目录和普通文件。
	if !fi.IsDir() && !fi.Mode().IsRegular() {
		return nil
	}
	hdr, err := tar.FileInfoHeader(fi, "")
	if err != nil {
		return err
	}
	// FileInfoHeader 只填文件名那一段,包内完整路径要自己覆盖;目录按惯例带尾斜杠。
	hdr.Name = name
	if fi.IsDir() {
		hdr.Name += "/"
	}
	if err := tw.WriteHeader(hdr); err != nil {
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
	// tar 头里的 Size 是 Walk 那一刻的大小,之后多写少写都会让整包报废。文件正好在
	// 这时被改动(压一个还在写的日志)就以头为准:长了截掉,短了补零 —— 宁可包里
	// 那一个文件不准,也不要压出一个打不开的包。
	n, err := io.CopyN(tw, f, hdr.Size)
	if errors.Is(err, io.EOF) {
		_, err = io.CopyN(tw, zeroReader{}, hdr.Size-n)
	}
	return err
}

// zeroReader 读出来永远是 0,用于补齐 tar 头里声明的长度。
type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// writeZipEntry 往 zw 里写一条,name 是包内路径(正斜杠,目录不用带尾斜杠)。
func writeZipEntry(zw *zip.Writer, p string, fi os.FileInfo, name string) error {
	// 目录和普通文件之外(符号链接、设备节点、socket)跳过:zip 存不了它们,
	// 而 Walk 不跟链接走,硬存只会把链接目标的内容抄成一个假文件。
	if !fi.IsDir() && !fi.Mode().IsRegular() {
		return nil
	}
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
}
