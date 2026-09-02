package aiprofile

import (
	"encoding/json"
	"errors"
	"io"
)

// dump 导出格式:按 app 分组的配置清单。当前生效项不导出 —— 那是本机状态,
// 换台机器上再决定切到哪份。
type dump struct {
	Version int                   `json:"version"`
	Apps    map[string][]Provider `json:"apps"`
}

// Export 把全部配置写成一份 JSON。
func (s *Service) Export(w io.Writer) error {
	d := dump{Version: 1, Apps: map[string][]Provider{}}
	for _, app := range []string{AppClaude, AppCodex} {
		list, err := s.List(app)
		if err != nil {
			return err
		}
		d.Apps[app] = list
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(d)
}

// Import 按名字合并导入的配置:同名覆盖,新名新增。返回落库的条数。
func (s *Service) Import(r io.Reader) (int, error) {
	var d dump
	// 4MB 够放几百份配置了,不设上限的话一个大文件就能把内存吃干。
	if err := json.NewDecoder(io.LimitReader(r, 4<<20)).Decode(&d); err != nil {
		return 0, invalid("不是合法的配置清单: %v", err)
	}
	n := 0
	for app, list := range d.Apps {
		if !appOK(app) {
			continue
		}
		for _, p := range list {
			p.App = app
			if err := s.upsertByName(p); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, nil
}

// upsertByName 走 Create/Update 而不是直接写库,校验和"改到当前生效那份就顺手
// 落盘"的逻辑都能复用。
func (s *Service) upsertByName(p Provider) error {
	p.Name = cleanName(p.Name)
	if p.Name == "" {
		return invalid("配置名不能为空")
	}
	old, err := s.byName(p.App, p.Name)
	switch {
	case err == nil:
		p.ID = old.ID
		_, err = s.Update(p)
		return err
	case errors.Is(err, ErrNotFound):
		p.ID = ""
		_, err = s.Create(p)
		return err
	}
	return err
}

func (s *Service) byName(app, name string) (Provider, error) {
	rows, err := s.db.Query(selectCols+` WHERE app = ? AND name = ?`, app, name)
	if err != nil {
		return Provider{}, err
	}
	defer rows.Close()
	list, err := scanProviders(rows)
	if err != nil {
		return Provider{}, err
	}
	if len(list) == 0 {
		return Provider{}, ErrNotFound
	}
	return list[0], nil
}
