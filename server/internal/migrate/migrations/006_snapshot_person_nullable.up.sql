-- 未建档主播的数据也要存：导入时匹配不到人（还没建档/绑号）的行，
-- person_id 先留空，照常写快照；之后一旦绑定账号，回填 person_id 并重算，
-- 历史数据自动出现在榜上。
ALTER TABLE wave_snapshot
  MODIFY COLUMN person_id BIGINT UNSIGNED NULL COMMENT 'NULL=导入时未建档，绑号后回填';

ALTER TABLE duration_snapshot
  MODIFY COLUMN person_id BIGINT UNSIGNED NULL COMMENT 'NULL=导入时未建档，绑号后回填';
