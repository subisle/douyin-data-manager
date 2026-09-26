package render

import (
	"fmt"
	"math"
	"strings"
)

const (
	svgScale  = 2
	fontSans  = "-apple-system, BlinkMacSystemFont, SF Pro Display, SF Pro Text, PingFang SC, Hiragino Sans GB, Microsoft YaHei, sans-serif"
	fontMono  = "SF Mono, Menlo, Consolas, monospace"
	fontSerif = "Georgia, Times New Roman, serif"
	// apple 样式的主色，进度条填充与强调都用这个（取自 615 的 const blue）
	blue = "#007AFF"
	// 615 canvas 的默认输出倍率是 scale=2，逻辑宽度由内容实测决定
)

/* ═══════════════════ 文本宽度估算（替代 canvas measureText） ═══════════════════ */

// estimateTextWidth 近似 canvas measureText：CJK 全宽 1em，数字 0.6em，
// ASCII 字母 0.55em，其余 0.5em；粗体略宽 5%。只用于列宽分配，不参与绘制。
func estimateTextWidth(s string, px float64, bold bool) float64 {
	w := 0.0
	for _, r := range s {
		switch {
		case r >= 0x2E80: // CJK 及全宽区
			w += 1.0
		case r >= '0' && r <= '9':
			w += 0.6
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
			w += 0.55
		case r == ' ':
			w += 0.3
		default:
			w += 0.5
		}
	}
	if bold {
		w *= 1.05
	}
	return w * px
}

// truncateToWidth 对齐 615 的 truncateCanvasText：超宽就截断加 "...".
func truncateToWidth(s string, maxW float64, px float64, bold bool) string {
	if maxW <= 0 || estimateTextWidth(s, px, bold) <= maxW {
		return s
	}
	runes := []rune(s)
	for len(runes) > 0 && estimateTextWidth(string(runes)+"...", px, bold) > maxW {
		runes = runes[:len(runes)-1]
	}
	if len(runes) == 0 {
		return s
	}
	return string(runes) + "..."
}

/* ═══════════════════ 列定义（对齐 615 getColumnDefinitions） ═══════════════════ */

type colDef struct {
	key   string
	label string
	minW  float64
	flex  float64
	align string // left / center / right
}

// columnOrder 与 615 一致：排名 姓名 未播 日音浪 累计 时长 师傅 等级。
func columnDefs(profile, notLiveLabel, waveLabel string) []colDef {
	if profile == "apple" {
		return []colDef{
			{key: "rank", label: "排名", minW: 66, flex: 0.05, align: "center"},
			{key: "name", label: "主播姓名", minW: 88, flex: 0.04, align: "left"},
			{key: "notLiveDays", label: notLiveLabel, minW: 28, flex: 0, align: "center"},
			{key: "dailyWave", label: waveLabel, minW: 168, flex: 0.7, align: "right"},
			{key: "totalWave", label: "累计总音浪", minW: 120, flex: 0.4, align: "right"},
			{key: "duration", label: "当月时长", minW: 86, flex: 0.25, align: "center"},
			{key: "master", label: "师傅", minW: 92, flex: 0.4, align: "left"},
			{key: "tier", label: "等级", minW: 64, flex: 0.15, align: "center"},
		}
	}
	return []colDef{
		{key: "rank", label: "排名", minW: 66, flex: 0.08, align: "center"},
		{key: "name", label: "主播姓名", minW: 104, flex: 0.12, align: "left"},
		{key: "notLiveDays", label: notLiveLabel, minW: 30, flex: 0, align: "center"},
		{key: "dailyWave", label: waveLabel, minW: 160, flex: 0.55, align: "right"},
		{key: "totalWave", label: "累计总音浪", minW: 130, flex: 1.2, align: "right"},
		{key: "duration", label: "当月时长", minW: 94, flex: 0.7, align: "center"},
		{key: "master", label: "师傅", minW: 96, flex: 0.3, align: "left"},
		{key: "tier", label: "等级", minW: 72, flex: 0.35, align: "center"},
	}
}

func visibleDefs(set ColumnSet, profile, notLiveLabel, waveLabel string) []colDef {
	all := columnDefs(profile, notLiveLabel, waveLabel)
	out := []colDef{}
	for _, d := range all {
		visible := false
		switch d.key {
		case "rank":
			visible = set.Rank
		case "name":
			visible = set.Name
		case "notLiveDays":
			visible = set.NotLiveDays
		case "dailyWave":
			visible = set.DailyWave
		case "totalWave":
			visible = set.TotalWave
		case "duration":
			visible = set.Duration
		case "master":
			visible = set.Master
		case "tier":
			visible = set.Tier
		}
		if visible {
			out = append(out, d)
		}
	}
	if len(out) == 0 {
		out = []colDef{all[0]}
	}
	return out
}

