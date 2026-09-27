package repo

import (
	"context"
	"fmt"
	"time"

	"douyin-server/internal/domain"
)

// BatchDeletePersons 批量软删除。硬删会毁掉月榜和年榜，一律禁止。
func (r *Repo) BatchDeletePersons(ctx context.Context, ids []uint64) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	query, args, err := sqlxIn(
		`UPDATE person SET deleted_at = NOW(3) WHERE id IN (?) AND deleted_at IS NULL`, uint64ToStrings(ids))
	if err != nil {
		return 0, err
	}
	res, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("批量删除主播: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("读取影响行数: %w", err)
	}
	if n > 0 {
		r.MarkDataUpdated(ctx)
	}
	return int(n), nil
}

// DuplicateGroup 是同名主播的一组，用于"重复数据检测"。
type DuplicateGroup struct {
	Name    string          `json:"name"`
	Persons []domain.Person `json:"persons"`
}

// FindDuplicatePersons 按姓名找重复。同名多半是导入时重复建的人。
func (r *Repo) FindDuplicatePersons(ctx context.Context) ([]DuplicateGroup, error) {
	type row struct {
		Name string `db:"name"`
		Cnt  int    `db:"cnt"`
	}
	var names []row
	if err := r.db.SelectContext(ctx, &names,
		`SELECT name, COUNT(*) AS cnt FROM person
		 WHERE deleted_at IS NULL AND name <> ''
		 GROUP BY name HAVING cnt > 1 ORDER BY cnt DESC, name ASC`); err != nil {
		return nil, fmt.Errorf("查询重复主播: %w", err)
	}

	out := make([]DuplicateGroup, 0, len(names))
	for _, n := range names {
		var persons []domain.Person
		if err := r.db.SelectContext(ctx, &persons,
			`SELECT id, name, gender, master_id, generation, group_name, avatar_url,
			        hide_in_daily_report, status, joined_at, created_at, updated_at
			 FROM person WHERE name = ? AND deleted_at IS NULL ORDER BY id ASC`, n.Name); err != nil {
			return nil, fmt.Errorf("查询同名主播: %w", err)
		}
		out = append(out, DuplicateGroup{Name: n.Name, Persons: persons})
	}
	return out, nil
}

// MergePersons 把 secondary 合并进 primary。
//
// 规则（与 615 一致）：
//   - 音浪始终合并：secondary 的账号与快照全部划到 primary 名下
//   - 时长可选：mergeDuration=false 时丢弃 secondary 的时长快照，
//     因为两个账号的时长不能简单相加（同一个人同一时间只能播一场）
//   - 合并后 secondary 软删除
func (r *Repo) MergePersons(ctx context.Context, primaryID, secondaryID uint64, mergeDuration bool) error {
	if primaryID == secondaryID {
		return fmt.Errorf("不能把自己合并给自己")
	}

	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开启事务: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`UPDATE account SET person_id = ?, is_primary = 0 WHERE person_id = ?`, primaryID, secondaryID); err != nil {
		return fmt.Errorf("转移账号: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE wave_snapshot SET person_id = ? WHERE person_id = ?`, primaryID, secondaryID); err != nil {
		return fmt.Errorf("转移音浪快照: %w", err)
	}

	if mergeDuration {
		if _, err := tx.ExecContext(ctx,
			`UPDATE duration_snapshot SET person_id = ? WHERE person_id = ?`, primaryID, secondaryID); err != nil {
			return fmt.Errorf("转移时长快照: %w", err)
		}
	} else {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM duration_snapshot WHERE person_id = ?`, secondaryID); err != nil {
			return fmt.Errorf("丢弃时长快照: %w", err)
		}
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE person SET master_id = ? WHERE master_id = ?`, primaryID, secondaryID); err != nil {
		return fmt.Errorf("转移徒弟: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE person SET deleted_at = NOW(3) WHERE id = ? AND deleted_at IS NULL`, secondaryID); err != nil {
		return fmt.Errorf("软删除被合并方: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交合并: %w", err)
	}
	r.MarkDataUpdated(ctx)
	return nil
}

// SetMaster 设置或清空师傅。
func (r *Repo) SetMaster(ctx context.Context, personID uint64, masterID *uint64) error {
	if masterID != nil && *masterID == personID {
		return fmt.Errorf("不能把自己设为自己的师傅")
	}
	res, err := r.db.ExecContext(ctx,
		`UPDATE person SET master_id = ? WHERE id = ? AND deleted_at IS NULL`, masterID, personID)
	if err != nil {
		return fmt.Errorf("设置师傅: %w", err)
	}
	if err := ensureAffected(res, "设置师傅"); err != nil {
		return err
	}
	r.MarkDataUpdated(ctx)
	return nil
}

// SaveDailySnapshot 直接写某主播某天的快照（主播管理页的"编辑音浪/时长"用）。
func (r *Repo) SaveDailySnapshot(ctx context.Context, anchorID string, personID uint64,
	bizDate time.Time, wave int64, minutes int, rank *int) error {

	if _, err := r.db.ExecContext(ctx,
		`INSERT INTO wave_snapshot (anchor_id, person_id, biz_date, wave_value, rank_in_guild)
		 VALUES (?, ?, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE wave_value = VALUES(wave_value), rank_in_guild = VALUES(rank_in_guild)`,
		anchorID, personID, bizDate, wave, rank); err != nil {
		return fmt.Errorf("保存音浪快照: %w", err)
	}
	if minutes > 0 {
		if _, err := r.db.ExecContext(ctx,
			`INSERT INTO duration_snapshot (anchor_id, person_id, biz_date, cumulative_minutes)
			 VALUES (?, ?, ?, ?)
			 ON DUPLICATE KEY UPDATE cumulative_minutes = VALUES(cumulative_minutes)`,
			anchorID, personID, bizDate, minutes); err != nil {
			return fmt.Errorf("保存时长快照: %w", err)
		}
	}
	return nil
}

func uint64ToStrings(ids []uint64) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, fmt.Sprint(id))
	}
	return out
}
