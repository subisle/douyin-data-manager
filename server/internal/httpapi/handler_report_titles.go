package httpapi

import (
	"encoding/json"
	"net/http"

	"douyin-server/internal/render"
)

// getReportTitles GET /api/v1/exports/report-titles
// 返回男/女两个自定义日报图标题；空串表示用内置默认（男 ST-001 / 女 主播数据统计）。
func (s *Server) getReportTitles(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.repo.GetReportTitles(r.Context()))
}

// putReportTitles PUT /api/v1/exports/report-titles
// 请求体 {"male":"...","female":"..."}；字段传 null/缺省 = 不改，
// 传空串 = 恢复该性别默认标题。
func (s *Server) putReportTitles(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Male   *string `json:"male"`
		Female *string `json:"female"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, "请求体不是合法 JSON")
		return
	}
	if req.Male == nil && req.Female == nil {
		badRequest(w, "male / female 至少传一个")
		return
	}
	if req.Male != nil {
		if err := s.repo.SetReportTitle(r.Context(), "male", *req.Male); err != nil {
			internalError(w, err)
			return
		}
	}
	if req.Female != nil {
		if err := s.repo.SetReportTitle(r.Context(), "female", *req.Female); err != nil {
			internalError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, s.repo.GetReportTitles(r.Context()))
}

// getReportColumns GET /api/v1/exports/report-columns
// 返回保存的日报图列勾选（逗号分隔的 615 列键）；空 = 默认五列。
func (s *Server) getReportColumns(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"columns": s.repo.GetReportColumns(r.Context())})
}

// putReportColumns PUT /api/v1/exports/report-columns
// 请求体 {"columns":"rank,name,dailyWave,totalWave"}；空串 = 恢复默认列。
// 列键合法性在这里先验一遍，坏值不落库。
func (s *Server) putReportColumns(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Columns string `json:"columns"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, "请求体不是合法 JSON")
		return
	}
	if req.Columns != "" {
		if _, err := render.ParseColumnSet(req.Columns); err != nil {
			badRequest(w, err.Error())
			return
		}
	}
	if err := s.repo.SetReportColumns(r.Context(), req.Columns); err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"columns": s.repo.GetReportColumns(r.Context())})
}
