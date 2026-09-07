package fs

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// 用 modernc 驱动建一个真库再走 Service 的预览路径:概览、行数据、
// WITHOUT ROWID 表、BLOB/NULL 单元格、分页都各有一条用例。
func createTestDB(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	// 预览端点只读;建库用一条临时可写连接。
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open writable: %v", err)
	}
	defer db.Close()
	stmts := []string{
		`CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT, age INT, data BLOB)`,
		`INSERT INTO users VALUES (1, 'alice', 30, NULL)`,
		`INSERT INTO users VALUES (2, 'bob', 25, x'00FF10')`,
		`INSERT INTO users VALUES (3, '中文', 41, 'plain-text')`,
		`CREATE TABLE kv (k TEXT PRIMARY KEY, v TEXT) WITHOUT ROWID`,
		`INSERT INTO kv VALUES ('a', '1')`,
		`INSERT INTO kv VALUES ('b', '2')`,
		`PRAGMA user_version = 7`,
	}
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}
	return dbPath
}

func TestSqliteInfo(t *testing.T) {
	dbPath := createTestDB(t)
	s := NewService(filepath.Dir(dbPath), false)
	tables, version, err := s.SqliteInfo(dbPath)
	if err != nil {
		t.Fatalf("SqliteInfo: %v", err)
	}
	if version != 7 {
		t.Errorf("user_version = %d, want 7", version)
	}
	if len(tables) != 2 {
		t.Fatalf("tables = %+v, want 2 entries", tables)
	}
	if tables[0].Name != "kv" || tables[0].Count != 2 {
		t.Errorf("kv = %+v, want count 2", tables[0])
	}
	if tables[1].Name != "users" || tables[1].Count != 3 {
		t.Errorf("users = %+v, want count 3", tables[1])
	}
}

func TestSqliteRows(t *testing.T) {
	dbPath := createTestDB(t)
	s := NewService(filepath.Dir(dbPath), false)

	data, err := s.SqliteRows(dbPath, SqliteRowsOptions{Table: "users", Limit: 2, Offset: 1})
	if err != nil {
		t.Fatalf("SqliteRows: %v", err)
	}
	if data.Total != 3 {
		t.Errorf("Total = %d, want 3", data.Total)
	}
	if len(data.Columns) != 4 || data.Columns[0].Name != "id" || data.Columns[0].Type != "INTEGER" {
		t.Errorf("columns = %+v", data.Columns)
	}
	if len(data.Rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(data.Rows))
	}
	// offset=1 起的两行:bob(BLOB) 与 中文([]byte 文本)。
	if data.Rows[0][1] != "bob" {
		t.Errorf("row0 name = %v, want bob", data.Rows[0][1])
	}
	blob, ok := data.Rows[0][3].(string)
	if !ok || blob[:5] != "blob:" {
		t.Errorf("row0 data = %v, want blob: 前缀", data.Rows[0][3])
	}
	if data.Rows[1][1] != "中文" {
		t.Errorf("row1 name = %v, want 中文", data.Rows[1][1])
	}
	if data.Rows[1][3] != "plain-text" {
		t.Errorf("row1 data = %v, want plain-text(TEXT 列扫成 []byte 应还原成文本)", data.Rows[1][3])
	}
}

func TestSqliteRowsWithoutRowid(t *testing.T) {
	dbPath := createTestDB(t)
	s := NewService(filepath.Dir(dbPath), false)
	data, err := s.SqliteRows(dbPath, SqliteRowsOptions{Table: "kv"})
	if err != nil {
		t.Fatalf("SqliteRows(kv): %v", err)
	}
	if len(data.Rows) != 2 {
		t.Fatalf("rows = %d, want 2(WITHOUT ROWID 表 max(rowid) 不可用,须 COUNT 兜底)", len(data.Rows))
	}
}

func TestSqliteRowsBadTable(t *testing.T) {
	dbPath := createTestDB(t)
	s := NewService(filepath.Dir(dbPath), false)
	if _, err := s.SqliteRows(dbPath, SqliteRowsOptions{Table: "nope"}); err == nil {
		t.Fatal("nope 表应报错")
	}
	if _, err := s.SqliteRows(dbPath, SqliteRowsOptions{}); err == nil {
		t.Fatal("空表名应报错")
	}
}