// sampleText 列的样例文本，用于实测列宽（与 615 getText 一致的展示形态）。
func sampleText(key string, row Row) string {
	switch key {
	case "rank":
		return "01"
	case "name":
		return row.Name
	case "master":
		if strings.TrimSpace(row.MasterName) == "" {
			return "-"
		}
		return row.MasterName
	case "notLiveDays":
		return fmt.Sprint(row.NotLiveDays)
	case "dailyWave":
		if row.IsLive {
			return FormatWave(row.DailyWave)
		}
		return "未开播"
	case "totalWave":
		return FormatWave(row.TotalWave)
	case "duration":
		return shortDuration(row.DurationMinutes)
	case "tier":
		return row.Tier
	}
	return ""
}

// shortDuration 615 画布上的时长文案：X时Y分 / X时 / Y分 / —。
func shortDuration(minutes int) string {
	if minutes <= 0 {
		return "—"
	}
	h := minutes / 60
	m := minutes % 60
	if h > 0 {
		if m > 0 {
			return fmt.Sprintf("%d时%d分", h, m)
		}
		return fmt.Sprintf("%d时", h)
	}
	return fmt.Sprintf("%d分", m)
}

// naturalWidths 实测各列自然宽（615 measureNaturalColumnWidths）。
func naturalWidths(defs []colDef, rows []Row) []float64 {
	n := len(defs)
	widths := make([]float64, n)
	for i, d := range defs {
		headerW := estimateTextWidth(d.label, 14, true)
		sampleW := 0.0
		for _, r := range rows {
			if w := estimateTextWidth(sampleText(d.key, r), 16, false); w > sampleW {
				sampleW = w
			}
		}
		if d.key == "notLiveDays" {
			widths[i] = math.Max(d.minW, math.Max(headerW+10, sampleW+14))
		} else {
			widths[i] = math.Max(d.minW, math.Max(headerW+30, sampleW+36))
		}
	}
	return widths
}

// layoutColumns 对齐 615 buildColumns：实测自然宽 → flex 补足 / 超宽等比压缩。
// 返回带 x/width 的列。tableWidth 是表格可用宽度（已扣 padding）。
func layoutColumns(defs []colDef, rows []Row, tableWidth float64) []col {
	widths := naturalWidths(defs, rows)

	total := 0.0
	for _, w := range widths {
		total += w
	}
	switch {
	case total < tableWidth:
		flexSum := 0.0
		for _, d := range defs {
			flexSum += d.flex
		}
		if flexSum <= 0 {
			flexSum = 1
		}
		extra := tableWidth - total
		for i, d := range defs {
			widths[i] += extra * (d.flex / flexSum)
		}
	case total > tableWidth:
		ratio := tableWidth / total
		for i := range widths {
			widths[i] *= ratio
		}
	}

	cols := make([]col, len(defs))
	cursor := 0.0
	for i, d := range defs {
		cols[i] = col{key: d.key, label: d.label, align: d.align, width: widths[i], x: cursor}
		cursor += widths[i]
	}
	return cols
}

// headerTextX 表头对齐与数据值一致（615：名字左+8、师傅左+12、累计右-12、其余居中）。
func headerTextX(c col, px float64) float64 {
	switch c.key {
	case "name":
		return c.x + 8*px
	case "master":
		return c.x + 12*px
	case "totalWave":
		return c.x + c.width - 12*px
	}
	return c.x + c.width/2
}

func headerAnchor(c col) string {
	switch c.key {
	case "name", "master":
		return "start"
	case "totalWave":
		return "end"
	}
	return "middle"
}

func maxLiveWave(rows []Row) float64 {
	m := 1.0
	for _, r := range rows {
		if r.IsLive && float64(r.DailyWave) > m {
			m = float64(r.DailyWave)
		}
	}
	return m
}

// maxDuration 全量行当月时长最大值（615 分页时两页进度条比例一致）。
func maxDuration(rows []Row) float64 {
	m := 0.0
	for _, r := range rows {
		if float64(r.DurationMinutes) > m {
			m = float64(r.DurationMinutes)
		}
	}
	return m
}

/* ═══════════════════ classic（女队，浅色表格） ═══════════════════ */

