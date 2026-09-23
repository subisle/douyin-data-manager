// PNG 渲染：把 styles.go 里的 SVG 版式用纯 Go 画出来，给机器人发图用。
//
// 为什么不用 SVG 直接发：微信/QQ 的图片消息只认 PNG/JPEG 位图；615 是用
// sharp(librsvg) 在桌面端转的，盒子是 scratch + CGO_ENABLED=0 的静态镜像，
// 只能纯 Go 自己画。列宽分配、进度条几何、配色全部复用 styles.go 的布局
// 函数，保证和网页导出的 SVG 版式一致。
//
// 已知差异：内置字体只有 AlibabaPuHuiTi Heavy 一款（无衬线/斜体/emoji），
// 所以 classic 前三名的 🥇🥈🥉 用金银铜圆底白字代替，apple 水印网格为
// 横向（原版 -18° 旋转）。版式尺寸、颜色、文案逐项对齐 615。
package render

import (
	"bytes"
	_ "embed"
	"fmt"
	"image/color"
	"math"
	"strconv"
	"strings"
	"sync"

	"github.com/fogleman/gg"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
)

//go:embed assets/AlibabaPuHuiTi-3-105-Heavy.ttf
var reportFontTTF []byte

var (
	fontOnce     sync.Once
	parsedFont   *opentype.Font
	fontParseErr error
)

// pngScale 输出倍率，与 SVG 的 svgScale 一致（615 canvas scale=2）。
const pngScale = 2.0

func parsedReportFont() (*opentype.Font, error) {
	fontOnce.Do(func() {
		parsedFont, fontParseErr = opentype.Parse(reportFontTTF)
	})
	return parsedFont, fontParseErr
}

// RenderPNG 生成 PNG。版式与 RenderSVG 同源，机器人发图统一走这里。
func RenderPNG(r Report, style Style) ([]byte, error) {
	if style == StyleApple {
		return renderApplePNG(r)
	}
	return renderClassicPNG(r)
}

/* ------------------------------- 画布基础 ------------------------------- */

type pngRenderer struct {
	dc    *gg.Context
	s     float64
	faces map[float64]font.Face
}

func newPNGRenderer(wLogical, hLogical float64) *pngRenderer {
	return &pngRenderer{
		dc:    gg.NewContext(int(math.Ceil(wLogical*pngScale)), int(math.Ceil(hLogical*pngScale))),
		s:     pngScale,
		faces: map[float64]font.Face{},
	}
}

func (p *pngRenderer) face(px float64) font.Face {
	key := px * p.s
	if f, ok := p.faces[key]; ok {
		return f
	}
	f, err := parsedReportFont()
	if err != nil {
		return nil
	}
	face, err := opentype.NewFace(f, &opentype.FaceOptions{
		Size:    key,
		DPI:     72,
		Hinting: font.HintingFull,
	})
	if err != nil {
		return nil
	}
	p.faces[key] = face
	return face
}

func (p *pngRenderer) encode() ([]byte, error) {
	var buf bytes.Buffer
	if err := p.dc.EncodePNG(&buf); err != nil {
		return nil, fmt.Errorf("编码 PNG 失败: %w", err)
	}
	return buf.Bytes(), nil
}

func (p *pngRenderer) fill(hex string) { p.dc.SetColor(hexColor(hex)) }

func (p *pngRenderer) rect(x, y, w, h float64, hex string) {
	p.fill(hex)
	p.dc.DrawRectangle(x*p.s, y*p.s, w*p.s, h*p.s)
	p.dc.Fill()
}

// rectStroke 圆角矩形：先填后描（stroke-width 为逻辑像素）。
func (p *pngRenderer) rectStroke(x, y, w, h, radius float64, fillHex, strokeHex string, strokeW float64) {
	p.dc.SetColor(hexColor(fillHex))
	p.dc.DrawRoundedRectangle(x*p.s, y*p.s, w*p.s, h*p.s, radius*p.s)
	p.dc.Fill()
	if strokeHex != "" && strokeW > 0 {
		p.dc.SetColor(hexColor(strokeHex))
		p.dc.SetLineWidth(strokeW * p.s)
		p.dc.Stroke()
	}
}

