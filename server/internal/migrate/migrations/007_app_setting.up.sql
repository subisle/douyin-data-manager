-- 轻量 KV 设置表：机器人开关（如每日 1 点索要 CSV 的推送开关）需要
-- 跨重启持久化，内存态一重启就丢。
CREATE TABLE IF NOT EXISTS app_setting (
  setting_key   VARCHAR(64) NOT NULL,
  setting_value TEXT NULL,
  updated_at    DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (setting_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
