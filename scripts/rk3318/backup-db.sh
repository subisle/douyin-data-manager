#!/bin/sh
# 盒子本地库 → sqlpub 远端备份（每日 cron 03:30）
# 只备份 Go 业务表；615 staging 表（persons/accounts/wave_snapshots/duration_snapshots/import_records）
# 的主档在远端（本地 Electron 直写远端），不能反向覆盖。
# 日志: /srv/douyin/backup-db.log
set -u
cd /srv/douyin/app

GO_TABLES="person account tier_rule import_batch wave_snapshot duration_snapshot daily_metric monthly_metric yearly_metric data_anomaly schema_migrations_server"

# 1) 盒子本地文件快照（保留最近 7 份）
TS=$(date +%Y%m%d-%H%M)
mkdir -p /srv/douyin/backups
docker compose --env-file deploy/rk3318/.env -f docker-compose.rk3318.yml exec -T db \
  sh -c "mysqldump -uroot -p\"\$MYSQL_ROOT_PASSWORD\" --single-transaction --set-gtid-purged=OFF --no-tablespaces douyinxs $GO_TABLES" \
  | gzip > "/srv/douyin/backups/douyinxs-$TS.sql.gz"
ls -1t /srv/douyin/backups/douyinxs-*.sql.gz | tail -n +8 | xargs -r rm -f

# 2) 推到 sqlpub 远端（DROP+CREATE+INSERT，整表替换）
docker compose --env-file deploy/rk3318/.env -f docker-compose.rk3318.yml exec -T db \
  sh -c "mysqldump -uroot -p\"\$MYSQL_ROOT_PASSWORD\" --single-transaction --set-gtid-purged=OFF --no-tablespaces douyinxs $GO_TABLES | mysql -h mysql7.sqlpub.com -P 3312 -u douyinxs -p'WABZfpfGGlPSxlrs' douyinxs"

RC=$?
echo "$(date '+%F %T') backup rc=$RC file=douyinxs-$TS.sql.gz"
exit $RC