func (p *pngRenderer) circle(cx, cy, radius float64, hex string) {
	p.fill(hex)
	p.dc.DrawCircle(cx*p.s, cy*p.s, radius*p.s)
	p.dc.Fill()
}

func (p *pngRenderer) hline(x1, x2, y float64, hex string, width float64) {
	p.dc.SetColor(hexColor(hex))
	p.dc.SetLineWidth(width * p.s)
	p.dc.DrawLine(x1*p.s, y*p.s, x2*p.s, y*p.s)
	p.dc.Stroke()
}

// text 与 SVG 的 text() 对齐：y 是「垂直居中」的中心线（dominant-baseline
// central），这里换算成基线再画。
func (p *pngRenderer) text(x, y float64, s string, o textOpt) {
	f := p.face(float64(o.size))
	if f == nil {
		return
	}
	p.dc.SetFontFace(f)
	w, _ := p.dc.MeasureString(s)
	var dx float64
	switch o.anchor {
	case "middle":
		dx = -w / 2
	case "end":
		dx = -w
	}
	if o.opacity != "" {
		if op, err := strconv.ParseFloat(o.opacity, 64); err == nil {
			p.dc.SetColor(hexColorAlpha(o.fill, op))
		} else {
			p.dc.SetColor(hexColor(o.fill))
		}
	} else {
		p.dc.SetColor(hexColor(o.fill))
	}
	m := f.Metrics()
	baseline := y*p.s + float64(m.Ascent-m.Descent)/(2*64)
	p.dc.DrawString(s, x*p.s+dx, baseline)
}

/* ------------------------------- 颜色工具 ------------------------------- */

func hexColor(hex string) color.Color {
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) == 3 {
		hex = string([]byte{hex[0], hex[0], hex[1], hex[1], hex[2], hex[2]})
	}
	v, err := strconv.ParseUint(hex, 16, 32)
	if err != nil || len(hex) != 6 {
		return color.RGBA{R: 0, G: 0, B: 0, A: 255}
	}
	return color.RGBA{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 255}
}

func hexColorAlpha(hex string, opacity float64) color.Color {
	c := hexColor(hex)
	r, g, b, _ := c.RGBA()
	return color.NRGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: uint8(255 * math.Max(0, math.Min(1, opacity)))}
}

/* ═══════════════════ classic（女队，样式1） ═══════════════════ */

