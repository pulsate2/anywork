-- 备份指纹:上次成功备份时源目录的内容指纹,未变化则跳过。
ALTER TABLE backup_jobs ADD COLUMN last_fp TEXT NOT NULL DEFAULT '';
