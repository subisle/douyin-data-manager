// Package render 生成导出图片的 SVG。
//
// 规格逐条来自 615 分支的 electron/weixin-bot-report.js，
// 两套样式（classic 女队 / apple 男团）的配色、尺寸、列权重全部对齐。
// 改任何一个常量前，先确认是不是真的要偏离 615——那份图是运营认过的样子。
package render

import (
	"fmt"
	"strings"
)

// Style 导出样式。女队用 classic，男团用 apple（与 615 的 resolveReportStyle 一致）。
type Style string

const (
	StyleClassic Style = "classic"
	StyleApple   Style = "apple"
)

// ResolveStyle 按性别决定样式，external 非空则强制使用。
func ResolveStyle(gender string, external string) Style {
	if external == string(StyleClassic) || external == string(StyleApple) {
		return Style(external)
	}
	if gender == "female" {
		return StyleClassic
	}
	return StyleApple
}

// Row 导出图的一行。字段名对应 615 的 DailyReportRow。
type Row struct {
	Name            string
	NotLiveDays     int
	DailyWave       int64
	TotalWave       int64
	DurationMinutes int
	Tier            string
	IsLive          bool
	MasterName      string
}

// ColumnSet 控制哪些列可见，默认与 615 一致（时长、师傅、等级默认隐藏）。
type ColumnSet struct {
	Rank        bool
	Name        bool
	NotLiveDays bool
	DailyWave   bool
	TotalWave   bool
	Duration    bool
	Master      bool
	Tier        bool
}

// DefaultColumns 返回 615 的默认可见列：排名/姓名/未播天数/日音浪/累计总音浪。
func DefaultColumns() ColumnSet {
	return ColumnSet{Rank: true, Name: true, NotLiveDays: true, DailyWave: true, TotalWave: true}
}

// ParseColumnSet 解析 cols=rank,name,dailyWave,... 的任意列组合（615 字段勾选）。
// 键与 615 的 ColumnKey 一致；至少要有一个合法键。
func ParseColumnSet(raw string) (ColumnSet, error) {
	var set ColumnSet
	count := 0
	for _, part := range strings.Split(raw, ",") {
		switch strings.TrimSpace(strings.ToLower(part)) {
		case "rank":
			set.Rank = true
			count++
		case "name":
			set.Name = true
			count++
		case "notlivedays":
			set.NotLiveDays = true
			count++
		case "dailywave":
			set.DailyWave = true
			count++
		case "totalwave":
			set.TotalWave = true
			count++
		case "duration":
			set.Duration = true
			count++
		case "master":
			set.Master = true
			count++
		case "tier":
			set.Tier = true
			count++
		case "":
			// 忽略空段（末尾逗号）
		default:
			return ColumnSet{}, fmt.Errorf("未知列: %s", part)
		}
	}
	if count == 0 {
		return ColumnSet{}, fmt.Errorf("cols 不能为空")
	}
	return set, nil
}

// Report 一张导出图的全部输入。
type Report struct {
	Title  string
	Date   string // YYYY-MM-DD
	Gender string // male / female
	Rows   []Row  // 本页行
	// 统计口径用全量行；分页时 Rows 只是其中一页，但页脚要写全团人数。
	Stats              []Row
	Columns            ColumnSet
	PageIndex          int
	PageCount          int
	PageSize           int // 每页行数；分页时全局排名要靠它算，未传则按当前页行数估算
	InactiveLines      []string // 未播名单，已按师傅分组并折行
	ShowInactiveFooter bool
}

// rankBase 当前页第一行的排名偏移（0-based；渲染时 rank = rankBase + i + 1）。
// 旧公式 (PageIndex-1)*len(本页行数) 在最后一页不满页时算错：男团 90 人
// 分 50+40 两页，第 2 页排名会从 41 开始（应为 51），与第 1 页重叠错乱。
func (r Report) rankBase() int {
	if r.PageIndex <= 1 {
		return 0
	}
	perPage := r.PageSize
	if perPage <= 0 {
		perPage = len(r.Rows)
	}
	return (r.PageIndex - 1) * perPage
}

// RenderSVG 生成 SVG。
func RenderSVG(r Report, style Style) []byte {
	if style == StyleApple {
		return []byte(renderApple(r))
	}
	return []byte(renderClassic(r))
}

/* ---------------------------- 格式化（对齐 615） ---------------------------- */

// FormatWave 音浪展示：>=1亿用「亿」，>=1万用「万」（精确到 0.1），否则千分位。
func FormatWave(value int64) string {
	if value <= 0 {
		return "0"
	}
	if value >= 100_000_000 {
		yi := float64(value) / 100_000_000
		r := float64(int(yi*10+0.5)) / 10
		return trimNum(r) + " 亿"
	}
	if value < 10_000 {
		return groupDigits(value)
	}
	wan := float64(value) / 10_000
	r := float64(int(wan*10+0.5)) / 10
	if r <= 0 {
		return "0"
	}
	return trimNum(r) + " 万"
}