func renderClassic(r Report) string {
	const (
		headerHeight      = 54.0
		tableHeaderHeight = 32.0
		rowHeight         = 38.0
		tablePaddingX     = 20.0
	)

	// 容器宽：615 用标题宽/列自然宽取大后夹在 520~1800
	defs := visibleDefs(r.Columns, "classic", r.notLiveDaysLabel(), r.dailyWaveLabel())
	titleText := strings.TrimSpace(r.title(StyleClassic) + " " + classicDate(r.Date) + " " + r.pageSuffix())
	titleW := estimateTextWidth(titleText, 22, true)
	naturalW := 0.0
	for _, w := range naturalWidths(defs, r.Rows) {
		naturalW += w
	}
	containerW := math.Max(520, math.Min(1800, math.Max(titleW+100, naturalW+tablePaddingX*2)))
	tableWidth := containerW - tablePaddingX*2
	cols := layoutColumns(defs, r.Rows, tableWidth)
	for i := range cols {
		cols[i].x += tablePaddingX
	}

	hasInactive := r.ShowInactiveFooter && len(r.InactiveLines) > 0
	footerHeight := 64.0
	if hasInactive {
		footerHeight = math.Max(118, 86+float64(len(r.InactiveLines))*18)
	}
	heightLogical := headerHeight + tableHeaderHeight + rowHeight*float64(len(r.Rows)) + footerHeight
	logicalW := containerW

	var b strings.Builder
	b.WriteString(fmt.Sprintf(
		`<svg xmlns="http://www.w3.org/2000/svg" width="%.0f" height="%.0f" viewBox="0 0 %.0f %.0f" role="img">`,
		logicalW*svgScale, heightLogical*svgScale, logicalW, heightLogical))
	b.WriteString(rect(0, 0, logicalW, heightLogical, "#F8FAFC", ""))

	// 标题栏
	b.WriteString(rect(0, 0, logicalW, headerHeight, "#1E293B", ""))
	b.WriteString(text(logicalW/2, headerHeight/2+1, titleText, textOpt{
		size: 22, fill: "#F8FAFC", weight: 700, anchor: "middle",
	}))

	// 表头
	y := headerHeight
	b.WriteString(rect(0, y, logicalW, tableHeaderHeight, "#E2E8F0", ""))
	for _, c := range cols {
		b.WriteString(text(headerTextX(c, 1), y+tableHeaderHeight/2+1, c.label, textOpt{
			size: 13, fill: "#475569", weight: 700, anchor: headerAnchor(c),
		}))
	}
	y += tableHeaderHeight

	maxWave := maxLiveWave(r.Rows)
	rankOffset := 0
	if r.PageIndex > 1 && len(r.Rows) > 0 {
		rankOffset = (r.PageIndex - 1) * len(r.Rows)
	}

	for i, row := range r.Rows {
		rank := rankOffset + i + 1
		isTop3 := rank <= 3
		fill := "#FFFFFF"
		switch {
		case !row.IsLive:
			fill = "#FEF2F2"
		case rank == 1:
			fill = "#FEF3C7"
		case rank == 2:
			fill = "#F8FAFC"
		case rank == 3:
			fill = "#FFEDD5"
		case i%2 == 1:
			fill = "#F8FAFC"
		}
		b.WriteString(rect(0, y, logicalW, rowHeight, fill, ""))
		if !row.IsLive {
			b.WriteString(rect(0, y, 4, rowHeight, "#DC2626", ""))
		}
		lineColor := "#E2E8F0"
		if !row.IsLive {
			lineColor = "#FECACA"
		}
		b.WriteString(fmt.Sprintf(`<line x1="0" y1="%.1f" x2="%.1f" y2="%.1f" stroke="%s" stroke-width="0.5"/>`,
			y, logicalW, y, lineColor))

		cy := y + rowHeight/2 + 1
		for _, c := range cols {
			switch c.key {
			case "rank":
				cx := c.x + c.width/2
				fillTxt := "#334155"
				if !row.IsLive {
					fillTxt = "#B91C1C"
				}
				if isTop3 {
					medal := "🥇"
					if rank == 2 {
						medal = "🥈"
					} else if rank == 3 {
						medal = "🥉"
					}
					b.WriteString(text(cx, cy, medal, textOpt{size: 20, fill: fillTxt, weight: 700, anchor: "middle"}))
				} else {
					b.WriteString(fmt.Sprintf(`<text x="%.1f" y="%.1f" font-size="15" fill="%s" font-weight="700" text-anchor="middle" dominant-baseline="central" font-family="%s" font-style="italic">%s</text>`,
						cx, cy, fillTxt, esc(fontSerif), esc(fmt.Sprintf("%02d", rank))))
				}
			case "name":
				fillTxt := "#0F172A"
				w := 500
				if !row.IsLive {
					fillTxt = "#991B1B"
					w = 700
				}
				b.WriteString(text(c.x+8, cy, truncateToWidth(row.Name, c.width-16, 15, false), textOpt{
					size: 15, fill: fillTxt, weight: w, anchor: "start",
				}))
			case "master":
				master := row.MasterName
				if strings.TrimSpace(master) == "" {
					master = "—"
				}
				b.WriteString(text(c.x+12, cy, truncateToWidth(master, c.width-24, 13, false), textOpt{
					size: 13, fill: "#64748B", weight: 500, anchor: "start",
				}))
			case "notLiveDays":
				fillTxt := "#15803D"
				if row.NotLiveDays > 0 {
					fillTxt = "#B91C1C"
				}
				b.WriteString(text(c.x+c.width/2, cy, fmt.Sprint(row.NotLiveDays), textOpt{
					size: 13, fill: fillTxt, weight: 700, anchor: "middle",
				}))
			case "dailyWave":
				b.WriteString(classicWaveBar(c, cy, row, maxWave))
			case "totalWave":
				b.WriteString(text(c.x+c.width-12, cy, FormatWave(row.TotalWave), textOpt{
					size: 14, fill: "#475569", weight: 500, anchor: "end", family: fontMono,
				}))
			case "duration":
				b.WriteString(classicDurationBar(c, cy, row, maxDuration(r.Stats)))
			case "tier":
				if row.Tier != "" {
					b.WriteString(classicTierPill(c, cy, row))
				}
			}
		}
		y += rowHeight
	}

	// 页脚
	b.WriteString(rect(0, y, logicalW, footerHeight, "#E2E8F0", ""))
	totalCount, notLiveCount, notLiveDays := r.summary()
	pageCountLabel := fmt.Sprintf("%s主播 %d 人", r.genderText(), totalCount)
	if r.PageCount > 1 {
		pageCountLabel = fmt.Sprintf("本页 %d 人 · 共 %d 人", len(r.Rows), totalCount)
	}
	summaryText := fmt.Sprintf("%s · 未开播人数 %d 人 · 未开播天数 %d 天", pageCountLabel, notLiveCount, notLiveDays)
	b.WriteString(text(tablePaddingX, y+26, truncateToWidth(summaryText, logicalW-tablePaddingX*2, 18, true), textOpt{size: 18, fill: "#334155", weight: 700}))
	b.WriteString(text(tablePaddingX, y+50, "数据日期 "+classicDate(r.Date), textOpt{
		size: 12, fill: "#64748B", weight: 500,
	}))

	if hasInactive {
		warnX := tablePaddingX
		warnY := y + 64
		warnW := logicalW - tablePaddingX*2
		warnH := footerHeight - 78
		b.WriteString(rect(warnX, warnY, warnW, warnH, "#FEF2F2", `rx="8" stroke="#FECACA" stroke-width="1"`))
		// 615：提示框内有粗体标题行，未播名单从 +40 起每行 16
		b.WriteString(text(warnX+14, warnY+22,
			fmt.Sprintf("未开播人数 %d 人 · 未开播天数 %d 天", notLiveCount, notLiveDays),
			textOpt{size: 14, fill: "#DC2626", weight: 700}))
		for i, line := range r.InactiveLines {
			ly := warnY + 40 + float64(i)*16
			if ly > warnY+warnH {
				break
			}
			b.WriteString(text(warnX+14, ly, line, textOpt{size: 11, fill: "#7F1D1D", weight: 500}))
		}
	}

	b.WriteString("</svg>")
	return b.String()
}

