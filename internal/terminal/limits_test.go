package terminal

import "testing"

func TestLimitsClamp(t *testing.T) {
	cases := []struct {
		name string
		in   Limits
		want Limits
	}{
		{"零值原样通过", Limits{}, Limits{}},
		{"负数当没填", Limits{MemoryMB: -1, CPUPercent: -5}, Limits{}},
		{"内存太小抬到下界", Limits{MemoryMB: 1}, Limits{MemoryMB: minLimitMemoryMB}},
		{"内存上界挡手滑", Limits{MemoryMB: maxLimitMemoryMB * 4}, Limits{MemoryMB: maxLimitMemoryMB}},
		{"CPU 超 100 收到 100", Limits{CPUPercent: 400}, Limits{CPUPercent: 100}},
		{"正常值不动", Limits{MemoryMB: 512, CPUPercent: 30}, Limits{MemoryMB: 512, CPUPercent: 30}},
	}
	for _, c := range cases {
		if got := c.in.clamp(); got != c.want {
			t.Errorf("%s: clamp(%+v) = %+v, 期望 %+v", c.name, c.in, got, c.want)
		}
	}
}

func TestLimitsIsZero(t *testing.T) {
	if !(Limits{}).isZero() {
		t.Error("零值应判为不限制")
	}
	if (Limits{CPUPercent: 1}).isZero() {
		t.Error("只填 CPU 也算有限制")
	}
}
