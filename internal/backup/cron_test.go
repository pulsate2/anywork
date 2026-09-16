package backup

import (
	"errors"
	"testing"
	"time"
)

func TestParseCronValid(t *testing.T) {
	valid := []string{
		"0 2 * * *",
		"*/15 * * * *",
		"0 3 * * 1-5",
		"0 0 1,15 * *",
		"30 4 1 * 0",
		" 0 2 * * * ", // 首尾空白由 Save 裁剪,parseCron 自己也该扛得住
	}
	for _, expr := range valid {
		if _, err := parseCron(expr); err != nil {
			t.Errorf("parseCron(%q) 应合法,却报错: %v", expr, err)
		}
	}
}

func TestParseCronInvalid(t *testing.T) {
	invalid := []string{
		"",             // 0 段
		"0 2 * *",      // 4 段
		"0 2 * * * *",  // 6 段
		"60 * * * *",   // 分越界
		"0 25 * * *",   // 时越界
		"0 2 * * 7",    // 周越界(0-6)
		"5-1 * * * *",  // 倒序范围:以前会静默变成"全部"
		"*/0 * * * *",  // 步长非正
		"*/-2 * * * *", // 步长负数
		"MON * * * *",  // 不支持名字
		"1,,2 * * * *", // 空取值
		"1- * * * *",   // 残缺范围
		"0 2 * 13 *",   // 月越界
	}
	for _, expr := range invalid {
		_, err := parseCron(expr)
		if err == nil {
			t.Errorf("parseCron(%q) 应报错,却通过了", expr)
			continue
		}
		// handler 靠 errors.Is 映射 400,哨兵必须一直在链上。
		if !errors.Is(err, ErrBadCron) {
			t.Errorf("parseCron(%q) 的错误未包装 ErrBadCron: %v", expr, err)
		}
	}
}

// 倒序范围曾静默产出一份"匹配全部"的 spec:展开循环一次都不走,out 保持 nil,
// 而 nil 的语义就是全部 —— 于是 5-1 变成每分钟都跑。
func TestParseCronReversedRangeNotAll(t *testing.T) {
	if _, err := parseCron("5-1 * * * *"); err == nil {
		t.Fatal("倒序范围 5-1 必须报错,不能再静默当全部")
	}
}

func TestCronNext(t *testing.T) {
	spec, err := parseCron("0 2 * * *")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	base := time.Date(2026, 9, 16, 10, 30, 0, 0, time.Local)
	got := spec.next(base)
	want := time.Date(2026, 9, 17, 2, 0, 0, 0, time.Local)
	if !got.Equal(want) {
		t.Errorf("next = %v, 期望 %v", got, want)
	}
	// 刚好卡在触发点上:next 取"之后",不该再命中同一分钟。
	onPoint := time.Date(2026, 9, 17, 2, 0, 0, 0, time.Local)
	if got := spec.next(onPoint); !got.Equal(time.Date(2026, 9, 18, 2, 0, 0, 0, time.Local)) {
		t.Errorf("整点触发后 next = %v, 期望次日 02:00", got)
	}
}

func TestCronEveryFifteen(t *testing.T) {
	spec, err := parseCron("*/15 * * * *")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	base := time.Date(2026, 9, 16, 10, 7, 0, 0, time.Local)
	want := time.Date(2026, 9, 16, 10, 15, 0, 0, time.Local)
	if got := spec.next(base); !got.Equal(want) {
		t.Errorf("next = %v, 期望 %v", got, want)
	}
}
