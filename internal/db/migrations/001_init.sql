-- 001_init.sql —— 里程碑 1 骨架表。
-- 方言中立:主键一律 TEXT(应用生成 id),布尔用 INTEGER(0/1),时间用 ISO-8601 TEXT。
-- 后续里程碑按版本号追加新文件,勿改已发布的迁移。

CREATE TABLE settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

CREATE TABLE workspaces (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL,
  path       TEXT NOT NULL UNIQUE,
  favorite   INTEGER NOT NULL DEFAULT 0,
  sort_order INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL
);

-- AI 配置档案:env 注入 + 各自的 CLAUDE_CONFIG_DIR / CODEX_HOME。
CREATE TABLE ai_profiles (
  id                TEXT PRIMARY KEY,
  name              TEXT NOT NULL UNIQUE,
  env_json          TEXT NOT NULL DEFAULT '{}',
  claude_config_dir TEXT,
  codex_home        TEXT,
  created_at        TEXT NOT NULL,
  updated_at        TEXT NOT NULL
);

-- 备份任务只存配置;快照本体在 WebDAV,不建表(见 DESIGN 4.5)。
CREATE TABLE backup_jobs (
  id              TEXT PRIMARY KEY,
  name            TEXT NOT NULL,
  source_dir      TEXT NOT NULL,
  webdav_url      TEXT NOT NULL,
  webdav_user     TEXT,
  webdav_password TEXT,
  schedule_cron   TEXT,
  excludes_json   TEXT NOT NULL DEFAULT '[]',
  retention       INTEGER NOT NULL DEFAULT 7,
  auto_restore    INTEGER NOT NULL DEFAULT 0,
  enabled         INTEGER NOT NULL DEFAULT 1,
  created_at      TEXT NOT NULL
);
