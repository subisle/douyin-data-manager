// 导入记录：防止重复 CSV 入库。
//
// 直接复用 615 的 import_records 表（同一个库，615 的 db-maintenance.js 建的）：
// 两端共享去重账本——615 导过的文件 Go 也不会再导，反之亦然。
// file_hash = 文件字节 MD5；data_hash = 规范化数据的 SHA256。
//
// 去重口径（运营规则）：唯一键都带 import_date——同一文件/同一内容
// 对**同一日期**只许导入一次；换个日期再导同一文件是允许的。
package repo

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"douyin-server/internal/csvparse"
)

type ImportRecord struct {
	ID        uint64    `db:"id" json:"id"`
	Kind      string    `db:"kind" json:"kind"`
	ImportAt  time.Time `db:"import_date" json:"import_date"`
	FileHash  string    `db:"file_hash" json:"file_hash"`
	DataHash  string    `db:"data_hash" json:"data_hash"`
	FileName  string    `db:"file_name" json:"file_name"`
	RowCount  int       `db:"row_count" json:"row_count"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
}

// ImportLogRow 是导入日志的一条：比 ImportRecord 多来源与匹配统计。
// DSN 开了 parseTime，DATE 列必须扫成 time.Time，不能是 string。
type ImportLogRow struct {
	ID             uint64    `db:"id" json:"id"`
	Kind           string    `db:"kind" json:"kind"`
	ImportDate     time.Time `db:"import_date" json:"import_date"`
	FileName       string    `db:"file_name" json:"file_name"`
	RowCount       int       `db:"row_count" json:"row_count"`
	Source         string    `db:"source" json:"source"`
	MatchedCount   int       `db:"matched_count" json:"matched_count"`
	UnmatchedCount int       `db:"unmatched_count" json:"unmatched_count"`
	DuplicateRows  int       `db:"duplicate_rows" json:"duplicate_rows"`
	CreatedAt      time.Time `db:"created_at" json:"created_at"`
}

// ImportStats 记录一次导入的来源与匹配统计（写进 import_records 扩展列）。
type ImportStats struct {
	Source    string // "bot" 或 "web"
	Matched   int
	Unmatched int
	Duplicate int
}

// FindImportRecord 查这个文件/这批数据在该类型+日期下是否已导入过。
// 返回 nil 表示没导过。命中 file_hash 视为同一文件，命中 data_hash 视为同内容。
// 注意：按 kind+日期查——同一文件换个日期导入不会被这条拦。
func (r *Repo) FindImportRecord(ctx context.Context, kind string, date time.Time,
	fileHash, dataHash string) (*ImportRecord, error) {

	var rec ImportRecord
	err := r.db.GetContext(ctx, &rec,
		`SELECT id, kind, import_date, file_hash, data_hash, file_name, row_count, created_at
		   FROM import_records
		  WHERE kind = ? AND import_date = ? AND (file_hash = ? OR data_hash = ?)
		  LIMIT 1`,
		kind, date, fileHash, dataHash)
	if err != nil {
		return nil, translateNotFound(err, "查导入记录")
	}
	return &rec, nil
}

// InsertImportRecord 记一笔导入。与 615 的唯一键一致：
// uq_import_kind_date_file (kind, import_date, file_hash) 和
// uq_import_kind_date_data (kind, import_date, data_hash)。
// stats 为 nil 时按来源 bot / 零统计落库。
func (r *Repo) InsertImportRecord(ctx context.Context, kind string, date time.Time,
	fileHash, dataHash, fileName string, rowCount int, stats *ImportStats) error {

	source, matched, unmatched, duplicate := "bot", 0, 0, 0
	if stats != nil {
		if stats.Source != "" {
			source = stats.Source
		}
		matched, unmatched, duplicate = stats.Matched, stats.Unmatched, stats.Duplicate
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO import_records (kind, import_date, file_hash, data_hash, file_name, row_count,
		                            source, matched_count, unmatched_count, duplicate_rows)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		kind, date, fileHash, dataHash, fileName, rowCount,
		source, matched, unmatched, duplicate)
	return err
}

// ListImportLogs 最近 limit 条导入日志（新→旧）。
func (r *Repo) ListImportLogs(ctx context.Context, limit int) ([]ImportLogRow, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var rows []ImportLogRow
	err := r.db.SelectContext(ctx, &rows,
		`SELECT id, kind, import_date, file_name, row_count,
		        source, matched_count, unmatched_count, duplicate_rows, created_at
		   FROM import_records
		  ORDER BY id DESC
		  LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("查询导入日志: %w", err)
	}
	return rows, nil
}

// ensureImportLogColumns 幂等补齐导入日志扩展列。
// MySQL 8 不支持 ADD COLUMN IF NOT EXISTS，靠 information_schema 判断。
func (r *Repo) ensureImportLogColumns(ctx context.Context) {
	var cols []struct {
		Name string `db:"name"`
	}
	if err := r.db.SelectContext(ctx, &cols,
		`SELECT COLUMN_NAME AS name FROM information_schema.COLUMNS
		  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'import_records'`); err != nil {
		return // 表都还没有时交给 migrations/ensure 流程，这里静默跳过
	}
	have := make(map[string]bool, len(cols))
	for _, c := range cols {
		have[strings.ToLower(c.Name)] = true
	}
	for _, col := range []struct{ name, def string }{
		{"source", "VARCHAR(16) NOT NULL DEFAULT 'bot'"},
		{"matched_count", "INT NOT NULL DEFAULT 0"},
		{"unmatched_count", "INT NOT NULL DEFAULT 0"},
		{"duplicate_rows", "INT NOT NULL DEFAULT 0"},
	} {
		if have[col.name] {
			continue
		}
		_, _ = r.db.ExecContext(ctx,
			fmt.Sprintf("ALTER TABLE import_records ADD COLUMN %s %s", col.name, col.def))
	}
}

// ComputeImportHashes 与 615 的 buildImportMeta 对齐：文件 MD5 + 规范化数据 SHA256。
// 规范化 = 匹配到的行取 {anchorId, value, rank(wave)}，按 anchorId 排序后序列化。
// bot 与 web 导入共用，保证两端算出的指纹一致、账本互通。
func ComputeImportHashes(fileBytes []byte, rows []csvparse.Row, kind csvparse.Kind) (string, string) {
	fileHash := fmt.Sprintf("%x", md5.Sum(fileBytes))

	type canonicalRow struct {
		AnchorID string `json:"anchorId"`
		Value    int64  `json:"value"`
		Rank     int    `json:"rank"`
	}
	canonical := make([]canonicalRow, 0, len(rows))
	for _, row := range rows {
		if row.AnchorID == "" || row.Err != "" {
			continue
		}
		item := canonicalRow{AnchorID: row.AnchorID, Value: row.Wave}
		if kind == csvparse.KindDuration {
			item.Value = int64(row.Minutes)
		} else if row.Rank != nil {
			item.Rank = *row.Rank
		}
		canonical = append(canonical, item)
	}
	sort.Slice(canonical, func(i, j int) bool { return canonical[i].AnchorID < canonical[j].AnchorID })
	blob, _ := json.Marshal(canonical)
	dataHash := fmt.Sprintf("%x", sha256.Sum256(blob))
	return fileHash, dataHash
}