// classicWaveBar 日音浪进度条。填充超过轨道 52% 时数字转白，否则深蓝。
func classicWaveBar(c col, cy float64, row Row, maxWave float64) string {
	const padX = 8.0
	const barH = 22.0
	barLeft := c.x + padX
	barTrackW := math.Max(48, c.width-padX*2)
	barTop := cy - barH/2

	var b strings.Builder
	if !row.IsLive {
		b.WriteString(rect(barLeft, barTop, barTrackW, barH, "#FEE2E2", fmt.Sprintf(`rx="%.1f"`, barH/2)))
		b.WriteString(text(barLeft+barTrackW/2, cy, "未开播", textOpt{
			size: 13, fill: "#DC2626", weight: 700, anchor: "middle",
		}))
		return b.String()
	}

	fillW := math.Max(0, math.Min(barTrackW, barTrackW*float64(row.DailyWave)/maxWave))
	b.WriteString(rect(barLeft, barTop, barTrackW, barH, "#DBEAFE", fmt.Sprintf(`rx="%.1f"`, barH/2)))
	if fillW > 0 {
		drawn := math.Max(fillW, barH)
		b.WriteString(rect(barLeft, barTop, drawn, barH, "#60A5FA", fmt.Sprintf(`rx="%.1f"`, barH/2)))
	}
	textFill := "#1E3A8A"
	if fillW/barTrackW > 0.52 {
		textFill = "#FFFFFF"
	}
	b.WriteString(text(barLeft+barTrackW/2, cy,
		truncateToWidth(FormatWave(row.DailyWave), barTrackW-12, 13, true), textOpt{
			size: 13, fill: textFill, weight: 700, anchor: "middle", family: fontMono,
		}))
	return b.String()
}

