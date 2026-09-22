package httpapi

import (
	"encoding/json"
	"net/http"
	"time"
)

// purgeData POST /api/v1/imports/purge
//
// 请求体：{"type":"wave|duration|both","from":"YYYY-MM-DD","to":"YYYY-MM-DD"}
// 删除区间（含首尾）内的采集快照 + 本地 staging 副本 + 去重账本，
// 然后按整年重算指标——整年而不是只算区间所在月份，因为：
//  1. 月指标按整月聚合，区间只是月中几天时必须带全整月剩余天数；
//  2. 区间删到月末会改变次月差分基线；
//  3. 年指标按 12 个月聚合，任何月份变化都要重建。
func (s *Server) purgeData(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Type string `json:"type"`
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, "请求体不是合法 JSON")
		return
	}
	if req.Type == "" {
		req.Type = "both"
	}
	if req.Type != "wave" && req.Type != "duration" && req.Type != "both" {
		badRequest(w, "type 只能是 wave / duration / both")
		return
	}
	// 删除操作日期必填，不用 parseRange 的"最近30天"默认值——误删不可逆
	if req.From == "" || req.To == "" {
		badRequest(w, "from 和 to 必填（YYYY-MM-DD）")
		return
	}
	from, err := time.ParseInLocation(isoDate, req.From, time.Local)
	if err != nil {
		badRequest(w, "from 格式应为 YYYY-MM-DD")
		return
	}
	to, err := time.ParseInLocation(isoDate, req.To, time.Local)
	if err != nil {
		badRequest(w, "to 格式应为 YYYY-MM-DD")
		return
	}
	if from.After(to) {
		badRequest(w, "from 不能晚于 to")
		return
	}

	// 重算范围：from 所在年 1 月 1 日 ~ to 所在年 12 月 31 日
	recomputeFrom := time.Date(from.Year(), 1, 1, 0, 0, 0, 0, time.Local)
	recomputeTo := time.Date(to.Year(), 12, 31, 23, 59, 59, 0, time.Local)

	var waveDeleted, durDeleted, ledgerDeleted int64
	kinds := []string{req.Type}
	if req.Type == "both" {
		kinds = []string{"wave", "duration"}
	}
	for _, kind := range kinds {
		var n int64
		var err error
		if kind == "wave" {
			n, err = s.repo.PurgeWaveRange(r.Context(), from, to)
		} else {
			n, err = s.repo.PurgeDurationRange(r.Context(), from, to)
		}
		if err != nil {
			internalError(w, err)
			return
		}
		if kind == "wave" {
			waveDeleted = n
		} else {
			durDeleted = n
		}
		m, err := s.repo.PurgeImportLedger(r.Context(), kind, from, to)
		if err != nil {
			internalError(w, err)
			return
		}
		ledgerDeleted += m
	}

	// 波及到的月/年指标行先删掉，重算会按剩余数据重建
	if req.Type == "wave" || req.Type == "both" {
		if err := s.repo.PurgeMonthlyYearly(r.Context(), from, to); err != nil {
			internalError(w, err)
			return
		}
	}

	if _, err := s.repo.RecomputeAll(r.Context(), recomputeFrom, recomputeTo); err != nil {
		internalError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"waveDeleted":     waveDeleted,
		"durationDeleted": durDeleted,
		"ledgerDeleted":   ledgerDeleted,
		"recomputedFrom":  recomputeFrom.Format(isoDate),
		"recomputedTo":    recomputeTo.Format(isoDate),
	})
}
