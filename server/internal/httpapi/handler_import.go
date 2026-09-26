package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"douyin-server/internal/csvparse"
	"douyin-server/internal/domain"
	"douyin-server/internal/repo"
)

// importSnapshots POST /api/v1/imports/snapshots
//
// 请求体：
//
//	{
//	  "date": "2026-09-18",
//	  "source": "douyinlang",
//	  "operator": "admin",
//	  "waves":     [{"anchorId":"A1","waveValue":123456,"rank":3}],
//	  "durations": [{"anchorId":"A1","minutes":480}]
//	}
//
// 写完快照会立刻重算涉及到的主播，保证查到的永远是新鲜指标。
// anchor_id 没绑过主播的行会被跳过并计入 skipped，不会静默丢数据。
func (s *Server) importSnapshots(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Date     string `json:"date"`
		Source   string `json:"source"`
		Operator string `json:"operator"`
		Waves    []struct {
			AnchorID  string `json:"anchorId"`
			WaveValue int64  `json:"waveValue"`
			Rank      *int   `json:"rank"`
		} `json:"waves"`
		Durations []struct {
			AnchorID string `json:"anchorId"`
			Minutes  int    `json:"minutes"`
		} `json:"durations"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, "请求体不是合法 JSON")
		return
	}
	if req.Date == "" {
		badRequest(w, "date 不能为空")
		return
	}
	date, err := time.ParseInLocation(isoDate, req.Date, time.Local)
	if err != nil {
		badRequest(w, "date 格式应为 YYYY-MM-DD")
		return
	}
	if len(req.Waves) == 0 && len(req.Durations) == 0 {
		badRequest(w, "waves 与 durations 不能同时为空")
		return
	}
	if req.Source == "" {
		req.Source = "manual"
	}

	batchID, err := s.repo.CreateBatch(r.Context(), date, req.Source, req.Operator)
	if err != nil {
		internalError(w, err)
		return
	}

	// 解析归属：认不出的账号单独记下来，让前端能看到"这次导入漏了谁"。
	waves := make([]domain.WaveSnapshot, 0, len(req.Waves))
	var skipped []string
	owners := map[uint64]bool{}

	for _, item := range req.Waves {
		if item.AnchorID == "" {
			continue
		}
		personID, err := s.repo.ResolveAnchorOwner(r.Context(), item.AnchorID)
		if err != nil {
			if errors.Is(err, repo.ErrNotFound) {
				skipped = append(skipped, item.AnchorID)
				continue
			}
			_ = s.repo.FinishBatch(r.Context(), batchID, 0, err.Error())
			internalError(w, err)
			return
		}
		waves = append(waves, domain.WaveSnapshot{
			AnchorID:    item.AnchorID,
			PersonID:    &personID,
			BizDate:     date,
			WaveValue:   item.WaveValue,
			RankInGuild: item.Rank,
		})
		owners[personID] = true
	}

	durations := make([]domain.DurationSnapshot, 0, len(req.Durations))
	for _, item := range req.Durations {
		if item.AnchorID == "" {
			continue
		}
		personID, err := s.repo.ResolveAnchorOwner(r.Context(), item.AnchorID)
		if err != nil {
			if errors.Is(err, repo.ErrNotFound) {
				skipped = append(skipped, item.AnchorID)
				continue
			}
			_ = s.repo.FinishBatch(r.Context(), batchID, 0, err.Error())
			internalError(w, err)
			return
		}
		durations = append(durations, domain.DurationSnapshot{
			AnchorID:          item.AnchorID,
			PersonID:          &personID,
			BizDate:           date,
			CumulativeMinutes: item.Minutes,
		})
		owners[personID] = true
	}

	if err := s.repo.UpsertWaveSnapshots(r.Context(), batchID, waves); err != nil {
		_ = s.repo.FinishBatch(r.Context(), batchID, 0, err.Error())
		internalError(w, err)
		return
	}
	if err := s.repo.UpsertDurationSnapshots(r.Context(), batchID, durations); err != nil {
		_ = s.repo.FinishBatch(r.Context(), batchID, 0, err.Error())
		internalError(w, err)
		return
	}

	// 只重算今天这一天：导入是按天来的，没必要全量刷。
	for personID := range owners {
		if err := s.repo.RecomputePerson(r.Context(), personID, date, date); err != nil {
			_ = s.repo.FinishBatch(r.Context(), batchID, 0, err.Error())
			internalError(w, err)
			return
		}
	}

	rowCount := len(waves) + len(durations)
	if err := s.repo.FinishBatch(r.Context(), batchID, rowCount, ""); err != nil {
		internalError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"batchId":  batchID,
		"imported": rowCount,
		"persons":  len(owners),
		"skipped":  skipped,
	})
}

// recompute POST /api/v1/imports/recompute
//
// 物化表脏了就用它重建。数据量小，全量重算也是秒级。
// 请求体：{"from":"2026-09-01","to":"2026-09-30","personId":123}
// 不传 personId 表示全量。
func (s *Server) recompute(w http.ResponseWriter, r *http.Request) {
	var req struct {
		From     string `json:"from"`
		To       string `json:"to"`
		PersonID uint64 `json:"personId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, "请求体不是合法 JSON")
		return
	}
	from, to, ok := parseRange(w, req.From, req.To)
	if !ok {
		return
	}

	if req.PersonID > 0 {
		if err := s.repo.RecomputePerson(r.Context(), req.PersonID, from, to); err != nil {
			internalError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"persons": 1})
		return
	}

	n, err := s.repo.RecomputeAll(r.Context(), from, to)
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"persons": n})
}