// classicDurationBar 紫色进度条（615 classic 的 duration 列）。
func classicDurationBar(c col, cy float64, row Row, maxDur float64) string {
	const padX = 8.0
	const barH = 22.0
	barLeft := c.x + padX
	barTrackW := math.Max(48, c.width-padX*2)
	barTop := cy - barH/2
	dur := row.DurationMinutes

	var b strings.Builder
	if dur <= 0 {
		b.WriteString(rect(barLeft, barTop, barTrackW, barH, "#F1F5F9", fmt.Sprintf(`rx="%.1f"`, barH/2)))
		b.WriteString(text(barLeft+barTrackW/2, cy, "—", textOpt{
			size: 13, fill: "#94A3B8", weight: 700, anchor: "middle",
		}))
		return b.String()
	}
	fillW := math.Max(0, math.Min(barTrackW, barTrackW*float64(dur)/math.Max(maxDur, 1)))
	b.WriteString(rect(barLeft, barTop, barTrackW, barH, "#EDE9FE", fmt.Sprintf(`rx="%.1f"`, barH/2)))
	if fillW > 0 {
		b.WriteString(rect(barLeft, barTop, math.Max(fillW, barH), barH, "#A78BFA", fmt.Sprintf(`rx="%.1f"`, barH/2)))
	}
	textFill := "#5B21B6"
	if fillW/barTrackW > 0.52 {
		textFill = "#FFFFFF"
	}
	b.WriteString(text(barLeft+barTrackW/2, cy,
		truncateToWidth(shortDuration(dur), barTrackW-12, 13, true), textOpt{
			size: 13, fill: textFill, weight: 700, anchor: "middle", family: fontMono,
		}))
	return b.String()
}

func classicTierPill(c col, cy float64, row Row) string {
	bw := math.Min(c.width-20, 66)
	bh := 20.0
	bl := c.x + (c.width-bw)/2
	bt := cy - bh/2
	bg := "#E0F2FE"
	fg := "#0369A1"
	if !row.IsLive {
		bg = "#FEE2E2"
		fg = "#B91C1C"
	}
	var b strings.Builder
	b.WriteString(rect(bl, bt, bw, bh, bg, `rx="8"`))
	b.WriteString(text(c.x+c.width/2, cy+1, row.Tier, textOpt{size: 11, fill: fg, weight: 700, anchor: "middle"}))
	return b.String()
}

func classicDate(date string) string {
	parts := strings.Split(date, "-")
	y, m, d := "2026", "1", "1"
	if len(parts) >= 1 && parts[0] != "" {
		y = parts[0]
	}
	if len(parts) >= 2 {
		m = strings.TrimLeft(parts[1], "0")
		if m == "" {
			m = "1"
		}
	}
	if len(parts) >= 3 {
		d = strings.TrimLeft(parts[2], "0")
		if d == "" {
			d = "1"
		}
	}
	return fmt.Sprintf("%s-%02s-%02s", y, m, d)
}

/* ═══════════════════ apple（男团，浅色报告） ═══════════════════ */

