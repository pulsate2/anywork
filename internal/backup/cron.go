// Package backup 实现 WebDAV 目录备份(里程碑 6)。
package backup

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// cronSpec 解析后的 5 段 cron:分 时 日 月 周。
type cronSpec struct {
	minute, hour, dom, month, dow []int
}

// parseCron 解析 5 段 cron 表达式(标准 crontab 语法)。
func parseCron(expr string) (*cronSpec, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil, errors.New("cron 需 5 段:分 时 日 月 周")
	}
	s := &cronSpec{}
	maxes := []int{59, 23, 31, 12, 6}

	for i := 0; i < 5; i++ {
		v, e := parseField(fields[i], maxes[i])
		if e != nil {
			return nil, e
		}
		for _, x := range v {
			if x < 0 || x > maxes[i] {
				return nil, fmt.Errorf("字段越界: %d 超出 0-%d", x, maxes[i])
			}
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
func parseField(f string, max int) ([]int, error) {
	f = strings.TrimSpace(f)
	if f == "*" {
		return nil, nil // nil = 全部
	}
	// 步长 */n:展开为 0,n,2n,... ≤ max。
	if strings.HasPrefix(f, "*/") {
		n, err := strconv.Atoi(strings.TrimPrefix(f, "*/"))
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("非法步长: %s", f)
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
		if strings.Contains(part, "-") {
			bounds := strings.SplitN(part, "-", 2)
			lo, e1 := strconv.Atoi(bounds[0])
			hi, e2 := strconv.Atoi(bounds[1])
			if e1 != nil || e2 != nil {
				return nil, fmt.Errorf("非法范围: %s", part)
			}
			for i := lo; i <= hi; i++ {
				out = append(out, i)
			}
		} else {
			n, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("非法数值: %s", part)
			}
			out = append(out, n)
		}
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
