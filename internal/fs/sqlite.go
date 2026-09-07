package fs

import (
	"database/sql"
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite" // driver:纯 Go 无 CGO,预览用它只读打开
)

// sqliteQuery 只读单条 SELECT:所有 sqlite 入口共用。
// 任何不是"一个只读 SELECT"的语句都会被驱动直接拒绝,注入在这里构造不出来。
func sqliteQuery(abs, query string) (queryResult, error) {
	// 语义:预览绝不写库。
	// immutable 额外跳过 WAL/journal 侧车文件 —— 并发写着的库照样能读,
	// 代价是可能读到稍旧的快照,对预览完全可以接受。
	db, err := sql.Open("sqlite", "file:"+url.PathEscape(abs)+"?mode=ro&immutable=1")
	if err != nil {
		return queryResult{}, err
	}
	defer db.Close()
	rows, err := db.Query(query)
	if err != nil {
		db.Close()
		return queryResult{}, err
	}
	// rows 断开后 db 才能关:这里把关闭 db 的义务连同 rows 一起交给调用方。
	return queryResult{rows: rows, db: db}, nil
}

// queryResult:绑定在一起的一组 rows + 打开的库连接。
type queryResult struct {
	rows *sql.Rows
	db   *sql.DB
}

func (q queryResult) Rows() *sql.Rows { return q.rows }

// Close 关掉 rows 并连同库连接一起释放。
func (q queryResult) Close() error {
	err := q.rows.Close()
	if cerr := q.db.Close(); err == nil {
		err = cerr
	}
	return err
}

// Err 透传 rows.Err()。
func (q queryResult) Err() error { return q.rows.Err() }

// Next/Scan/Columns/NCol 是调用处用得到的 rows 便捷方法。
func (q queryResult) Next() bool  { return q.rows.Next() }
func (q queryResult) Scan(dest ...any) error { return q.rows.Scan(dest...) }
func (q queryResult) Columns() ([]string, error) { return q.rows.Columns() }

func (q queryResult) NCol() int {
	names, err := q.rows.Columns()
	if err != nil {
		return 0
	}
	return len(names)
}

// quoteIdent 表名/列名按 identifier 引号规则转义(SQLite 语法:"a""b")。
// 表名来自 schema 查询而非用户输入,但稳妥起见仍统一走它。
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// SqliteTable 概览里的单表信息。
type SqliteTable struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

// SqliteColumn 行查询返回的列定义。
type SqliteColumn struct {
	Name string `json:"name"`
	// 映射自 table_info 的 type,由建表语句怎么写决定,缺省为空。
	Type string `json:"type,omitempty"`
}

