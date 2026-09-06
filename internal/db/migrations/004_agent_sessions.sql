-- 004_agent_sessions.sql —— Agent 会话远控(DESIGN-AGENT.md)。
-- 会话与消息分离:进程态只在内存,DB 只存记录;消息以 (session_id, seq) 唯一,
-- seq 是会话内单调递增的回放游标(REST afterSeq 增量拉取 + WS 推送补差共用)。

CREATE TABLE agent_sessions (
  id              TEXT PRIMARY KEY,
  app             TEXT NOT NULL,              -- claude | codex
  workspace       TEXT NOT NULL,              -- cwd 绝对路径(正斜杠形式,与 workspaces.path 一致)
  title           TEXT,                       -- 首条用户消息截断
  external_id     TEXT,                       -- claude session_id / codex thread id,续聊用
  permission_mode TEXT NOT NULL DEFAULT 'ask',
  status          TEXT NOT NULL,              -- running | idle | dead
  created_at      TEXT NOT NULL,
  updated_at      TEXT NOT NULL
);

CREATE INDEX idx_agent_sessions_updated ON agent_sessions(updated_at DESC);

CREATE TABLE agent_messages (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id  TEXT NOT NULL,
  seq         INTEGER NOT NULL,
  kind        TEXT NOT NULL,                  -- user | assistant_text | reasoning | tool_call | permission_request | permission_result | status | error
  payload     TEXT NOT NULL,                  -- JSON,落库前已截断
  created_at  TEXT NOT NULL,
  UNIQUE(session_id, seq)
);

CREATE INDEX idx_agent_messages_session ON agent_messages(session_id, seq);
