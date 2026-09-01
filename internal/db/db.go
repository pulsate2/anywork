// Package db 提供统一的数据库访问层:默认 SQLite(纯 Go 无 CGO),
// 设 DATABASE_URL 时切换远程 PostgreSQL。两个后端共用同一套 SQL 与迁移。
package db

import (
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	// 驱动注册:database/sql 需要显式 import 驱动包。
	_ "github.com/jackc/pgx/v5/stdlib" // driver 名 "pgx"
	_ "modernc.org/sqlite"             // driver 名 "sqlite",纯 Go 无 CGO
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type DB struct {
	*sql.DB
}

// Open 按配置打开 SQLite 或 PostgreSQL 并执行迁移。
func Open(dataDir, databaseURL string) (*DB, error) {
	var (
		driver string
		dsn    string
	)

	if databaseURL != "" {
		driver = "pgx"
		dsn = databaseURL
		if err := pingWithDriver(driver, dsn); err != nil {
			return nil, fmt.Errorf("连接 PostgreSQL 失败: %w", err)
		}
	} else {
		driver = "sqlite"
		var err error
		if dsn, err = sqliteDSN(filepath.Join(dataDir, "lightremote.db")); err != nil {
			return nil, err
		}
	}

	sqlDB, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, err
	}
	// SQLite 单进程一写多读,WAL 模式低内存;PG 连接数保持最小。
	sqlDB.SetMaxOpenConns(4)
	sqlDB.SetMaxIdleConns(2)

	if err := migrate(sqlDB); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("数据库迁移失败: %w", err)
	}
	return &DB{DB: sqlDB}, nil
}

func pingWithDriver(driver, dsn string) error {
	sqlDB, err := sql.Open(driver, dsn)
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	sqlDB.SetMaxOpenConns(1)
	return sqlDB.Ping()
}

func sqliteDSN(path string) (string, error) {
	if err := ensureDir(filepath.Dir(path)); err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)",
		path,
	), nil
}

func ensureDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("创建数据目录失败 %s: %w", dir, err)
	}
	return nil
}

// migrate 按文件名顺序执行 embed 迁移,记录已应用的版本。
func migrate(sqlDB *sql.DB) error {
	if _, err := sqlDB.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    TEXT PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return err
	}

	applied := map[string]bool{}
	rows, err := sqlDB.Query(`SELECT version FROM schema_migrations`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return err
		}
		applied[v] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		if applied[name] {
			continue
		}
		body, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		// 每条迁移在事务里执行:PG 的 DDL 也支持事务,双后端安全。
		tx, err := sqlDB.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("%s: %w", name, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations(version, applied_at) VALUES(?, ?)`, name, time.Now().UTC().Format(time.RFC3339)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// IsUniqueViolation 判断错误是否为唯一约束冲突(SQLite 与 PG 通用提示)。
func IsUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "unique constraint") || strings.Contains(s, "constraint failed")
}