func renderClassicPNG(r Report) ([]byte, error) {
	const (
		headerHeight      = 54.0
		tableHeaderHeight = 32.0
		rowHeight         = 38.0
		tablePaddingX     = 20.0
	)

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

	p := newPNGRenderer(containerW, heightLogical)
	p.rect(0, 0, containerW, heightLogical, "#F8FAFC")

	// 标题栏
	p.rect(0, 0, containerW, headerHeight, "#1E293B")
	p.text(containerW/2, headerHeight/2+1, titleText, textOpt{size: 22, fill: "#F8FAFC", weight: 700, anchor: "middle"})

	// 表头
	y := headerHeight
	p.rect(0, y, containerW, tableHeaderHeight, "#E2E8F0")
	for _, c := range cols {
		p.text(headerTextX(c, 1), y+tableHeaderHeight/2+1, c.label, textOpt{
			size: 13, fill: "#475569", weight: 700, anchor: headerAnchor(c),
		})
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
		p.rect(0, y, containerW, rowHeight, fill)
		if !row.IsLive {
			p.rect(0, y, 4, rowHeight, "#DC2626")
		}
		lineColor := "#E2E8F0"
		if !row.IsLive {
			lineColor = "#FECACA"
		}
		p.hline(0, containerW, y, lineColor, 0.5)

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
					// 字体没有 emoji，用金银铜圆底白字代替 🥇🥈🥉
					medal := "#F59E0B"
					if rank == 2 {
						medal = "#94A3B8"
					} else if rank == 3 {
						medal = "#F97316"
					}
					p.circle(cx, cy, 11, medal)
					p.text(cx, cy, strconv.Itoa(rank), textOpt{size: 14, fill: "#FFFFFF", weight: 700, anchor: "middle"})
				} else {
					p.text(cx, cy, fmt.Sprintf("%02d", rank), textOpt{size: 15, fill: fillTxt, weight: 700, anchor: "middle"})
				}
			case "name":
				fillTxt := "#0F172A"
				if !row.IsLive {
					fillTxt = "#991B1B"
				}
				p.text(c.x+8, cy, truncateToWidth(row.Name, c.width-16, 15, false), textOpt{
					size: 15, fill: fillTxt, weight: 500, anchor: "start",
				})
			case "master":
				master := row.MasterName
				if strings.TrimSpace(master) == "" {
					master = "—"
				}
				p.text(c.x+12, cy, truncateToWidth(master, c.width-24, 13, false), textOpt{
					size: 13, fill: "#64748B", weight: 500, anchor: "start",
				})
			case "notLiveDays":
				fillTxt := "#15803D"
				if row.NotLiveDays > 0 {
					fillTxt = "#B91C1C"
				}
				p.text(c.x+c.width/2, cy, fmt.Sprint(row.NotLiveDays), textOpt{
					size: 13, fill: fillTxt, weight: 700, anchor: "middle",
				})
			case "dailyWave":
				p.classicWaveBar(c, cy, row, maxWave)
			case "totalWave":
				p.text(c.x+c.width-12, cy, FormatWave(row.TotalWave), textOpt{
					size: 14, fill: "#475569", weight: 500, anchor: "end",
				})
			case "duration":
				p.classicDurationBar(c, cy, row, maxDuration(r.Stats))
			case "tier":
				if row.Tier != "" {
					p.classicTierPill(c, cy, row)
				}
			}
		}
		y += rowHeight
	}

	// 页脚
	p.rect(0, y, containerW, footerHeight, "#E2E8F0")
	totalCount, notLiveCount, notLiveDays := r.summary()
	pageCountLabel := fmt.Sprintf("%s主播 %d 人", r.genderText(), totalCount)
	if r.PageCount > 1 {
		pageCountLabel = fmt.Sprintf("本页 %d 人 · 共 %d 人", len(r.Rows), totalCount)
	}
	summaryText := fmt.Sprintf("%s · 未开播人数 %d 人 · 未开播天数 %d 天", pageCountLabel, notLiveCount, notLiveDays)
	p.text(tablePaddingX, y+26, truncateToWidth(summaryText, containerW-tablePaddingX*2, 18, true), textOpt{size: 18, fill: "#334155", weight: 700})
	p.text(tablePaddingX, y+50, "数据日期 "+classicDate(r.Date), textOpt{size: 12, fill: "#64748B", weight: 500})

	if hasInactive {
		warnX := tablePaddingX
		warnY := y + 64
		warnW := containerW - tablePaddingX*2
		warnH := footerHeight - 78
		p.rectStroke(warnX, warnY, warnW, warnH, 8, "#FEF2F2", "#FECACA", 1)
		p.text(warnX+14, warnY+22,
			fmt.Sprintf("未开播人数 %d 人 · 未开播天数 %d 天", notLiveCount, notLiveDays),
			textOpt{size: 14, fill: "#DC2626", weight: 700})
		for i, line := range r.InactiveLines {
			ly := warnY + 40 + float64(i)*16
			if ly > warnY+warnH {
				break
			}
			p.text(warnX+14, ly, line, textOpt{size: 11, fill: "#7F1D1D", weight: 500})
		}
	}

	return p.encode()
}