func renderApple(r Report) string {
	const (
		headerH       = 86.0
		tableHeaderH  = 34.0
		rowH          = 48.0
		rowGap        = 6.0
		footerTextGap = 8.0
		footerTextH   = 18.0
		warnGap       = 20.0
		warnH         = 42.0
	)

	hasInactive := r.ShowInactiveFooter && len(r.InactiveLines) > 0
	footerH := footerTextGap + footerTextH
	if hasInactive {
		footerH += warnGap + warnH
	}
	tableRowsH := rowH*float64(len(r.Rows)) + math.Max(0, float64(len(r.Rows)-1))*rowGap

	defs := visibleDefs(r.Columns, "apple", r.notLiveDaysLabel(), r.dailyWaveLabel())
	// 容器宽：615 = 实测列宽 + 列数呼吸空间，夹在 560~2200
	naturalW := 0.0
	for _, w := range naturalWidths(defs, r.Rows) {
		naturalW += w
	}
	breathing := math.Max(0, float64(len(defs)-5)) * 18
	logicalW := math.Max(560, math.Min(2200, naturalW+breathing))
	cols := layoutColumns(defs, r.Rows, logicalW)
	denseColumns := len(defs) >= 7

	heightLogical := headerH + tableHeaderH + rowGap + tableRowsH + footerH

	var b strings.Builder
	b.WriteString(fmt.Sprintf(
		`<svg xmlns="http://www.w3.org/2000/svg" width="%.0f" height="%.0f" viewBox="0 0 %.0f %.0f" role="img">`,
		logicalW*svgScale, heightLogical*svgScale, logicalW, heightLogical))
	b.WriteString(rect(0, 0, logicalW, heightLogical, "#FFFFFF", ""))

	// 斜向水印网格（-18°，与 615 的 -PI/10 一致）
	cx, cyW := logicalW/2, heightLogical/2
	for wy := -heightLogical; wy <= heightLogical; wy += 76 {
		for wx := -logicalW; wx <= logicalW; wx += 180 {
			b.WriteString(fmt.Sprintf(
				`<text x="%.0f" y="%.0f" font-family="%s" font-size="18" font-weight="900" fill="#334155" fill-opacity="0.055" text-anchor="middle" dominant-baseline="central" transform="rotate(-18 %.0f %.0f)">内部数据 · 请勿外传</text>`,
				cx+wx, cyW+wy, esc(fontSans), cx, cyW))
		}
	}

	// 标题区（615：top 基线 y+10 / y+44 ≈ central 基线 y+22 / y+50）
	titleBase := r.title(StyleApple)
	if s := r.pageSuffix(); s != "" {
		titleBase = titleBase + " " + s
	}
	b.WriteString(text(logicalW/2, 22, truncateToWidth(titleBase, logicalW-24, 25, true), textOpt{
		size: 25, fill: "#101828", weight: 700, anchor: "middle", family: fontSans,
	}))
	b.WriteString(text(logicalW/2, 50, appleDate(r.Date), textOpt{
		size: 13, fill: "#475467", weight: 500, anchor: "middle", family: fontSans,
	}))
	y := headerH

	// 表头
	b.WriteString(rect(0, y, logicalW, tableHeaderH, "#F2F4F7", ""))
	for _, c := range cols {
		b.WriteString(text(headerTextX(c, 1), y+tableHeaderH/2+1, c.label, textOpt{
			size: 12, fill: "#667085", weight: 700, anchor: headerAnchor(c), family: fontSans,
		}))
	}
	y += tableHeaderH + rowGap

	maxWave := maxLiveWave(r.Rows)
	maxDur := maxDuration(r.Stats)
	rankOffset := 0
	if r.PageIndex > 1 && len(r.Rows) > 0 {
		rankOffset = (r.PageIndex - 1) * len(r.Rows)
	}

	for i, row := range r.Rows {
		rank := rankOffset + i + 1
		rowFill := "#FFFFFF"
		stroke := "#EAECF0"
		if !row.IsLive {
			rowFill = "#FFF7F7"
			stroke = "#FEE4E2"
		}
		b.WriteString(rect(0, y, logicalW, rowH, rowFill, fmt.Sprintf(`stroke="%s" stroke-width="1"`, stroke)))
		if !row.IsLive {
			b.WriteString(rect(0, y+8, 4, rowH-16, "#F04438", `rx="2"`))
		}

		cy := y + rowH/2 + 1
		for _, c := range cols {
			switch c.key {
			case "rank":
				b.WriteString(appleRankChip(c, cy, rank, !row.IsLive))
			case "name":
				fillTxt := "#101828"
				if !row.IsLive {
					fillTxt = "#B42318"
				}
				b.WriteString(text(c.x+8, cy, truncateToWidth(row.Name, c.width-16, 14, true), textOpt{
					size: 14, fill: fillTxt, weight: 700, anchor: "start", family: fontSans,
				}))
			case "master":
				master := row.MasterName
				if strings.TrimSpace(master) == "" {
					master = "-"
				}
				b.WriteString(text(c.x+12, cy, truncateToWidth(master, c.width-24, 13, false), textOpt{
					size: 13, fill: "#667085", weight: 500, anchor: "start", family: fontSans,
				}))
			case "notLiveDays":
				fillTxt := "#027A48"
				if row.NotLiveDays > 0 {
					fillTxt = "#B42318"
				}
				b.WriteString(text(c.x+c.width/2, cy, fmt.Sprint(row.NotLiveDays), textOpt{
					size: 13, fill: fillTxt, weight: 700, anchor: "middle", family: fontSans,
				}))
			case "dailyWave":
				barH := 22.0
				if denseColumns {
					barH = 20.0
				}
				b.WriteString(appleWaveBar(c, cy, row, maxWave, barH))
			case "totalWave":
				b.WriteString(text(c.x+c.width-12, cy, FormatWave(row.TotalWave), textOpt{
					size: 13, fill: "#475467", weight: 600, anchor: "end", family: fontMono,
				}))
			case "duration":
				barH := 22.0
				if denseColumns {
					barH = 20.0
				}
				b.WriteString(appleDurationBar(c, cy, row, maxDur, barH))
			case "tier":
				if row.Tier != "" {
					b.WriteString(appleTierPill(c, cy, row))
				}
			}
		}
		// 615 只在行与行之间加间距，最后一行后面不加
		y += rowH
		if i != len(r.Rows)-1 {
			y += rowGap
		}
	}

	// 页脚：左「数据日期 · 人数」，右「未开播」，中间「内部数据 · 请勿外传」
	totalCount, notLiveCount, notLiveDays := r.summary()
	footerY := y + footerTextGap
	peopleLabel := fmt.Sprintf("%s %d 人", r.genderGroupText(), totalCount)
	if r.PageCount > 1 {
		peopleLabel = fmt.Sprintf("本页 %d/%d 人", len(r.Rows), totalCount)
	}
	b.WriteString(text(8, footerY+4, fmt.Sprintf("数据日期 %s · %s", appleDate(r.Date), peopleLabel), textOpt{
		size: 12, fill: "#667085", weight: 600, anchor: "start", family: fontSans,
	}))
	b.WriteString(text(logicalW-8, footerY+4,
		fmt.Sprintf("未开播人数 %d 人 · 未开播天数 %d 天", notLiveCount, notLiveDays), textOpt{
			size: 12, fill: "#667085", weight: 600, anchor: "end", family: fontSans,
		}))
	b.WriteString(text(logicalW/2, footerY+4, "内部数据 · 请勿外传", textOpt{
		size: 12, fill: "#98A2B3", weight: 700, anchor: "middle", family: fontSans,
	}))

	if hasInactive {
		warnY := footerY + footerTextH + warnGap
		b.WriteString(rect(0, warnY, logicalW, warnH, "#FFF7F7", `stroke="#FEE4E2" stroke-width="1"`))
		names := make([]string, 0, notLiveCount)
		src := r.Stats
		if len(src) == 0 {
			src = r.Rows
		}
		for _, row := range src {
			if !row.IsLive {
				names = append(names, row.Name)
			}
		}
		b.WriteString(text(14, warnY+warnH/2,
			truncateToWidth("未开播："+strings.Join(names, "、"), logicalW-28, 12, true), textOpt{
				size: 12, fill: "#B42318", weight: 700, anchor: "start", family: fontSans,
			}))
	}

	b.WriteString("</svg>")
	return b.String()
}

