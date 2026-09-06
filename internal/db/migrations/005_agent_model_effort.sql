-- 模型与思考强度:创建时指定,会话内可调(codex 下一回合生效;claude 落库、续聊生效)。
-- model 同时是"当前实际模型"的展示来源:claude 从 init 消息、codex 从 thread/start 响应回填。
ALTER TABLE agent_sessions ADD COLUMN model TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_sessions ADD COLUMN effort TEXT NOT NULL DEFAULT ''; -- none | low | medium | high(空 = 默认)