/* ------------------------------ CSV 导入 ------------------------------ */

// maxUploadBytes 上传大小上限。运营的 CSV 最多几百行，20MB 绰绰有余。
const maxUploadBytes = 20 << 20

// readCSV 从 multipart 表单里读出文件并解析。
func readCSV(r *http.Request) (csvparse.Kind, []csvparse.Row, string, []byte, error) {
	if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
		return "", nil, "", nil, fmt.Errorf("解析上传表单失败: %w", err)
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		return "", nil, "", nil, fmt.Errorf("读取上传文件失败: %w", err)
	}
	defer func() { _ = file.Close() }()

	// 原始字节留着算 MD5（去重账本用），解析走 tee
	var buf bytes.Buffer
	kind, rows, err := csvparse.Parse(io.TeeReader(file, &buf))
	if err != nil {
		return "", nil, "", nil, err
	}
	return kind, rows, header.Filename, buf.Bytes(), nil
}

// previewImport POST /api/v1/imports/preview （multipart: file, date）
//
// 只回答"导进去会发生什么"，不写任何数据。覆盖已有数据是危险操作，
// 必须让人先看清楚新增/覆盖/无变化的分布再点确认。
func (s *Server) previewImport(w http.ResponseWriter, r *http.Request) {
	kind, rows, filename, raw, err := readCSV(r)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	if kind == csvparse.KindUnknown {
		badRequest(w, "看不出这是音浪表还是时长表：表头里没有「音浪」或「时长」字样")
		return
	}

	rawDate := strings.TrimSpace(r.FormValue("date"))
	if rawDate == "" {
		badRequest(w, "date 不能为空")
		return
	}
	// 时长按月导入（YYYY-MM），音浪按日（YYYY-MM-DD）
	if kind == csvparse.KindDuration && len(rawDate) == 7 {
		rawDate += "-01"
	}
	date, err := time.ParseInLocation(isoDate, rawDate, time.Local)
	if err != nil {
		badRequest(w, "date 格式应为 YYYY-MM-DD（时长表可用 YYYY-MM）")
		return
	}

	preview, err := s.repo.BuildImportPreview(r.Context(), rows, kind, date)
	if err != nil {
		internalError(w, err)
		return
	}

	// 去重账本仅作记录与提示（导入不硬拦，见 importCSV）。
	var duplicate *repo.ImportRecord
	fileHash, dataHash := repo.ComputeImportHashes(raw, rows, kind)
	if rec, err := s.repo.FindImportRecord(r.Context(), string(kind), date, fileHash, dataHash); err == nil {
		duplicate = rec
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"filename":  filename,
		"date":      date.Format(isoDate),
		"preview":   preview,
		"duplicate": duplicate,
	})
}

