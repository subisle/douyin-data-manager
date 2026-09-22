// 数据清理：按日期区间删除采集快照（音浪/时长）与去重账本。
//
// 只动快照与账本，指标表由调用方删除+重算（见 handler_purge.go）：
// 重算是 upsert 语义，不会删除"已无快照"的日期行，所以那些行必须显式删掉，
// 否则清掉的数据会以 0 值幽灵行的形式留在日/月/年指标里。
package repo

import (
	"context"
	"fmt"
	"time"
)

// PurgeWaveRange 删除区间（含首尾）内的音浪快照：生产表 + 本地 staging 副本
// + 日指标行。月/年指标由重算覆盖（调用方把重算范围扩到整年）。
func (r *Repo) PurgeWaveRange(ctx context.Context, from, to time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM wave_snapshot WHERE biz_date >= ? AND biz_date <= ?`, from, to)
	if err != nil {
		return 0, fmt.Errorf("清理音浪快照: %w", err)
	}
	n, _ := res.RowsAffected()

	// 本地 staging 副本一并清，防止下次 sync-615 把旧数据带回来
	if _, err := r.db.ExecContext(ctx,
		`DELETE FROM wave_snapshots WHERE import_date >= ? AND import_date <= ?`, from, to); err != nil {
		return n, fmt.Errorf("清理音浪 staging 副本: %w", err)
	}
	// 日指标：清掉区间内所有行——有快照的日期重算会补回，没快照的（被清的）不留幽灵行
	if _, err := r.db.ExecContext(ctx,
		`DELETE FROM daily_metric WHERE biz_date >= ? AND biz_date <= ?`, from, to); err != nil {
		return n, fmt.Errorf("清理日指标: %w", err)
	}
	return n, nil
}

// PurgeDurationRange 删除区间（含首尾）内的时长快照与本地 staging 副本。
// 日指标的 minutes 列会在重算时自动归零（有音浪快照的日期会重建）。
func (r *Repo) PurgeDurationRange(ctx context.Context, from, to time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM duration_snapshot WHERE biz_date >= ? AND biz_date <= ?`, from, to)
	if err != nil {
		return 0, fmt.Errorf("清理时长快照: %w", err)
	}
	n, _ := res.RowsAffected()
	if _, err := r.db.ExecContext(ctx,
		`DELETE FROM duration_snapshots WHERE import_date >= ? AND import_date <= ?`, from, to); err != nil {
		return n, fmt.Errorf("清理时长 staging 副本: %w", err)
	}
	return n, nil
}

// PurgeMonthlyYearly 清掉区间覆盖到的月/年指标行，让重算按剩余数据重建。
// 只清波及到的月与年；重算范围必须覆盖这些月份所在的整年。
func (r *Repo) PurgeMonthlyYearly(ctx context.Context, from, to time.Time) error {
	fromPeriod := from.Format("2006-01")
	toPeriod := to.Format("2006-01")
	if _, err := r.db.ExecContext(ctx,
		`DELETE FROM monthly_metric WHERE period >= ? AND period <= ?`, fromPeriod, toPeriod); err != nil {
		return fmt.Errorf("清理月指标: %w", err)
	}
	if _, err := r.db.ExecContext(ctx,
		`DELETE FROM yearly_metric WHERE year >= ? AND year <= ?`, from.Year(), to.Year()); err != nil {
		return fmt.Errorf("清理年指标: %w", err)
	}
	return nil
}

// PurgeImportLedger 清掉区间内指定类型的去重账本，让这些 CSV 可以重新导入。
// kind: wave / duration。
func (r *Repo) PurgeImportLedger(ctx context.Context, kind string, from, to time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM import_records WHERE kind = ? AND import_date >= ? AND import_date <= ?`,
		kind, from, to)
	if err != nil {
		return 0, fmt.Errorf("清理导入账本: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
