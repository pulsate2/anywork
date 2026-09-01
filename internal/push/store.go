package push

import (
	"database/sql"
	"fmt"
)

// Store 提供订阅的数据库访问。? 占位符对 SQLite 与 pgx 均可用。
type Store struct {
	db *sql.DB
}

// NewStore 构造订阅存储。
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// Upsert 插入或更新(endpoint 冲突时刷新密钥与 last_seen)。
// 返回是否为新插入。
func (s *Store) Upsert(sub Subscription) (bool, error) {
	now := now()
	res, err := s.db.Exec(`INSERT INTO push_subscriptions(id, endpoint, p256dh, auth, created_at, last_seen)
		VALUES(?,?,?,?,?,?)
		ON CONFLICT(endpoint) DO UPDATE SET
			p256dh=excluded.p256dh, auth=excluded.auth, last_seen=excluded.last_seen`,
		sub.ID, sub.Endpoint, sub.P256DH, sub.Auth, now, now)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// List 返回全部订阅。
func (s *Store) List() ([]Subscription, error) {
	rows, err := s.db.Query(`SELECT id, endpoint, p256dh, auth, created_at, last_seen FROM push_subscriptions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Subscription{}
	for rows.Next() {
		var sub Subscription
		if err := rows.Scan(&sub.ID, &sub.Endpoint, &sub.P256DH, &sub.Auth, &sub.CreatedAt, &sub.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

// Count 返回订阅数量。
func (s *Store) Count() (int, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM push_subscriptions`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// DeleteByEndpoint 按端点删除订阅,返回删除行数。
func (s *Store) DeleteByEndpoint(ep string) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM push_subscriptions WHERE endpoint = ?`, ep)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteAll 清空全部订阅(手动关闭时用),返回删除行数。
func (s *Store) DeleteAll() (int64, error) {
	res, err := s.db.Exec(`DELETE FROM push_subscriptions`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ErrNotFound 供 handler 判断端点不存在。
var ErrNotFound = fmt.Errorf("subscription not found")