// importCSV POST /api/v1/imports/csv （multipart: file, date）
func (s *Server) importCSV(w http.ResponseWriter, r *http.Request) {
	kind, rows, filename, raw, err := readCSV(r)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	if kind == csvparse.KindUnknown {
		badRequest(w, "看不出这是音浪表还是时长表")
		return
	}

	rawDate := strings.TrimSpace(r.FormValue("date"))
	if kind == csvparse.KindDuration && len(rawDate) == 7 {
		rawDate += "-01"
	}
	date, err := time.ParseInLocation(isoDate, rawDate, time.Local)
	if err != nil {
		badRequest(w, "date 格式应为 YYYY-MM-DD（时长表可用 YYYY-MM）")
		return
	}

	// 去重账本仅作记录（InsertImportRecord 是 upsert），不拦截重复导入：
	// 快照按（主播, 日期）upsert，重复导入相同数据是幂等的。
	fileHash, dataHash := repo.ComputeImportHashes(raw, rows, kind)

	preview, err := s.repo.BuildImportPreview(r.Context(), rows, kind, date)
	if err != nil {
		internalError(w, err)
		return
	}

	batchID, err := s.repo.CreateBatch(r.Context(), date, "csv", "web")
	if err != nil {
		internalError(w, err)
		return
	}

	var affected int
	var skipped []string
	if kind == csvparse.KindDuration {
		affected, err = s.repo.ApplyDurationImport(r.Context(), preview.Rows, date, batchID)
	} else {
		affected, skipped, err = s.repo.ApplyImport(r.Context(), preview.Rows, kind, date, batchID)
	}
	if err != nil {
		_ = s.repo.FinishBatch(r.Context(), batchID, 0, err.Error())
		internalError(w, err)
		return
	}

	// 只重算这一天
	personIDs := map[uint64]bool{}
	for _, row := range preview.Rows {
		if row.PersonID != nil {
			personIDs[*row.PersonID] = true
		}
	}
	for id := range personIDs {
		if err := s.repo.RecomputePerson(r.Context(), id, date, date); err != nil {
			_ = s.repo.FinishBatch(r.Context(), batchID, affected, err.Error())
			internalError(w, err)
			return
		}
	}

	if err := s.repo.FinishBatch(r.Context(), batchID, affected, ""); err != nil {
		internalError(w, err)
		return
	}

	// 记一笔去重账（与 bot/615 共用）。失败不算失败：数据已入库，最多下次再拦一次
	if err := s.repo.InsertImportRecord(r.Context(), string(kind), date,
		fileHash, dataHash, filename, affected, &repo.ImportStats{Source: "web"}); err != nil {
		// 只进日志，不影响响应
		_ = err
	}

	// 数据更新了：安排自动日报（防抖合并，每个数据日只推一次）
	if m, ok := s.botManager(); ok {
		m.ScheduleAutoReport(date)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"batchId":  batchID,
		"kind":     kind,
		"imported": affected,
		"persons":  len(personIDs),
		"skipped":  skipped,
		"counts": map[string]int{
			"new":       preview.NewCount,
			"changed":   preview.ChgCount,
			"unchanged": preview.SameCount,
			"unmatched": preview.Unmatched,
		},
	})
}

func parseRange(w http.ResponseWriter, rawFrom, rawTo string) (time.Time, time.Time, bool) {
	now := time.Now()
	from := now.AddDate(0, 0, -30)
	to := now

	if rawFrom != "" {
		v, err := time.ParseInLocation(isoDate, rawFrom, time.Local)
		if err != nil {
			badRequest(w, "from 格式应为 YYYY-MM-DD")
			return time.Time{}, time.Time{}, false
		}
		from = v
	}
	if rawTo != "" {
		v, err := time.ParseInLocation(isoDate, rawTo, time.Local)
		if err != nil {
			badRequest(w, "to 格式应为 YYYY-MM-DD")
			return time.Time{}, time.Time{}, false
		}
		to = v
	}
	if to.Before(from) {
		badRequest(w, "to 不能早于 from")
		return time.Time{}, time.Time{}, false
	}
	return from, to, true
}

// importLogs GET /api/v1/imports/logs?limit=50
// 最近导入日志（含来源与匹配统计），网页「导入日志」页使用。
func (s *Server) importLogs(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	logs, err := s.repo.ListImportLogs(r.Context(), limit)
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, logs)
}
