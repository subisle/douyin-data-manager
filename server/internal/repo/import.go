package repo

import (
	"context"
	"fmt"
	"time"

	"douyin-server/internal/csvparse"
	"douyin-server/internal/domain"
)

// ImportRowStatus 预览里每一行的状态，沿用 615 的三态口径。
type ImportRowStatus string

const (
	StatusNew       ImportRowStatus = "new"       // 新增
	StatusChanged   ImportRowStatus = "changed"   // 覆盖已有值
	StatusUnchanged ImportRowStatus = "unchanged" // 与库里一致
	StatusUnmatched ImportRowStatus = "unmatched" // 主播列表里没有，但仍会导入
	StatusDuplicate ImportRowStatus = "duplicate" // 文件内重复行
	StatusSkipped   ImportRowStatus = "skipped"   // ID 空或数值解析失败
)

// ImportPreviewRow 预览的一行。
type ImportPreviewRow struct {
	Status   ImportRowStatus `json:"status"`
	RawIndex int             `json:"rawIndex,omitempty"` // CSV 里的行号（表头算第 1 行），报告错误用
	AnchorID string          `json:"anchorId"`
	Name     string          `json:"name"`
	PersonID *uint64         `json:"personId,omitempty"`
	Current  *int64          `json:"current,omitempty"`
	Next     int64           `json:"next"`
	Rank     *int            `json:"rank,omitempty"`
	Err      string          `json:"err,omitempty"`
}

// ImportPreview 整个预览结果。
type ImportPreview struct {
	Kind      csvparse.Kind      `json:"kind"`
	RowCount  int                `json:"rowCount"`
	Rows      []ImportPreviewRow `json:"rows"`
	NewCount  int                `json:"newCount"`
	ChgCount  int                `json:"changedCount"`
	SameCount int                `json:"unchangedCount"`
	Unmatched int                `json:"unmatchedCount"`
	Duplicate int                `json:"duplicateCount"`
	Skipped   int                `json:"skippedCount"`
}

// BuildImportPreview 解析 CSV 并与库里现有值比对，产出预览。
//
// 这一步不做任何写入，只回答"导进去会发生什么"——
// 覆盖已有数据是危险操作，必须让人先看清楚再确认。
func (r *Repo) BuildImportPreview(ctx context.Context, rows []csvparse.Row,
	kind csvparse.Kind, bizDate time.Time) (ImportPreview, error) {

	out := ImportPreview{Kind: kind}
	seen := map[string]bool{}
	existing := map[string]int64{}

	// 一次性把涉及的账号当天快照取出来，避免逐行查库
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.AnchorID != "" {
			ids = append(ids, row.AnchorID)
		}
	}
	if len(ids) > 0 {
		// 两个 ? 都要给 sqlx.In：它扫到没有参数可绑的 ? 会报
		// "number of bindVars exceeds arguments"（bot 导入首跑就栽在这）。
		query, args, err := sqlxIn(
			`SELECT anchor_id, wave_value FROM wave_snapshot WHERE anchor_id IN (?) AND biz_date = ?`,
			ids, bizDate)
		if err != nil {
			return out, err
		}

		type snap struct {
			AnchorID  string `db:"anchor_id"`
			WaveValue int64  `db:"wave_value"`
		}
		var snaps []snap
		if err := r.db.SelectContext(ctx, &snaps, query, args...); err != nil {
			return out, fmt.Errorf("查询已有快照: %w", err)
		}
		for _, s := range snaps {
			existing[s.AnchorID] = s.WaveValue
		}
	}

	for _, row := range rows {
		pr := ImportPreviewRow{
			Status:   StatusSkipped,
			RawIndex: row.RawIndex,
			AnchorID: row.AnchorID,
			Name:     row.Name,
			Rank:     row.Rank,
			Next:     row.Wave,
			Err:      row.Err,
		}
		if kind == csvparse.KindDuration {
			pr.Next = int64(row.Minutes)
		}

		switch {
		case row.Err != "":
			pr.Status = StatusSkipped
			out.Skipped++
		case row.AnchorID == "" && row.Name == "":
			// ID 和艺名都没有，确实没法匹配（615 同款）。
			// 只有艺名列的 CSV 是合法的：走 default 用艺名弹性匹配。
			pr.Status = StatusSkipped
			pr.Err = "缺少主播 ID 和艺名"
			out.Skipped++
		case seen[row.AnchorID] && row.AnchorID != "":
			pr.Status = StatusDuplicate
			out.Duplicate++
		default:
			seen[row.AnchorID] = true
			personID, err := r.ResolveAnchorOwnerFlexible(ctx, row.AnchorID, row.Name)
			if err != nil {
				pr.Status = StatusUnmatched
				out.Unmatched++
			} else {
				pr.PersonID = &personID
				cur, ok := existing[row.AnchorID]
				if !ok {
					pr.Status = StatusNew
					out.NewCount++
				} else {
					pr.Current = &cur
					if cur == pr.Next {
						pr.Status = StatusUnchanged
						out.SameCount++
					} else {
						pr.Status = StatusChanged
						out.ChgCount++
					}
				}
			}
		}
		out.Rows = append(out.Rows, pr)
	}
	out.RowCount = len(out.Rows)
	return out, nil
}

// ApplyImport 把预览确认过的行写入快照。
// 只写能匹配到主播的行，未匹配的跳过（与 615 一致：列表里没有的仍然导入，
// 但这里没有 person_id 无法落库，所以跳过并在结果里返回）。
func (r *Repo) ApplyImport(ctx context.Context, rows []ImportPreviewRow,
	kind csvparse.Kind, bizDate time.Time, batchID uint64) (int, []string, error) {

	waves := make([]domain.WaveSnapshot, 0)
	skipped := []string{}

	for _, row := range rows {
		if row.Status == StatusSkipped || row.Status == StatusDuplicate || row.PersonID == nil {
			if row.AnchorID != "" {
				skipped = append(skipped, row.AnchorID)
			}
			continue
		}
		if kind == csvparse.KindDuration {
			continue // 时长走另一张表，见 ApplyDurationImport
		}
		waves = append(waves, domain.WaveSnapshot{
			AnchorID:    row.AnchorID,
			PersonID:    *row.PersonID,
			BizDate:     bizDate,
			WaveValue:   row.Next,
			RankInGuild: row.Rank,
		})
	}

	if err := r.UpsertWaveSnapshots(ctx, batchID, waves); err != nil {
		return 0, skipped, err
	}
	return len(waves), skipped, nil
}

// ApplyDurationImport 写入时长快照（分钟）。
func (r *Repo) ApplyDurationImport(ctx context.Context, rows []ImportPreviewRow,
	bizDate time.Time, batchID uint64) (int, error) {

	durations := make([]domain.DurationSnapshot, 0)
	for _, row := range rows {
		if row.Status == StatusSkipped || row.Status == StatusDuplicate || row.PersonID == nil {
			continue
		}
		durations = append(durations, domain.DurationSnapshot{
			AnchorID:          row.AnchorID,
			PersonID:          *row.PersonID,
			BizDate:           bizDate,
			CumulativeMinutes: int(row.Next),
		})
	}
	if err := r.UpsertDurationSnapshots(ctx, batchID, durations); err != nil {
		return 0, err
	}
	return len(durations), nil
}