// FormatDuration 时长展示：X小时Y分 或 Y分钟。
func FormatDuration(minutes int) string {
	if minutes < 0 {
		minutes = 0
	}
	h := minutes / 60
	rest := minutes % 60
	if h > 0 {
		return fmt.Sprintf("%d小时%d分", h, rest)
	}
	return fmt.Sprintf("%d分钟", rest)
}

func trimNum(v float64) string {
	if v == float64(int(v)) {
		return fmt.Sprintf("%d", int(v))
	}
	return fmt.Sprintf("%.1f", v)
}

func groupDigits(v int64) string {
	s := fmt.Sprintf("%d", v)
	var out []string
	for len(s) > 3 {
		out = append([]string{s[len(s)-3:]}, out...)
		s = s[:len(s)-3]
	}
	out = append([]string{s}, out...)
	return strings.Join(out, ",")
}

/* ------------------------------- 通用图形 ------------------------------- */

func esc(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	return s
}

type textOpt struct {
	size    int
	fill    string
	weight  int
	anchor  string // start / middle / end
	family  string
	opacity string
}

func text(x, y float64, s string, o textOpt) string {
	anchor := o.anchor
	if anchor == "" {
		anchor = "start"
	}
	weight := o.weight
	if weight == 0 {
		weight = 400
	}
	size := o.size
	if size == 0 {
		size = 13
	}
	fill := o.fill
	if fill == "" {
		fill = "#0F172A"
	}
	family := o.family
	if family != "" {
		family = fmt.Sprintf(` font-family="%s"`, esc(family))
	}
	opacity := ""
	if o.opacity != "" {
		opacity = fmt.Sprintf(` opacity="%s"`, o.opacity)
	}
	return fmt.Sprintf(
		`<text x="%.1f" y="%.1f" font-size="%d" fill="%s" font-weight="%d" text-anchor="%s" dominant-baseline="central"%s%s>%s</text>`,
		x, y, size, fill, weight, anchor, family, opacity, esc(s),
	)
}

func rect(x, y, w, h float64, fill string, extra string) string {
	attrs := ""
	if extra != "" {
		attrs = " " + extra
	}
	return fmt.Sprintf(`<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" fill="%s"%s/>`, x, y, w, h, fill, attrs)
}

type col struct {
	key   string
	label string
	width float64
	align string
	x     float64
}

// layoutCols 把列权重归一化后铺满可用宽度（615 的做法）。
func layoutCols(cols []col, total float64) {
	sum := 0.0
	for _, c := range cols {
		sum += c.width
	}
	cursor := 0.0
	for i := range cols {
		cols[i].width = (cols[i].width / sum) * total
		cols[i].x = cursor
		cursor += cols[i].width
	}
}

func (r Report) notLiveDaysLabel() string {
	parts := strings.Split(r.Date, "-")
	m := "1"
	if len(parts) >= 2 {
		m = strings.TrimLeft(parts[1], "0")
		if m == "" {
			m = "1"
		}
	}
	return m + "月未播天数"
}

func (r Report) dailyWaveLabel() string {
	// 615 formatDailyWaveLabel：只有「日」——「19日音浪」
	parts := strings.Split(r.Date, "-")
	d := "1"
	if len(parts) >= 3 {
		d = strings.TrimLeft(parts[2], "0")
		if d == "" {
			d = "1"
		}
	}
	return d + "日音浪"
}

func (r Report) summary() (totalCount, notLiveCount, notLiveDays int) {
	src := r.Stats
	if len(src) == 0 {
		src = r.Rows
	}
	for _, row := range src {
		if !row.IsLive {
			notLiveCount++
			notLiveDays += row.NotLiveDays
		}
	}
	return len(src), notLiveCount, notLiveDays
}

func (r Report) title(style Style) string {
	// 2026-09-27：报告标题不带公会名。男团（原「星嗨艺创」）按运营要求改为「ST-001」，
	// 女队用中性标题；外部传 title 仍可覆盖。
	if strings.TrimSpace(r.Title) != "" {
		return strings.TrimSpace(r.Title)
	}
	if r.Gender == "male" {
		return "ST-001"
	}
	return "主播数据统计"
}

func (r Report) pageSuffix() string {
	if r.PageCount <= 1 {
		return ""
	}
	// 615 的 formatDailyReportPageSuffix：全角括号「（1/2）」
	return fmt.Sprintf("（%d/%d）", r.PageIndex, r.PageCount)
}

// genderText classic 页脚用「男 / 女」
func (r Report) genderText() string {
	if r.Gender == "female" {
		return "女"
	}
	return "男"
}

// genderGroupText apple 页脚用「男团 / 女队」
func (r Report) genderGroupText() string {
	if r.Gender == "female" {
		return "女队"
	}
	return "男团"
}
