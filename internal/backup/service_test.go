package backup

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// 测试只碰闸门,不碰 DB:构造 Manager 时不调 New(那会读库)。
func newTestManager(jobs ...*Job) *Manager {
	m := &Manager{jobs: map[string]*Job{}, stop: make(chan struct{})}
	for _, j := range jobs {
		if j.cancel == nil {
			j.cancel = make(chan struct{})
		}
		m.jobs[j.ID] = j
	}
	return m
}

func TestBackupRestoreGate(t *testing.T) {
	restoring := &Job{JobConfig: JobConfig{ID: "b1"}}
	restoring.Restoring = true
	running := &Job{JobConfig: JobConfig{ID: "b2"}}
	running.Running = true
	m := newTestManager(restoring, running)

	cases := []struct {
		name string
		err  error
		want error
	}{
		// 恢复中途备份会打包出半新半旧的树,备份中途恢复会让两份内容互相覆盖,互相都得挡。
		{"恢复中不许备份", m.RunBackup("b1", false), ErrBusy},
		{"备份中不许再备份", m.RunBackup("b2", false), ErrBusy},
		{"恢复中不许再恢复", m.StartRestore("b1", ""), ErrBusy},
		{"备份中不许恢复", m.StartRestore("b2", ""), ErrBusy},
		{"备份不存在的任务", m.RunBackup("nope", false), ErrNoJob},
		{"恢复不存在的任务", m.StartRestore("nope", ""), ErrNoJob},
	}
	for _, c := range cases {
		if !errors.Is(c.err, c.want) {
			t.Errorf("%s: err = %v, 期望 %v", c.name, c.err, c.want)
		}
	}
}

// StartRestore 必须立刻返回,并且"恢复中"这个状态在起头时就置上 ——
// 状态晚置一步,界面上的按钮就会在这段空档里再被点一次。
func TestStartRestoreAsyncAndGated(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // 把 PROPFIND 卡住,模拟一个还要跑一阵的恢复
		w.WriteHeader(http.StatusMultiStatus)
	}))
	defer srv.Close()

	j := &Job{JobConfig: JobConfig{ID: "b1", Name: "t", WebDAVURL: srv.URL, SourceDir: t.TempDir()}}
	m := newTestManager(j)

	start := time.Now()
	if err := m.StartRestore("b1", ""); err != nil {
		t.Fatalf("StartRestore 该接受: %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("StartRestore 该立刻返回,却花了 %v", d)
	}

	j.mu.Lock()
	restoring := j.Restoring
	j.mu.Unlock()
	if !restoring {
		t.Error("起头时就该把 Restoring 置上")
	}
	if err := m.StartRestore("b1", ""); !errors.Is(err, ErrBusy) {
		t.Errorf("恢复期间再点恢复该被挡,err = %v", err)
	}
	if err := m.RunBackup("b1", false); !errors.Is(err, ErrBusy) {
		t.Errorf("恢复期间该挡住备份,err = %v", err)
	}

	close(release)
	m.wg.Wait()

	j.mu.Lock()
	restoring, rerr := j.Restoring, j.RestoreErr
	j.mu.Unlock()
	if restoring {
		t.Error("跑完必须把 Restoring 落下,否则按钮一直灰着")
	}
	if rerr == "" {
		t.Error("这次恢复注定失败(没有快照),必须留下 restoreErr")
	}
}