// SqliteInfo 数据库概览:所有用户表 + 每表行数 + user_version(application id 常用它)。
// 大库数行数可能要全表扫,超时 5s 后放弃,该表 count 记 -1(前端显示"未知")。
func (s *Service) SqliteInfo(p string) ([]SqliteTable, int64, error) {
	abs, err := s.Resolve(p)
	if err != nil {
		return nil, 0, err
	}
	rows, err := sqliteQuery(abs, `SELECT name FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, 0, err
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	tables := make([]SqliteTable, 0, len(names))
	for _, name := range names {
		count, err := sqliteCount(abs, name)
		if err != nil {
			count = -1
		}
		tables = append(tables, SqliteTable{Name: name, Count: count})
	}
	version, _ := sqliteScalarInt(abs, `PRAGMA user_version`)
	return tables, version, nil
}

// sqliteCount 数单表行数。空表走 max(rowid) 是 O(1);数不出来(空表,或
// WITHOUT ROWID 表根本没有 rowid 列、max 直接报错)就退回 COUNT(*)。
// 大表 COUNT(*) 全扫,超时即放弃。
func sqliteCount(abs, table string) (int64, error) {
	if n, err := sqliteScalarInt(abs, `SELECT max(rowid) FROM `+quoteIdent(table)); err == nil && n > 0 {
		return n, nil
	}
	return sqliteScalarIntTimeout(abs, `SELECT count(*) FROM `+quoteIdent(table), 5)
}

// sqliteScalarInt 取单行单列整数值的便捷封装。
func sqliteScalarInt(abs, query string) (int64, error) {
	rows, err := sqliteQuery(abs, query)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	if !rows.Next() {
		return 0, fmt.Errorf("no row: %s", query)
	}
	var n sql.NullInt64
	if err := rows.Scan(&n); err != nil {
		return 0, err
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return n.Int64, nil
}

// sqliteScalarIntTimeout 带超时的单值整数查询,超时返回错误。
func sqliteScalarIntTimeout(abs, query string, seconds int) (int64, error) {
	done := make(chan scalarResult, 1)
	go func() {
		n, err := sqliteScalarInt(abs, query)
		done <- scalarResult{n: n, err: err}
	}()
	select {
	case r := <-done:
		return r.n, r.err
	case <-time.After(time.Duration(seconds) * time.Second):
		// 查询 goroutine 仍在后台跑完自己关资源;调用方拿错误走"未知"。
		go func() { <-done }()
		return 0, fmt.Errorf("count timeout after %ds", seconds)
	}
}

type scalarResult struct {
	n   int64
	err error
}

// SqliteRows 某表的行数据:列定义 + 行(单元格以"尽量可读的文本"给出)。
type SqliteRows struct {
	Columns []SqliteColumn `json:"columns"`
	Rows    [][]any        `json:"rows"`
	Total   int64          `json:"total"`
}

// SqliteRowsLimit 单页行数上限,与前端分页大小保持一致。
const SqliteRowsLimit = 200

// SqliteRowsOptions 分页参数。Table 是表名。
type SqliteRowsOptions struct {
	Table  string
	Offset int
	Limit  int
}

// SqliteRows 取一页行(升序 offset 翻页)。limit 封顶 SqliteRowsLimit;
// total 复用 SqliteInfo 的行数逻辑(拿不到为 -1),前端据此算总页数。
func (s *Service) SqliteRows(p string, opt SqliteRowsOptions) (*SqliteRows, error) {
	abs, err := s.Resolve(p)
	if err != nil {
		return nil, err
	}
	if opt.Table == "" {
		return nil, ErrBadQuery
	}
	if opt.Limit <= 0 || opt.Limit > SqliteRowsLimit {
		opt.Limit = SqliteRowsLimit
	}
	if opt.Offset < 0 {
		opt.Offset = 0
	}

	// 列定义。
	crows, err := sqliteQuery(abs, `SELECT name, type FROM pragma_table_info(`+quoteIdent(opt.Table)+`)`)
	if err != nil {
		return nil, err
	}
	defer crows.Close()
	var cols []SqliteColumn
	for crows.Next() {
		var name, typ sql.NullString
		if err := crows.Scan(&name, &typ); err != nil {
			return nil, err
		}
		cols = append(cols, SqliteColumn{Name: name.String, Type: typ.String})
	}
	if err := crows.Err(); err != nil {
		return nil, err
	}

	// 行数据:rowid 升序,没有 rowid 的表(WITHOUT ROWID)由驱动回落主键序。
	q := fmt.Sprintf(`SELECT * FROM %s LIMIT %d OFFSET %d`, quoteIdent(opt.Table), opt.Limit, opt.Offset)
	drows, err := sqliteQuery(abs, q)
	if err != nil {
		return nil, err
	}
	defer drows.Close()

	// total 与行数据分开:COUNT 在另一处可能超时/失败,不该拖垮本页数据。
	var total int64 = -1
	if n, err := sqliteCount(abs, opt.Table); err == nil {
		total = n
	}

	data := &SqliteRows{Columns: cols, Total: total}
	ncol := len(cols)
	if ncol == 0 {
		// 列定义拿不到(理论上不会):退回驱动报的列。
		data.Columns = make([]SqliteColumn, drows.NCol())
		for i := range data.Columns {
			data.Columns[i].Name = fmt.Sprintf("c%d", i+1)
		}
		ncol = len(data.Columns)
	}
	vals := make([]any, ncol)
	ptrs := make([]any, ncol)
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for drows.Next() {
		if err := drows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make([]any, ncol)
		for i, v := range vals {
			row[i] = sqliteCell(v)
		}
		data.Rows = append(data.Rows, row)
	}
	if err := drows.Err(); err != nil {
		return nil, err
	}
	return data, nil
}

// sqliteCell 把驱动扫出来的单元格转成前端友好的值:
// number/bool 原样;string 原样;[]byte 是 BLOB,转 base64;
// NULL -> nil。这样 JSON 端不用再猜类型。
func sqliteCell(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case int64, float64, bool, string:
		return t
	case []byte:
		// 老驱动/TEXT 列也可能扫成 []byte;是合法 UTF-8 就按文本给出。
		if utf8.Valid(t) {
			return string(t)
		}
		return "blob:" + base64.StdEncoding.EncodeToString(t)
	default:
		return fmt.Sprintf("%v", t)
	}
}
