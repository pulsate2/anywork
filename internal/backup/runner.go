package backup

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// remoteDirFor 快照在 WebDAV 上的存放目录。空 = 直接放在任务配置的那个地址下,
// 不再套 <源目录名>/<任务ID>/ 两层。
// 代价:多个任务指向同一目录时快照会混在一起(文件名带时间戳,同一秒才会互撞);
// 反过来轮转/列表都只认自己命名的 backup-*.tar.gz,不会碰到别人的文件。
func remoteDirFor(j *Job) string {
	return ""
}

// remotePath 拼远端路径,兼容空的目录前缀。
func remotePath(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

// isSnapshotName 是否是本系统生成的快照名。
func isSnapshotName(name string) bool {
	return strings.HasPrefix(name, "backup-") && strings.HasSuffix(name, ".tar.gz")
}

// fileStamp 指纹输入:路径 + 大小 + 纳秒级 mtime。
type fileStamp struct {
	rel   string
	size  int64
	mtime int64
}

// computeFingerprint 内容指纹:源目录 + 排除规则 + 每文件(路径|大小|mtime)。
// walk 顺序天然稳定;excludes 或文件清单任一变化,指纹必变。
func computeFingerprint(sourceDir string, excludes []string, files []fileStamp) string {
	h := sha256.New()
	fmt.Fprintf(h, "dir:%s\n", sourceDir)
	fmt.Fprintf(h, "excl:%s\n", strings.Join(excludes, "\x00"))
	for _, f := range files {
		fmt.Fprintf(h, "%s|%d|%d\n", f.rel, f.size, f.mtime)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// doBackup 流式打包 sourceDir 到 WebDAV(O(1) 内存),并写元数据。
// force=false 且指纹与上次相同 → 跳过(skipped=true),零网络开销。
func (m *Manager) doBackup(j *Job, force bool) (bool, error) {
	if j.SourceDir == "" || j.WebDAVURL == "" {
		return false, fmt.Errorf("来源目录或 WebDAV 地址为空")
	}
	if !m.dirAllowed(j.SourceDir) {
		return false, fmt.Errorf("来源目录超出根边界")
	}
	matcher := newIgnoreMatcher(j.Excludes)
	// 反选(!)可能救回被排除目录里的文件,此时不能剪枝,只能逐文件判断。
	prunable := !matcher.hasNegate()
	// 预扫描:收集文件 + 总量 + 指纹输入(用于跳过未变化备份)。
	files := []string{}
	fping := []fileStamp{}
	var total int64
	filepath.Walk(j.SourceDir, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, rerr := filepath.Rel(j.SourceDir, p)
		if rerr != nil || rel == "." {
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		if fi.IsDir() {
			// 目录命中即整棵剪枝,不再深入(node_modules 里不用逐个判断)。
			if prunable && matcher.shouldIgnore(relSlash, true) {
				return filepath.SkipDir
			}
			return nil
		}
		if matcher.shouldIgnore(relSlash, false) {
			return nil
		}
		files = append(files, p)
		fping = append(fping, fileStamp{rel: relSlash, size: fi.Size(), mtime: fi.ModTime().UnixNano()})
		total += fi.Size()
		return nil
	})

	// 内容未变化则跳过(手动 force 除外)。
	j.mu.Lock()
	lastFP := j.LastFP
	j.mu.Unlock()
	fp := computeFingerprint(j.SourceDir, j.Excludes, fping)
	if !force && fp == lastFP {
		j.mu.Lock()
		j.Progress = "内容未变化,已跳过"
		j.mu.Unlock()
		return true, nil
	}

	ts := time.Now().Format("20060102-150405")
	remoteDir := remoteDirFor(j)
	c := newWebdav(j.WebDAVURL, j.WebDAVUser, j.WebDAVPass)
	if err := c.ensureDir(remoteDir); err != nil {
		return false, err
	}
	base := "backup-" + ts
	gzPath := remotePath(remoteDir, base+".tar.gz")

	// 先落成临时文件再上传。此前是流式打包(PUT 一个 io.Pipe)省一次落盘,
	// 但 body 长度未知会退化成 chunked,而且上传耗时完全不受控:
	// 服务端对 chunked 处理不好、或按体积上限提前应答关连接时,客户端还在往
	// 管道里写,报出来的是 io.ErrClosedPipe("io: read/write on closed pipe"),
	// 真正的原因(413/507/超时)全被吃掉 —— 文件越多包越大越容易撞上。
	// 落盘后长度已知:PUT 带准确 Content-Length,超时也能按体量给。
	tmp, err := os.CreateTemp("", "lr-backup-*.tar.gz")
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp.Name())
	hash, size, terr := tarStream(j.SourceDir, files, matcher, tmp)
	if terr != nil {
		tmp.Close()
		return false, terr
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	// size 是原始内容字节数(给元数据看),上传要用压缩包自己的大小。
	fi, err := os.Stat(tmp.Name())
	if err != nil {
		return false, err
	}
	f, err := os.Open(tmp.Name())
	if err != nil {
		return false, err
	}
	defer f.Close()
	if err := c.put(gzPath, f, fi.Size()); err != nil {
		return false, err
	}

	// 写元数据。
	meta := map[string]any{
		"source":   j.SourceDir,
		"excludes": j.Excludes,
		"mtime":    time.Now().UTC().Format(time.RFC3339),
		"bytes":    size,
		"sha256":   hash,
		"fp":       fp,
	}
	mb, _ := json.Marshal(meta)
	if err := c.put(remotePath(remoteDir, base+".json"), strings.NewReader(string(mb)), int64(len(mb))); err != nil {
		return false, err
	}
	m.updateFingerprint(j.ID, fp)
	j.mu.Lock()
	j.Progress = fmt.Sprintf("已备份 %d 个文件 %s", len(files), humanSize(size))
	j.mu.Unlock()
	return false, nil
}

// tarStream 把 files 打包成 tar.gz 写入 w,返回 sha256 与总字节。
func tarStream(root string, files []string, matcher *ignoreMatcher, w io.Writer) (string, int64, error) {
	hasher := sha256.New()
	hw := io.MultiWriter(w, hasher)
	gz := gzip.NewWriter(hw)
	tw := tar.NewWriter(gz)
	var total int64
	for _, p := range files {
		rel, _ := filepath.Rel(root, p)
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		hdr, err := tar.FileInfoHeader(fi, "")
		if err != nil {
			continue
		}
		hdr.Name = filepath.ToSlash(rel)
		// PAX 才存得下纳秒级 mtime;恢复保留 mtime 后指纹才能与备份时一致。
		hdr.Format = tar.FormatPAX
		if err := tw.WriteHeader(hdr); err != nil {
			return "", 0, err
		}
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		n, err := io.Copy(tw, f)
		f.Close()
		if err != nil {
			return "", 0, err
		}
		total += n
	}
	if err := tw.Close(); err != nil {
		return "", 0, err
	}
	if err := gz.Close(); err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hasher.Sum(nil)), total, nil
}

// RestoreLatest 拉取远程最新快照并恢复。
func (m *Manager) RestoreLatest(id string) error {
	j := m.Get(id)
	if j == nil {
		return fmt.Errorf("任务不存在")
	}
	c := newWebdav(j.WebDAVURL, j.WebDAVUser, j.WebDAVPass)
	remoteDir := remoteDirFor(j)
	entries, err := c.propfind(remoteDir)
	if err != nil {
		return err
	}
	var latest string
	var latestTS time.Time
	for _, e := range entries {
		if isSnapshotName(filepath.Base(e.Href)) {
			if ts, ok := parseSnapshotTS(e.Href); ok && ts.After(latestTS) {
				latestTS = ts
				latest = e.Href
			}
		}
	}
	if latest == "" {
		return fmt.Errorf("无可用快照")
	}
	return m.restoreSnapshot(j, c, c.rel(latest))
}

func parseSnapshotTS(href string) (time.Time, bool) {
	base := filepath.Base(href)
	base = strings.TrimSuffix(base, ".tar.gz")
	base = strings.TrimPrefix(base, "backup-")
	t, err := time.Parse("20060102-150405", base)
	return t, err == nil
}

func humanSize(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%dB", n)
	}
	if n < 1024*1024 {
		return fmt.Sprintf("%.1fKB", float64(n)/1024)
	}
	return fmt.Sprintf("%.1fMB", float64(n)/1024/1024)
}

// restoreSnapshot 下载并解压覆盖到 sourceDir(校验 sha256)。
func (m *Manager) restoreSnapshot(j *Job, c *webdavClient, href string) error {
	// 取元数据校验。顺带拿到原始体积:下载时限按它估,
	// 否则大快照只能走 5 分钟底,慢线路上的恢复会被时限掐断。
	metaPath := strings.TrimSuffix(href, ".tar.gz") + ".json"
	wantHash := ""
	var estSize int64
	if rc, err := c.get(metaPath, 0); err == nil {
		b, _ := io.ReadAll(rc)
		rc.Close()
		var meta struct {
			Sha256 string `json:"sha256"`
			Bytes  int64  `json:"bytes"`
		}
		json.Unmarshal(b, &meta)
		wantHash = meta.Sha256
		estSize = meta.Bytes
	}
	// 下载到临时文件。
	tmp, err := os.CreateTemp("", "lr-restore-*.tar.gz")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	rc, err := c.get(href, estSize)
	if err != nil {
		return err
	}
	hasher := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, hasher), rc); err != nil {
		rc.Close()
		return err
	}
	rc.Close()
	tmp.Close()
	if wantHash != "" && hex.EncodeToString(hasher.Sum(nil)) != wantHash {
		return fmt.Errorf("sha256 校验失败")
	}
	// 解压到临时目录后整体挪(降低半目录风险)。
	staged, err := os.MkdirTemp("", "lr-restore-staged-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staged)
	if err := extractTarGz(tmp.Name(), staged, newIgnoreMatcher(j.Excludes)); err != nil {
		return err
	}
	return mergeDir(staged, j.SourceDir, newIgnoreMatcher(j.Excludes))
}

