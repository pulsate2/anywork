package terminal

import (
	"encoding/json"
	"testing"
)

// TestCreateMsgLimits 前端把上限放在 create 帧的顶层字段里,别写错名字。
func TestCreateMsgLimits(t *testing.T) {
	var in inMsg
	raw := `{"type":"create","dir":"/tmp","shell":"","cols":80,"rows":24,"memoryMB":256,"cpuPercent":30}`
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		t.Fatalf("解析 create 帧: %v", err)
	}
	if in.MemoryMB != 256 || in.CPUPercent != 30 {
		t.Errorf("上限没解出来: %+v", in)
	}
}

// TestSessionFrameShape session 帧靠内联展开 Summary 得到与列表项一致的形状。
// 内联一旦写成普通字段,前端读到的就是 payload.summary.id 了,这里把形状钉住。
func TestSessionFrameShape(t *testing.T) {
	b, err := json.Marshal(sessMsg{Type: "session", Summary: Summary{
		ID: "abc", Dir: "/tmp", Cols: 80, Rows: 24,
		MemoryMB: 256, CPUPercent: 30, LimitMode: "cgroup2",
	}})
	if err != nil {
		t.Fatalf("序列化: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("反序列化: %v", err)
	}
	for k, want := range map[string]any{
		"type": "session", "id": "abc", "dir": "/tmp",
		"memoryMB": 256.0, "cpuPercent": 30.0, "limitMode": "cgroup2",
	} {
		if got[k] != want {
			t.Errorf("%s = %v(%T),期望 %v", k, got[k], got[k], want)
		}
	}
	// 没限制的会话不该冒出 memoryMB:0 这种噪音(前端据此判断要不要显示标签)。
	b, _ = json.Marshal(sessMsg{Type: "session", Summary: Summary{ID: "x"}})
	got = nil
	_ = json.Unmarshal(b, &got)
	for _, k := range []string{"memoryMB", "cpuPercent", "limitMode"} {
		if _, ok := got[k]; ok {
			t.Errorf("零值限额不该出现在帧里: %s", k)
		}
	}
}

// TestEmptyListSerializesToArray 结束最后一个会话后 list 必须是 [] 而不是 null。
func TestEmptyListSerializesToArray(t *testing.T) {
	m := NewManager(t.TempDir(), false)
	b, err := json.Marshal(sessMsg{Type: "sessionList", List: m.List()})
	if err != nil {
		t.Fatalf("序列化: %v", err)
	}
	var got struct {
		List []Summary `json:"list"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("反序列化: %v", err)
	}
	if got.List == nil {
		t.Errorf("空会话列表序列化成了 null: %s", b)
	}
}
