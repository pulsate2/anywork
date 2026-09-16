// Package backup 实现 WebDAV 目录备份(里程碑 6)。
package backup

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ErrBadCron 定时表达式不合法。带哨兵是为了让 handler 能映射成 400 而不是 500。
var ErrBadCron = errors.New("cron 表达式无效")

// cronSpec 解析后的 5 段 cron:分 时 日 月 周。
type cronSpec struct {
	minute, hour, dom, month, dow []int
}

var cronFieldNames = []string{"分", "时", "日", "月", "周"}

// parseCron 解析 5 段 cron 表达式(标准 crontab 语法)。
func parseCron(expr string) (*cronSpec, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil, fmt.Errorf("%w:需要 5 段(分 时 日 月 周),当前 %d 段", ErrBadCron, len(fields))
	}
	s := &cronSpec{}
	maxes := []int{59, 23, 31, 12, 6}

	for i := 0; i < 5; i++ {
		v, e := parseField(fields[i], maxes[i])
		if e != nil {
			return nil, fmt.Errorf("%w:%s %v", ErrBadCron, cronFieldNames[i], e)
		}
		switch i {
		case 0:
			s.minute = v
		case 1:
			s.hour = v
		case 2:
			s.dom = v
		case 3:
			s.month = v
		case 4:
			s.dow = v
		}
	}
	return s, nil
}

// parseField 解析单个字段:* | */n | a-b | a,b,c | 数值(可组合)。max 为字段上限。
// 越界在这里就判掉:否则越界值会一路溜到 match() 里静默不命中。
func parseField(f string, max int) ([]int, error) {
	f = strings.TrimSpace(f)
	if f == "*" {
		return nil, nil // nil = 全部
	}
	// 步长 */n:展开为 0,n,2n,... ≤ max。
	if strings.HasPrefix(f, "*/") {
		n, err := strconv.Atoi(strings.TrimPrefix(f, "*/"))
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("步长得是正整数: %q", f)
		}
		out := []int{}
		for i := 0; i <= max; i += n {
			out = append(out, i)
		}
		return out, nil
	}
	var out []int
	for _, part := range strings.Split(f, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("有空的取值: %q", f)
		}
		if i := strings.Index(part, "-"); i >= 0 {
			lo, e1 := strconv.Atoi(part[:i])
			hi, e2 := strconv.Atoi(part[i+1:])
			if e1 != nil || e2 != nil {
				return nil, fmt.Errorf("范围得写成 数-数: %q", part)
			}
			// 倒序范围必须报错:展开循环一次都不走,out 保持 nil,
			// 而 nil 的语义是"全部" —— 5-1 会静默变成每分钟都跑。
			if lo > hi {
				return nil, fmt.Errorf("范围左端大于右端: %q", part)
			}
			if lo < 0 || hi > max {
				return nil, fmt.Errorf("超出 0-%d: %q", max, part)
			}
			for i := lo; i <= hi; i++ {
				out = append(out, i)
			}
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("无法解析: %q", part)
		}
		if n < 0 || n > max {
			return nil, fmt.Errorf("超出 0-%d: %q", max, part)
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("没有可用取值: %q", f)
	}
	return out, nil
}

// match 判断给定时间是否命中。
func (c *cronSpec) match(t time.Time) bool {
	if !containsOrAll(c.minute, t.Minute()) {
		return false
	}
	if !containsOrAll(c.hour, t.Hour()) {
		return false
	}
	if !containsOrAll(c.dom, t.Day()) {
		return false
	}
	if !containsOrAll(c.month, int(t.Month())) {
		return false
	}
	if !containsOrAll(c.dow, int(t.Weekday())) {
		return false
	}
	return true
}

func containsOrAll(list []int, v int) bool {
	if list == nil {
		return true
	}
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// next 计算下一次触发时间(从 t 之后的整分钟开始扫描)。
func (c *cronSpec) next(after time.Time) time.Time {
	t := after.Truncate(time.Minute).Add(time.Minute)
	for i := 0; i < 366*24*60; i++ { // 最多一年
		if c.match(t) {
			return t
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}
}
