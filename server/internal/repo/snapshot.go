package repo

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"douyin-server/internal/domain"
)

// CreateBatch 开一个导入批次。所有快照写入都挂在这个批次上，
// 便于事后追溯"这一天的数是谁、什么时候导进来的"。
func (r *Repo) CreateBatch(ctx context.Context, bizDate time.Time, source, operator string) (uint64, error) {
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO import_batch (biz_date, source, status, operator) VALUES (?, ?, 'running', ?)`,
		bizDate, source, operator)
	if err != nil {
		return 0, fmt.Errorf("创建导入批次: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("读取批次 ID: %w", err)
	}
	return uint64(id), nil
}

// FinishBatch 关闭批次，成功或失败都要调用，否则批次会一直是 running。
func (r *Repo) FinishBatch(ctx context.Context, batchID uint64, rowCount int, errText string) error {
	status := "succeeded"
	if errText != "" {
		status = "failed"
	}
	if _, err := r.db.ExecContext(ctx,
		`UPDATE import_batch SET status = ?, row_count = ?, error_text = NULLIF(?, ''), finished_at = NOW(3)
		 WHERE id = ?`, status, rowCount, errText, batchID); err != nil {
		return fmt.Errorf("关闭导入批次: %w", err)
	}
	return nil
}

// UpsertWaveSnapshots 批量写入音浪快照。
// 同一 anchor_id + biz_date 重复导入会覆盖，这是刻意的：允许用新数据纠正旧导入。
func (r *Repo) UpsertWaveSnapshots(ctx context.Context, batchID uint64, rows []domain.WaveSnapshot) error {
	if len(rows) == 0 {
		return nil
	}
	const query = `INSERT INTO wave_snapshot (anchor_id, person_id, biz_date, wave_value, rank_in_guild, batch_id)
	               VALUES (?, ?, ?, ?, ?, ?)
	               ON DUPLICATE KEY UPDATE
	                 wave_value = VALUES(wave_value),
	                 rank_in_guild = VALUES(rank_in_guild),
	                 batch_id = VALUES(batch_id)`

	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开启事务: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, row := range rows {
		if _, err := tx.ExecContext(ctx, query,
			row.AnchorID, row.PersonID, row.BizDate, row.WaveValue, row.RankInGuild, batchID,
		); err != nil {
			return fmt.Errorf("写入音浪快照(%s): %w", row.AnchorID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交音浪快照: %w", err)
	}
	return nil
}

// UpsertDurationSnapshots 批量写入时长快照。
func (r *Repo) UpsertDurationSnapshots(ctx context.Context, batchID uint64, rows []domain.DurationSnapshot) error {
	if len(rows) == 0 {
		return nil
	}
	const query = `INSERT INTO duration_snapshot (anchor_id, person_id, biz_date, cumulative_minutes, batch_id)
	               VALUES (?, ?, ?, ?, ?)
	               ON DUPLICATE KEY UPDATE
	                 cumulative_minutes = VALUES(cumulative_minutes),
	                 batch_id = VALUES(batch_id)`

	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开启事务: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, row := range rows {
		if _, err := tx.ExecContext(ctx, query,
			row.AnchorID, row.PersonID, row.BizDate, row.CumulativeMinutes, batchID,
		); err != nil {
			return fmt.Errorf("写入时长快照(%s): %w", row.AnchorID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交时长快照: %w", err)
	}
	return nil
}

// backfillOrphanSnapshots 把该账号导入时留下的"无主"快照（person_id IS NULL）
// 归到 personID 名下。这些是「先导数据、后加主播」留下的：导入时人还没建档，
// 数据照存但不进榜；绑号回填后重算，历史自动出现在榜上。
// 返回受影响的最早/最晚数据日与是否有变更，供调用方重算三层指标。
func (r *Repo) backfillOrphanSnapshots(ctx context.Context, anchorID string, personID uint64) (time.Time, time.Time, bool, error) {
	res, err := r.db.ExecContext(ctx,
		`UPDATE wave_snapshot SET person_id = ? WHERE anchor_id = ? AND person_id IS NULL`,
		personID, anchorID)
	if err != nil {
		return time.Time{}, time.Time{}, false, fmt.Errorf("回填音浪快照: %w", err)
	}
	n1, _ := res.RowsAffected()

	res, err = r.db.ExecContext(ctx,
		`UPDATE duration_snapshot SET person_id = ? WHERE anchor_id = ? AND person_id IS NULL`,
		personID, anchorID)
	if err != nil {
		return time.Time{}, time.Time{}, false, fmt.Errorf("回填时长快照: %w", err)
	}
	n2, _ := res.RowsAffected()

	if n1+n2 == 0 {
		return time.Time{}, time.Time{}, false, nil
	}

	// 两张表各取一次 min/max：回填范围以实际覆盖到的数据日为准。
	// 只有一张表有数时（比如只导过音浪），缺的那张 MIN/MAX 为 NULL。
	var from, to time.Time
	for _, table := range []string{"wave_snapshot", "duration_snapshot"} {
		var f, t sql.NullTime
		if err := r.db.QueryRowxContext(ctx,
			"SELECT MIN(biz_date), MAX(biz_date) FROM "+table+" WHERE anchor_id = ? AND person_id = ?",
			anchorID, personID).Scan(&f, &t); err != nil {
			return time.Time{}, time.Time{}, false, fmt.Errorf("查询回填范围(%s): %w", table, err)
		}
		if f.Valid && (from.IsZero() || f.Time.Before(from)) {
			from = f.Time
		}
		if t.Valid && t.Time.After(to) {
			to = t.Time
		}
	}
	return from, to, true, nil
}

// listWaveSnapshotsOfAnchors 取若干账号的全部音浪快照（按账号+日期升序）。
// 取全量而不是按区间切：差分需要第一条之前的基线，切区间会让首日数据失真。
func (r *Repo) listWaveSnapshotsOfAnchors(ctx context.Context, anchorIDs []string) ([]domain.WaveSnapshot, error) {
	if len(anchorIDs) == 0 {
		return nil, nil
	}
	query, args, err := sqlxIn(
		`SELECT id, anchor_id, person_id, biz_date, wave_value, rank_in_guild, batch_id, created_at
		 FROM wave_snapshot WHERE anchor_id IN (?) ORDER BY anchor_id, biz_date ASC`,
		anchorIDs)
	if err != nil {
		return nil, err
	}

	var out []domain.WaveSnapshot
	if err := r.db.SelectContext(ctx, &out, query, args...); err != nil {
		return nil, fmt.Errorf("查询音浪快照: %w", err)
	}
	return out, nil
}

// listDurationSnapshotsOfAnchors 同上，取时长快照。
func (r *Repo) listDurationSnapshotsOfAnchors(ctx context.Context, anchorIDs []string) ([]domain.DurationSnapshot, error) {
	if len(anchorIDs) == 0 {
		return nil, nil
	}
	query, args, err := sqlxIn(
		`SELECT id, anchor_id, person_id, biz_date, cumulative_minutes, batch_id, created_at
		 FROM duration_snapshot WHERE anchor_id IN (?) ORDER BY anchor_id, biz_date ASC`,
		anchorIDs)
	if err != nil {
		return nil, err
	}

	var out []domain.DurationSnapshot
	if err := r.db.SelectContext(ctx, &out, query, args...); err != nil {
		return nil, fmt.Errorf("查询时长快照: %w", err)
	}
	return out, nil
}

// RecordAnomaly 记录差分异常。同 anchor+日期+类型只记一条，避免重算刷屏。
func (r *Repo) RecordAnomaly(ctx context.Context, anchorID string, personID uint64,
	bizDate time.Time, kind, detail string) error {

	if _, err := r.db.ExecContext(ctx,
		`INSERT IGNORE INTO data_anomaly (person_id, anchor_id, biz_date, kind, detail)
		 VALUES (?, ?, ?, ?, ?)`, personID, anchorID, bizDate, kind, detail); err != nil {
		return fmt.Errorf("记录数据异常: %w", err)
	}
	return nil
}
