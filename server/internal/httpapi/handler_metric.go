package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"douyin-server/internal/domain"
	"douyin-server/internal/metric"
)

const isoDate = "2006-01-02"

// dailyMetrics GET /api/v1/metrics/daily?date=YYYY-MM-DD&gender=male|female
//
// 返回的是日榜原始行，按音浪降序。导出图片与前端表格都直接吃这份数据。
func (s *Server) dailyMetrics(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("date")
	if raw == "" {
		raw = time.Now().Format(isoDate)
	}
	date, err := time.ParseInLocation(isoDate, raw, time.Local)
	if err != nil {
		badRequest(w, "date 格式应为 YYYY-MM-DD")
		return
	}
	gender := domain.Gender(r.URL.Query().Get("gender"))
	if gender != "" && !gender.Valid() {
		badRequest(w, "gender 只能是 male / female / unknown")
		return
	}

	rows, err := s.repo.ListDailyByDate(r.Context(), date, gender)
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

// monthlyMetrics GET /api/v1/metrics/monthly?period=YYYY-MM&gender=
//
// 月音浪 + 月直播时长，用户明确要的一等公民指标。
func (s *Server) monthlyMetrics(w http.ResponseWriter, r *http.Request) {
	period := r.URL.Query().Get("period")
	if period == "" {
		period = time.Now().Format("2006-01")
	}
	if len(period) != 7 || period[4] != '-' {
		badRequest(w, "period 格式应为 YYYY-MM")
		return
	}
	gender := domain.Gender(r.URL.Query().Get("gender"))
	if gender != "" && !gender.Valid() {
		badRequest(w, "gender 只能是 male / female / unknown")
		return
	}

	rows, err := s.repo.ListMonthlyByPeriod(r.Context(), period, gender)
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

// dashboard GET /api/v1/metrics/dashboard?date=YYYY-MM-DD&days=30&top=5
//
// 首页一次拿全：KPI、趋势曲线、未播预警、榜单前 N。
// 后端一次查询算完，前端不必为了一个页面打四次接口。
func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("date")
	if raw == "" {
		// 不传日期 → 昨天。数据是 T+1 入的，今天还没有数据，
		// 默认今天会让首页一进来就是空的（机器人日报同理）。
		raw = time.Now().AddDate(0, 0, -1).Format(isoDate)
	}
	date, err := time.ParseInLocation(isoDate, raw, time.Local)
	if err != nil {
		badRequest(w, "date 格式应为 YYYY-MM-DD")
		return
	}

	days := queryInt(r.URL.Query().Get("days"), 30)
	if days < 7 {
		days = 7
	}
	if days > 90 {
		days = 90
	}
	topN := queryInt(r.URL.Query().Get("top"), 5)
	if topN < 1 {
		topN = 1
	}
	if topN > 20 {
		topN = 20
	}

	rows, err := s.repo.ListDailyRange(r.Context(), date.AddDate(0, 0, -(days-1)), date)
	if err != nil {
		internalError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, metric.BuildDashboard(rows, date, days, topN))
}

// yearlyMetrics GET /api/v1/metrics/yearly?year=2026&gender=gender=
func (s *Server) yearlyMetrics(w http.ResponseWriter, r *http.Request) {
	yearRaw := r.URL.Query().Get("year")
	if yearRaw == "" {
		yearRaw = strconv.Itoa(time.Now().Year())
	}
	year, err := strconv.Atoi(yearRaw)
	if err != nil || year < 2000 || year > 9999 {
		badRequest(w, "year 应为 4 位年份")
		return
	}
	gender := domain.Gender(r.URL.Query().Get("gender"))
	if gender != "" && !gender.Valid() {
		badRequest(w, "gender 只能是 male / female / unknown")
		return
	}

	rows, err := s.repo.ListYearlyByYear(r.Context(), year, gender)
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}
