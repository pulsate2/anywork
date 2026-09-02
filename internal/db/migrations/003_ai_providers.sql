-- 003_ai_providers.sql —— AI 配置改版:从"注入环境变量 + 各档案独立配置目录"
-- 换成供应商配置切换(参考 cc-switch):一条记录存一份真实配置文件的内容,
-- 切换即把它写回 ~/.claude/settings.json、~/.codex/config.toml + auth.json。
-- 方言中立:主键 TEXT,布尔 INTEGER(0/1),时间 ISO-8601 TEXT。

-- 旧表建了但从没被代码用过(旧实现落在文件系统上),连同旧的 active 记录一起清掉。
DROP TABLE IF EXISTS ai_profiles;
DELETE FROM settings WHERE key = 'active_ai_profile';

CREATE TABLE ai_providers (
  id          TEXT PRIMARY KEY,
  app         TEXT NOT NULL,
  name        TEXT NOT NULL,
  category    TEXT NOT NULL DEFAULT 'custom',
  website_url TEXT NOT NULL DEFAULT '',
  config_json TEXT NOT NULL,
  is_current  INTEGER NOT NULL DEFAULT 0,
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);

CREATE UNIQUE INDEX ai_providers_app_name ON ai_providers(app, name);