// Rotate 保留轮转:删除超出 retention 的最旧快照。
func (m *Manager) Rotate(id string) error {
	j := m.Get(id)
	if j == nil || j.Retention <= 0 {
		return nil
	}
	c := newWebdav(j.WebDAVURL, j.WebDAVUser, j.WebDAVPass)
	entries, err := c.propfind(remoteDirFor(j))
	if err != nil {
		return err
	}
	var snaps []string
	for _, e := range entries {
		// 扁平布局下这个目录里可能有别的东西,只轮转我们自己命名的快照。
		if isSnapshotName(filepath.Base(e.Href)) {
			snaps = append(snaps, e.Href)
		}
	}
	sort.Slice(snaps, func(a, b int) bool { return snaps[a] < snaps[b] })
	for len(snaps) > j.Retention {
		old := snaps[0]
		snaps = snaps[1:]
		rel := c.rel(old)
		_ = c.del(rel)
		_ = c.del(strings.TrimSuffix(rel, ".tar.gz") + ".json")
	}
	return nil
}

// RestoreSnapshot 恢复指定快照(名称如 backup-xxx.tar.gz 或 URL 路径)。
func (m *Manager) RestoreSnapshot(id, snap string) error {
	j := m.Get(id)
	if j == nil {
		return fmt.Errorf("任务不存在")
	}
	c := newWebdav(j.WebDAVURL, j.WebDAVUser, j.WebDAVPass)
	// 规范化:只有文件名时补上远程目录前缀。
	href := snap
	if !strings.Contains(snap, "/") {
		href = remotePath(remoteDirFor(j), snap)
	}
	return m.restoreSnapshot(j, c, href)
}