func (p *pngRenderer) classicWaveBar(c col, cy float64, row Row, maxWave float64) {
	const padX = 8.0
	const barH = 22.0
	barLeft := c.x + padX
	barTrackW := math.Max(48, c.width-padX*2)
	barTop := cy - barH/2

	if !row.IsLive {
		p.rectStroke(barLeft, barTop, barTrackW, barH, barH/2, "#FEE2E2", "", 0)
		p.text(barLeft+barTrackW/2, cy, "未开播", textOpt{size: 13, fill: "#DC2626", weight: 700, anchor: "middle"})
		return
	}

	fillW := math.Max(0, math.Min(barTrackW, barTrackW*float64(row.DailyWave)/maxWave))
	p.rectStroke(barLeft, barTop, barTrackW, barH, barH/2, "#DBEAFE", "", 0)
	if fillW > 0 {
		p.rectStroke(barLeft, barTop, math.Max(fillW, barH), barH, barH/2, "#60A5FA", "", 0)
	}
	textFill := "#1E3A8A"
	if fillW/barTrackW > 0.52 {
		textFill = "#FFFFFF"
	}
	p.text(barLeft+barTrackW/2, cy, truncateToWidth(FormatWave(row.DailyWave), barTrackW-12, 13, true), textOpt{
		size: 13, fill: textFill, weight: 700, anchor: "middle",
	})
}

func (p *pngRenderer) classicDurationBar(c col, cy float64, row Row, maxDur float64) {
	const padX = 8.0
	const barH = 22.0
	barLeft := c.x + padX
	barTrackW := math.Max(48, c.width-padX*2)
	barTop := cy - barH/2
	dur := row.DurationMinutes

	if dur <= 0 {
		p.rectStroke(barLeft, barTop, barTrackW, barH, barH/2, "#F1F5F9", "", 0)
		p.text(barLeft+barTrackW/2, cy, "—", textOpt{size: 13, fill: "#94A3B8", weight: 700, anchor: "middle"})
		return
	}
	fillW := math.Max(0, math.Min(barTrackW, barTrackW*float64(dur)/math.Max(maxDur, 1)))
	p.rectStroke(barLeft, barTop, barTrackW, barH, barH/2, "#EDE9FE", "", 0)
	if fillW > 0 {
		p.rectStroke(barLeft, barTop, math.Max(fillW, barH), barH, barH/2, "#A78BFA", "", 0)
	}
	textFill := "#5B21B6"
	if fillW/barTrackW > 0.52 {
		textFill = "#FFFFFF"
	}
	p.text(barLeft+barTrackW/2, cy, truncateToWidth(shortDuration(dur), barTrackW-12, 13, true), textOpt{
		size: 13, fill: textFill, weight: 700, anchor: "middle",
	})
}

func (p *pngRenderer) classicTierPill(c col, cy float64, row Row) {
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
	p.rectStroke(bl, bt, bw, bh, 8, bg, "", 0)
	p.text(c.x+c.width/2, cy+1, row.Tier, textOpt{size: 11, fill: fg, weight: 700, anchor: "middle"})
}

/* ═══════════════════ apple（男团，样式2） ═══════════════════ */

