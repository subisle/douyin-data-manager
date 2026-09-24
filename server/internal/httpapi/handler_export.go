package httpapi

import (
	"math"
	"net/http"
	"strings"
	"time"

	"douyin-server/internal/domain"
	"douyin-server/internal/render"
)

// exportReport GET /api/v1/exports/report.svg
//
// 参数：date=YYYY-MM-DD & gender=male|female & style=auto|classic|apple
//
//	& page=1 & pageSize=30 & duration=0|1 & master=0|1 & tier=0|1
//
// 返回 615 同等样式的 SVG。之所以先出 SVG 而不是 PNG：
// SVG 是矢量、可无损缩放，而且前端能用浏览器渲染成 PNG（那时字体与 emoji 一定正确）。
// 需要服务端直接出 PNG 时，再挂渲染器（见设计文档 7.1）。
func (s *Server) exportReport(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("date")
	if raw == "" {
		raw = time.Now().Format(isoDate)
	}
	date, err := time.ParseInLocation(isoDate, raw, time.Local)
	if err != nil {
		badRequest(w, "date 格式应为 YYYY-MM-DD")
		return
	}

	gender := normalizeGender(w, r.URL.Query().Get("gender"))
	if gender == "" && r.URL.Query().Get("gender") != "" {
		return // normalizeGender 已经写了错误响应
	}

	// 50 行/页：男团 90+ 人刚好两张，女团一张——运营反馈 30/页切太碎。
	pageSize := queryInt(r.URL.Query().Get("pageSize"), 50)
	if pageSize < 1 {
		pageSize = 50
	}
	if pageSize > 100 {
		pageSize = 100
	}
	page := queryInt(r.URL.Query().Get("page"), 1)
	if page < 1 {
		page = 1
	}

	rows, err := s.repo.ListDailyByDate(r.Context(), date, gender)
	if err != nil {
		internalError(w, err)
		return
	}

	// 未播天数取当月口径（615 的列头就是「X月未播天数」）
	absent := map[uint64]int{}
	period := date.Format("2006-01")
	if monthly, err := s.repo.ListMonthlyByPeriod(r.Context(), period, gender); err == nil {
		for _, m := range monthly {
			absent[m.PersonID] = m.AbsentDays
		}
	}

	all := make([]render.Row, 0, len(rows))
	for _, d := range rows {
		tier := ""
		if d.Tier != nil {
			tier = *d.Tier
		}
		master := ""
		if d.MasterName != nil {
			master = *d.MasterName
		}
		all = append(all, render.Row{
			Name:            d.Name,
			NotLiveDays:     absent[d.PersonID],
			DailyWave:       d.Wave,
			TotalWave:       d.CumulativeWave,
			DurationMinutes: d.Minutes,
			Tier:            tier,
			IsLive:          d.IsLive,
			MasterName:      master,
		})
	}

	// 分页：超过一页时按页切，页脚会写「本页 N/M 人」
	pageCount := int(math.Max(1, math.Ceil(float64(len(all))/float64(pageSize))))
	if page > pageCount {
		page = pageCount
	}
	start := (page - 1) * pageSize
	end := int(math.Min(float64(len(all)), float64(start+pageSize)))
	pageRows := all[start:end]

	cols := render.DefaultColumns()
	if rawCols := r.URL.Query().Get("cols"); rawCols != "" {
		// cols=rank,name,dailyWave,... 任意列组合（615 的字段勾选）
		set, err := render.ParseColumnSet(rawCols)
		if err != nil {
			badRequest(w, err.Error())
			return
		}
		cols = set
	} else {
		if r.URL.Query().Get("duration") == "1" {
			cols.Duration = true
		}
		if r.URL.Query().Get("master") == "1" {
			cols.Master = true
		}
		if r.URL.Query().Get("tier") == "1" {
			cols.Tier = true
		}
	}

	inactive := []render.Row{}
	for _, row := range all {
		if !row.IsLive {
			inactive = append(inactive, row)
		}
	}

	report := render.Report{
		Title:              strings.TrimSpace(r.URL.Query().Get("title")),
		Date:               date.Format(isoDate),
		Gender:             string(gender),
		Rows:               pageRows,
		Stats:              all,
		Columns:            cols,
		PageIndex:          page,
		PageCount:          pageCount,
		InactiveLines:      wrapLines(groupInactive(inactive), 58),
		ShowInactiveFooter: pageCount <= 1 || page >= pageCount,
	}

	style := render.ResolveStyle(string(gender), r.URL.Query().Get("style"))

	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	w.Header().Set("Content-Disposition",
		`inline; filename="report-`+date.Format(isoDate)+`-`+string(gender)+`.svg"`)
	_, _ = w.Write(render.RenderSVG(report, style))
}

// normalizeGender 校验并转换性别参数。空值返回空（表示全团）。
func normalizeGender(w http.ResponseWriter, raw string) domain.Gender {
	switch raw {
	case "":
		return ""
	case "male", "female":
		return domain.Gender(raw)
	default:
		badRequest(w, "gender 只能是 male 或 female")
		return ""
	}
}

// groupInactive 按师傅分组未播名单，格式与 615 一致：
// 「师傅: 甲、乙  /  无师傅: 丙」
func groupInactive(rows []render.Row) string {
	groups := map[string][]string{}
	order := []string{}
	for _, r := range rows {
		master := strings.TrimSpace(r.MasterName)
		if master == "" {
			master = "无师傅"
		}
		if _, ok := groups[master]; !ok {
			order = append(order, master)
		}
		groups[master] = append(groups[master], r.Name)
	}
	parts := make([]string, 0, len(order))
	for _, m := range order {
		parts = append(parts, m+": "+strings.Join(groups[m], "、"))
	}
	return strings.Join(parts, "  /  ")
}

// wrapLines 按字符数折行，页脚的未播名单块用（615 的经典值：58 字一行）。
func wrapLines(s string, width int) []string {
	if s == "" {
		return nil
	}
	runes := []rune(s)
	lines := []string{}
	for len(runes) > width {
		lines = append(lines, string(runes[:width]))
		runes = runes[width:]
	}
	if len(runes) > 0 {
		lines = append(lines, string(runes))
	}
	return lines
}
