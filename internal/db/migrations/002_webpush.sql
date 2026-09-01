-- 002_webpush.sql —— 里程碑 7 Web Push 订阅存储。
-- 方言中立:主键 TEXT,布尔 INTEGER(0/1),时间 ISO-8601 TEXT。
CREATE TABLE push_subscriptions (
  id         TEXT PRIMARY KEY,
  endpoint   TEXT NOT NULL UNIQUE,
  p256dh     TEXT NOT NULL,
  auth       TEXT NOT NULL,
  created_at TEXT NOT NULL,
  last_seen  TEXT NOT NULL
);