func renderApplePNG(r Report) ([]byte, error) {
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
	naturalW := 0.0
	for _, w := range naturalWidths(defs, r.Rows) {
		naturalW += w
	}
	breathing := math.Max(0, float64(len(defs)-5)) * 18
	logicalW := math.Max(560, math.Min(2200, naturalW+breathing))
	cols := layoutColumns(defs, r.Rows, logicalW)
	denseColumns := len(defs) >= 7

	heightLogical := headerH + tableHeaderH + rowGap + tableRowsH + footerH

	p := newPNGRenderer(logicalW, heightLogical)
	p.rect(0, 0, logicalW, heightLogical, "#FFFFFF")

	// 斜向水印网格。615 是 -18° 旋转；gg v1.3 的字形渲染不吃矩阵，
	// 这里保持横向平铺，透明度与间距不变。
	for wy := -heightLogical; wy <= heightLogical; wy += 76 {
		for wx := -logicalW; wx <= logicalW; wx += 180 {
			p.text(logicalW/2+wx, heightLogical/2+wy, "内部数据 · 请勿外传", textOpt{
				size: 18, fill: "#334155", weight: 900, anchor: "middle", opacity: "0.055",
			})
		}
	}

	// 标题区
	titleBase := r.title(StyleApple)
	if s := r.pageSuffix(); s != "" {
		titleBase = titleBase + " " + s
	}
	p.text(logicalW/2, 22, truncateToWidth(titleBase, logicalW-24, 25, true), textOpt{
		size: 25, fill: "#101828", weight: 700, anchor: "middle",
	})
	p.text(logicalW/2, 50, appleDate(r.Date), textOpt{
		size: 13, fill: "#475467", weight: 500, anchor: "middle",
	})
	y := headerH

	// 表头
	p.rect(0, y, logicalW, tableHeaderH, "#F2F4F7")
	for _, c := range cols {
		p.text(headerTextX(c, 1), y+tableHeaderH/2+1, c.label, textOpt{
			size: 12, fill: "#667085", weight: 700, anchor: headerAnchor(c),
		})
	}
	y += tableHeaderH + rowGap

	maxWave := maxLiveWave(r.Rows)
	maxDur := maxDuration(r.Stats)
	rankOffset := 0
	if r.PageIndex > 1 && len(r.Rows) > 0 {
		rankOffset = (r.PageIndex - 1) * len(r.Rows)
	}

	barH := 22.0
	if denseColumns {
		barH = 20.0
	}

	for i, row := range r.Rows {
		rank := rankOffset + i + 1
		rowFill := "#FFFFFF"
		stroke := "#EAECF0"
		if !row.IsLive {
			rowFill = "#FFF7F7"
			stroke = "#FEE4E2"
		}
		p.rectStroke(0, y, logicalW, rowH, 0, rowFill, stroke, 1)
		if !row.IsLive {
			p.rectStroke(0, y+8, 4, rowH-16, 2, "#F04438", "", 0)
		}

		cy := y + rowH/2 + 1
		for _, c := range cols {
			switch c.key {
			case "rank":
				p.appleRankChip(c, cy, rank, !row.IsLive)
			case "name":
				fillTxt := "#101828"
				if !row.IsLive {
					fillTxt = "#B42318"
				}
				p.text(c.x+8, cy, truncateToWidth(row.Name, c.width-16, 14, true), textOpt{
					size: 14, fill: fillTxt, weight: 700, anchor: "start",
				})
			case "master":
				master := row.MasterName
				if strings.TrimSpace(master) == "" {
					master = "-"
				}
				p.text(c.x+12, cy, truncateToWidth(master, c.width-24, 13, false), textOpt{
					size: 13, fill: "#667085", weight: 500, anchor: "start",
				})
			case "notLiveDays":
				fillTxt := "#027A48"
				if row.NotLiveDays > 0 {
					fillTxt = "#B42318"
				}
				p.text(c.x+c.width/2, cy, fmt.Sprint(row.NotLiveDays), textOpt{
					size: 13, fill: fillTxt, weight: 700, anchor: "middle",
				})
			case "dailyWave":
				p.appleWaveBar(c, cy, row, maxWave, barH)
			case "totalWave":
				p.text(c.x+c.width-12, cy, FormatWave(row.TotalWave), textOpt{
					size: 13, fill: "#475467", weight: 600, anchor: "end",
				})
			case "duration":
				p.appleDurationBar(c, cy, row, maxDur, barH)
			case "tier":
				if row.Tier != "" {
					p.appleTierPill(c, cy, row)
				}
			}
		}
		y += rowH
		if i != len(r.Rows)-1 {
			y += rowGap
		}
	}

	// 页脚
	totalCount, notLiveCount, notLiveDays := r.summary()
	footerY := y + footerTextGap
	peopleLabel := fmt.Sprintf("%s %d 人", r.genderGroupText(), totalCount)
	if r.PageCount > 1 {
		peopleLabel = fmt.Sprintf("本页 %d/%d 人", len(r.Rows), totalCount)
	}
	p.text(8, footerY+4, fmt.Sprintf("数据日期 %s · %s", appleDate(r.Date), peopleLabel), textOpt{
		size: 12, fill: "#667085", weight: 600, anchor: "start",
	})
	p.text(logicalW-8, footerY+4,
		fmt.Sprintf("未开播人数 %d 人 · 未开播天数 %d 天", notLiveCount, notLiveDays), textOpt{
			size: 12, fill: "#667085", weight: 600, anchor: "end",
		})
	p.text(logicalW/2, footerY+4, "内部数据 · 请勿外传", textOpt{
		size: 12, fill: "#98A2B3", weight: 700, anchor: "middle",
	})

	if hasInactive {
		warnY := footerY + footerTextH + warnGap
		p.rectStroke(0, warnY, logicalW, warnH, 0, "#FFF7F7", "#FEE4E2", 1)
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
		p.text(14, warnY+warnH/2,
			truncateToWidth("未开播："+strings.Join(names, "、"), logicalW-28, 12, true), textOpt{
				size: 12, fill: "#B42318", weight: 700, anchor: "start",
			})
	}

	return p.encode()
}

