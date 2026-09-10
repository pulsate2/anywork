-- 006_agent_queue.sql —— 排队消息服务端化。
-- 此前排队是前端内存态:回合进行中发出的消息只在浏览器里排队,页面退出
-- (关标签/切设备/断网重连)队列就没了。落到 DB 后由服务端在回合结束时
-- 放行,前端只负责展示与撤回;id 自增即入队顺序,放行取最小。

CREATE TABLE agent_queue (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id  TEXT NOT NULL,
  text        TEXT NOT NULL,
  created_at  TEXT NOT NULL
);

CREATE INDEX idx_agent_queue_session ON agent_queue(session_id, id);