func appleRankChip(c col, cy float64, rank int, inactive bool) string {
	const chipW, chipH = 40.0, 24.0
	chipX := c.x + (c.width-chipW)/2
	chipY := cy - chipH/2

	chipFill, chipStroke, chipText := "#F2F4F7", "#EAECF0", "#475467"
	switch rank {
	case 1:
		chipFill, chipStroke, chipText = "#FFFAEB", "#FEDF89", "#B54708"
	case 2:
		chipFill, chipStroke, chipText = "#F9FAFB", "#D0D5DD", "#475467"
	case 3:
		chipFill, chipStroke, chipText = "#FFF6ED", "#FED7AA", "#C4320A"
	}
	if inactive {
		chipFill, chipStroke, chipText = "#FFF1F2", "#FFE4E6", "#B42318"
	}

	var b strings.Builder
	b.WriteString(rect(chipX, chipY, chipW, chipH, chipFill, fmt.Sprintf(`rx="%.1f" stroke="%s" stroke-width="1"`, chipH/2, chipStroke)))
	b.WriteString(text(chipX+chipW/2, cy, fmt.Sprintf("%02d", rank), textOpt{
		size: 12, fill: chipText, weight: 700, anchor: "middle", family: fontMono,
	}))
	return b.String()
}

func appleWaveBar(c col, cy float64, row Row, maxWave, barH float64) string {
	const padX = 8.0
	barX := c.x + padX
	barW := math.Max(48, c.width-padX*2)
	barY := cy - barH/2

	var b strings.Builder
	if !row.IsLive {
		b.WriteString(rect(barX, barY, barW, barH, "#FEE4E2", fmt.Sprintf(`rx="%.1f"`, barH/2)))
		b.WriteString(text(barX+barW/2, cy, "未开播", textOpt{
			size: 13, fill: "#D92D20", weight: 700, anchor: "middle", family: fontSans,
		}))
		return b.String()
	}

	fillW := math.Max(0, math.Min(barW, float64(row.DailyWave)/maxWave*barW))
	b.WriteString(rect(barX, barY, barW, barH, "#EAF3FF", fmt.Sprintf(`rx="%.1f" stroke="#D6E8FF" stroke-width="1"`, barH/2)))
	if fillW > 0 {
		b.WriteString(rect(barX, barY, math.Max(fillW, barH), barH, blue, fmt.Sprintf(`rx="%.1f"`, barH/2)))
	}
	textFill := "#1D4ED8"
	if fillW/barW > 0.52 {
		textFill = "#FFFFFF"
	}
	b.WriteString(text(barX+barW/2, cy,
		truncateToWidth(FormatWave(row.DailyWave), barW-12, 12, true), textOpt{
			size: 12, fill: textFill, weight: 800, anchor: "middle", family: fontMono,
		}))
	return b.String()
}

