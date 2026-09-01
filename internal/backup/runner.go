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

// remoteDirFor 计算远程目录:基名/jobID。
func remoteDirFor(j *Job) string {
	return filepath.Join(filepath.Base(j.SourceDir), j.ID)
}

// doBackup 流式打包 sourceDir 到 WebDAV(O(1) 内存),并写元数据。
func (m *Manager) doBackup(j *Job) error {
	if j.SourceDir == "" || j.WebDAVURL == "" {
		return fmt.Errorf("来源目录或 WebDAV 地址为空")
	}
	if !m.dirAllowed(j.SourceDir) {
		return fmt.Errorf("来源目录超出根边界")
	}
	matcher := newIgnoreMatcher(j.Excludes)
	// 预扫描:收集文件 + 总量(用于进度)。
	files := []string{}
	var total int64
	filepath.Walk(j.SourceDir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(j.SourceDir, p)
		if rerr != nil {
			return nil
		}
		if matcher.shouldIgnore(filepath.ToSlash(rel)) {
			return nil
		}
		files = append(files, p)
		total += fi.Size()
		return nil
	})

	ts := time.Now().Format("20060102-150405")
	remoteDir := remoteDirFor(j)
	c := newWebdav(j.WebDAVURL, j.WebDAVUser, j.WebDAVPass)
	if err := c.ensureDir(remoteDir); err != nil {
		return err
	}
	base := "backup-" + ts
	gzPath := remoteDir + "/" + base + ".tar.gz"

	// 流式打包并 PUT(用 io.Pipe 边打边上传)。
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		werr := c.put(gzPath, pr)
		pr.CloseWithError(werr)
		done <- werr
	}()
	hash, size, terr := tarStream(j.SourceDir, files, matcher, pw)
	if terr != nil {
		pw.CloseWithError(terr)
		return terr
	}
	pw.Close()
	if err := <-done; err != nil {
		return err
	}

	// 写元数据。
	meta := map[string]any{
		"source":   j.SourceDir,
		"excludes": j.Excludes,
		"mtime":    time.Now().UTC().Format(time.RFC3339),
		"bytes":    size,
		"sha256":   hash,
	}
	mb, _ := json.Marshal(meta)
	if err := c.put(remoteDir+"/"+base+".json", strings.NewReader(string(mb))); err != nil {
		return err
	}
	j.mu.Lock()
	j.Progress = fmt.Sprintf("已备份 %d 个文件 %s", len(files), humanSize(size))
	j.mu.Unlock()
	return nil
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
		if strings.HasSuffix(e.Href, ".tar.gz") {
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
	// 取元数据校验。
	metaPath := strings.TrimSuffix(href, ".tar.gz") + ".json"
	wantHash := ""
	if rc, err := c.get(metaPath); err == nil {
		b, _ := io.ReadAll(rc)
		rc.Close()
		var meta struct {
			Sha256 string `json:"sha256"`
		}
		json.Unmarshal(b, &meta)
		wantHash = meta.Sha256
	}
	// 下载到临时文件。
	tmp, err := os.CreateTemp("", "lr-restore-*.tar.gz")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	rc, err := c.get(href)
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
		if strings.HasSuffix(e.Href, ".tar.gz") {
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
	// 规范化:补上远程目录前缀。
	href := snap
	if !strings.Contains(snap, "/") {
		href = remoteDirFor(j) + "/" + snap
	}
	return m.restoreSnapshot(j, c, href)
}