func (p *pngRenderer) appleRankChip(c col, cy float64, rank int, inactive bool) {
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

	p.rectStroke(chipX, chipY, chipW, chipH, chipH/2, chipFill, chipStroke, 1)
	p.text(chipX+chipW/2, cy, fmt.Sprintf("%02d", rank), textOpt{
		size: 12, fill: chipText, weight: 700, anchor: "middle",
	})
}

func (p *pngRenderer) appleWaveBar(c col, cy float64, row Row, maxWave, barH float64) {
	const padX = 8.0
	barX := c.x + padX
	barW := math.Max(48, c.width-padX*2)
	barY := cy - barH/2

	if !row.IsLive {
		p.rectStroke(barX, barY, barW, barH, barH/2, "#FEE4E2", "", 0)
		p.text(barX+barW/2, cy, "未开播", textOpt{size: 13, fill: "#D92D20", weight: 700, anchor: "middle"})
		return
	}

	fillW := math.Max(0, math.Min(barW, float64(row.DailyWave)/maxWave*barW))
	p.rectStroke(barX, barY, barW, barH, barH/2, "#EAF3FF", "#D6E8FF", 1)
	if fillW > 0 {
		p.rectStroke(barX, barY, math.Max(fillW, barH), barH, barH/2, blue, "", 0)
	}
	textFill := "#1D4ED8"
	if fillW/barW > 0.52 {
		textFill = "#FFFFFF"
	}
	p.text(barX+barW/2, cy, truncateToWidth(FormatWave(row.DailyWave), barW-12, 12, true), textOpt{
		size: 12, fill: textFill, weight: 800, anchor: "middle",
	})
}

func (p *pngRenderer) appleDurationBar(c col, cy float64, row Row, maxDur, barH float64) {
	const padX = 8.0
	barX := c.x + padX
	barW := math.Max(48, c.width-padX*2)
	barY := cy - barH/2
	dur := row.DurationMinutes

	if dur <= 0 {
		p.rectStroke(barX, barY, barW, barH, barH/2, "#F1F1F3", "#E4E4E7", 1)
		p.text(barX+barW/2, cy, "-", textOpt{size: 12, fill: "#1D4ED8", weight: 800, anchor: "middle"})
		return
	}
	fillW := math.Max(0, math.Min(barW, barW*float64(dur)/math.Max(maxDur, 1)))
	p.rectStroke(barX, barY, barW, barH, barH/2, "#F3F0FF", "#E5E0FF", 1)
	if fillW > 0 {
		p.rectStroke(barX, barY, math.Max(fillW, barH), barH, barH/2, "#8B5CF6", "", 0)
	}
	textFill := "#1D4ED8"
	if fillW/barW > 0.52 {
		textFill = "#FFFFFF"
	}
	p.text(barX+barW/2, cy, truncateToWidth(shortDuration(dur), barW-12, 12, true), textOpt{
		size: 12, fill: textFill, weight: 800, anchor: "middle",
	})
}

func (p *pngRenderer) appleTierPill(c col, cy float64, row Row) {
	tier := row.Tier
	bw := math.Min(c.width-18, math.Max(42, estimateTextWidth(tier, 11, true)+24))
	bh := 24.0
	bx := c.x + (c.width-bw)/2
	by := cy - bh/2
	colors := appleTierColor(tier)

	p.rectStroke(bx, by, bw, bh, bh/2, colors.bg, colors.border, 1)
	p.text(c.x+c.width/2, cy+0.5, tier, textOpt{size: 11, fill: colors.text, weight: 700, anchor: "middle"})
}