// appleDurationBar 紫色进度条（615 apple：track #F3F0FF / border #E5E0FF / fill #8B5CF6）。
func appleDurationBar(c col, cy float64, row Row, maxDur, barH float64) string {
	const padX = 8.0
	barX := c.x + padX
	barW := math.Max(48, c.width-padX*2)
	barY := cy - barH/2
	dur := row.DurationMinutes

	var b strings.Builder
	if dur <= 0 {
		b.WriteString(rect(barX, barY, barW, barH, "#F1F1F3", fmt.Sprintf(`rx="%.1f" stroke="#E4E4E7" stroke-width="1"`, barH/2)))
		b.WriteString(text(barX+barW/2, cy, "-", textOpt{
			size: 12, fill: "#1D4ED8", weight: 800, anchor: "middle", family: fontMono,
		}))
		return b.String()
	}
	fillW := math.Max(0, math.Min(barW, barW*float64(dur)/math.Max(maxDur, 1)))
	b.WriteString(rect(barX, barY, barW, barH, "#F3F0FF", fmt.Sprintf(`rx="%.1f" stroke="#E5E0FF" stroke-width="1"`, barH/2)))
	if fillW > 0 {
		b.WriteString(rect(barX, barY, math.Max(fillW, barH), barH, "#8B5CF6", fmt.Sprintf(`rx="%.1f"`, barH/2)))
	}
	textFill := "#1D4ED8"
	if fillW/barW > 0.52 {
		textFill = "#FFFFFF"
	}
	b.WriteString(text(barX+barW/2, cy,
		truncateToWidth(shortDuration(dur), barW-12, 12, true), textOpt{
			size: 12, fill: textFill, weight: 800, anchor: "middle", family: fontMono,
		}))
	return b.String()
}

func appleTierPill(c col, cy float64, row Row) string {
	tier := row.Tier
	bw := math.Min(c.width-18, math.Max(42, estimateTextWidth(tier, 11, true)+24))
	bh := 24.0
	bx := c.x + (c.width-bw)/2
	by := cy - bh/2
	colors := appleTierColor(row.Tier)

	var b strings.Builder
	b.WriteString(rect(bx, by, bw, bh, colors.bg, fmt.Sprintf(`rx="%.1f" stroke="%s" stroke-width="1"`, bh/2, colors.border)))
	b.WriteString(text(c.x+c.width/2, cy+0.5, tier, textOpt{
		size: 11, fill: colors.text, weight: 700, anchor: "middle", family: fontSans,
	}))
	return b.String()
}

type tierColor struct{ bg, border, text string }

func appleTierColor(tier string) tierColor {
	switch strings.ToUpper(tier) {
	case "A":
		return tierColor{"#FFF7E6", "#FDBA74", "#9A3412"}
	case "B":
		return tierColor{"#EAF3FF", "#60A5FA", "#1D4ED8"}
	case "C":
		return tierColor{"#ECFDF3", "#34D399", "#047857"}
	case "D":
		return tierColor{"#F5F3FF", "#A78BFA", "#6D28D9"}
	}
	return tierColor{"#F2F4F7", "#EAECF0", "#667085"}
}

func appleDate(date string) string {
	parts := strings.Split(date, "-")
	y, m, d := "2026", "1", "1"
	if len(parts) >= 1 && parts[0] != "" {
		y = parts[0]
	}
	if len(parts) >= 2 {
		m = strings.TrimLeft(parts[1], "0")
		if m == "" {
			m = "1"
		}
	}
	if len(parts) >= 3 {
		d = strings.TrimLeft(parts[2], "0")
		if d == "" {
			d = "1"
		}
	}
	return fmt.Sprintf("%s年%s月%s日", y, m, d)
}
