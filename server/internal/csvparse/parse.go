// Package csvparse 解析主播数据 CSV。
//
// 这段逻辑是从 615 的 src/components/desktop/csv.ts 搬过来的，规则必须一致：
// 运营手里的 CSV 表头千奇百怪（"主播id"/"抖音号"/"anchor_id"/"uid" 混用），
// 数值单位也乱（"12.5万" / "2:30:45" / "37小时16分钟" / "150分钟"），
// 不匹配这些别名就没法用。
package csvparse

import (
	"encoding/csv"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode"
)

// Kind 是 CSV 的类型，靠表头自动识别。
type Kind string

const (
	KindUnknown  Kind = ""
	KindWave     Kind = "wave"
	KindDuration Kind = "duration"
	KindAnchors  Kind = "anchors"
)

// normalizeHeader 去掉空格、下划线、括号，统一小写后比较。
func normalizeHeader(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsSpace(r) {
			continue
		}
		switch r {
		case '_', '-', '(', ')', '（', '）', '[', ']', '【', '】', '.':
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

var (
	idHeaders = []string{
		"主播id", "主播账号", "抖音号", "抖音id", "anchorid", "uid", "id",
	}
	nameHeaders = []string{
		"主播名", "主播昵称", "昵称", "用户昵称", "姓名", "抖音昵称", "达人昵称",
		"作者昵称", "成员", "主播", "anchorname", "name", "nickname",
	}
	waveHeaders = []string{"音浪", "wavevalue", "wave", "总音浪"}
	durHeaders  = []string{
		"时长", "duration", "durationminutes", "直播时长", "开播有效时长",
		"有效时长", "开播时长",
	}
	rankHeaders   = []string{"排名", "rank"}
	douyinHeaders = []string{"抖音号", "抖音id", "douyinno"}
	// 识别类型用的关键词，命中即归类
	kindKeywords = map[Kind][]string{
		KindDuration: {"时长", "duration", "开播", "有效时长"},
		KindWave:     {"音浪", "wave", "总音浪"},
		KindAnchors:  {"主播", "昵称", "抖音号"},
	}
)

// findColumn 在表头里找第一个命中的列下标，找不到返回 -1。
func findColumn(headers []string, candidates []string) int {
	normalized := make([]string, len(headers))
	for i, h := range headers {
		normalized[i] = normalizeHeader(h)
	}
	for _, c := range candidates {
		for i, n := range normalized {
			if n == c {
				return i
			}
		}
	}
	// 退一步：包含匹配，应对"主播ID（必填）"这类
	for _, c := range candidates {
		for i, n := range normalized {
			if strings.Contains(n, c) {
				return i
			}
		}
	}
	return -1
}

// DetectKind 按表头猜 CSV 类型。与 615 的顺序一致：时长 → 音浪 → 主播档案。
func DetectKind(headers []string) Kind {
	joined := strings.Builder{}
	for _, h := range headers {
		joined.WriteString(normalizeHeader(h))
	}
	all := joined.String()

	for _, kw := range kindKeywords[KindDuration] {
		if strings.Contains(all, kw) {
			return KindDuration
		}
	}
	for _, kw := range kindKeywords[KindWave] {
		if strings.Contains(all, kw) {
			return KindWave
		}
	}
	for _, kw := range kindKeywords[KindAnchors] {
		if strings.Contains(all, kw) {
			return KindAnchors
		}
	}
	return KindUnknown
}

// ParseWave 解析音浪值，支持 "12.5万" / "125000" / "12,500" / "45512音浪"。
// 运营导出的 CSV 数值常带单位尾巴（"音浪"/"元"/"钻"），与 615 的 parseInt
// 行为对齐：遇到非数字就截断，不报错。
func ParseWave(raw string) (int64, error) {
	s := strings.TrimSpace(raw)
	s = strings.ReplaceAll(s, ",", "")
	s = strings.ReplaceAll(s, "，", "")
	if s == "" || s == "-" {
		return 0, nil
	}

	// 常见单位尾巴直接剔除
	for _, unit := range []string{"音浪", "元", "钻"} {
		s = strings.ReplaceAll(s, unit, "")
	}
	s = strings.TrimSpace(s)

	multiplier := int64(1)
	switch {
	case strings.HasSuffix(s, "万"):
		multiplier = 10000
		s = strings.TrimSuffix(s, "万")
	case strings.HasSuffix(s, "w"), strings.HasSuffix(s, "W"):
		multiplier = 10000
		s = s[:len(s)-1]
	case strings.HasSuffix(s, "亿"):
		multiplier = 100000000
		s = strings.TrimSuffix(s, "亿")
	}
	s = strings.TrimSpace(s)

	// 兜底：截取开头的数字部分（与 parseInt 语义一致），应对未知尾巴
	if idx := numericPrefixLen(s); idx >= 0 && idx < len(s) {
		s = s[:idx]
	}

	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, errors.New("音浪值无法解析: " + raw)
	}
	return int64(v * float64(multiplier)), nil
}

// numericPrefixLen 返回开头连续数字/小数点部分的长度；开头就不是数字返回 -1。
func numericPrefixLen(s string) int {
	i := 0
	for i < len(s) && (unicode.IsDigit(rune(s[i])) || s[i] == '.') {
		i++
	}
	if i == 0 {
		return -1
	}
	return i
}

// ParseDuration 解析时长，统一换算为分钟。
// 支持：2:30:45 / 37小时16分钟5秒 / 1小时30分 / 150分钟 / 150
func ParseDuration(raw string) (int, error) {
	s := strings.TrimSpace(raw)
	s = strings.ReplaceAll(s, "：", ":")
	if s == "" || s == "-" {
		return 0, nil
	}

	// 2:30:45 或 1:30
	if strings.Contains(s, ":") {
		parts := strings.Split(s, ":")
		vals := make([]int, 0, len(parts))
		for _, p := range parts {
			v, err := strconv.Atoi(strings.TrimSpace(p))
			if err != nil {
				return 0, errors.New("时长无法解析: " + raw)
			}
			vals = append(vals, v)
		}
		switch len(vals) {
		case 3:
			return vals[0]*60 + vals[1] + vals[2]/60, nil
		case 2:
			return vals[0]*60 + vals[1], nil
		default:
			return 0, errors.New("时长无法解析: " + raw)
		}
	}

	// 中文：37小时16分钟5秒 / 1小时30分
	if strings.Contains(s, "小时") || strings.Contains(s, "分钟") || strings.Contains(s, "分") || strings.Contains(s, "秒") {
		hours := extractInt(s, "小时", "时")
		minutes := extractInt(s, "分钟", "分")
		seconds := extractInt(s, "秒", "")
		return hours*60 + minutes + seconds/60, nil
	}

	// 纯数字：150分钟 或 150
	s = strings.TrimSuffix(s, "分钟")
	s = strings.TrimSuffix(s, "min")
	s = strings.TrimSpace(s)
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, errors.New("时长无法解析: " + raw)
	}
	return int(v), nil
}

// extractInt 取"37小时"里的 37。primary 优先，其次 fallback。
func extractInt(s, primary, fallback string) int {
	idx := strings.Index(s, primary)
	if idx < 0 && fallback != "" {
		idx = strings.Index(s, fallback)
		if idx < 0 {
			return 0
		}
		// "分" 可能跟在 "分钟" 后面，这里只认独立的"分"
		if fallback == "分" && idx+len(fallback) < len(s) && s[idx+len(fallback):idx+len(fallback)+3] == "钟" {
			return 0
		}
	} else if idx < 0 {
		return 0
	}
	start := idx
	for start > 0 && (unicode.IsDigit(rune(s[start-1])) || s[start-1] == '.') {
		start--
	}
	numStr := s[start:idx]
	if numStr == "" {
		return 0
	}
	v, err := strconv.ParseFloat(numStr, 64)
	if err != nil {
		return 0
	}
	return int(v)
}

// Row 是解析出的一行。
type Row struct {
	AnchorID string
	Name     string
	DouyinNo string
	Wave     int64
	Minutes  int
	Rank     *int
	RawIndex int // 在文件里的行号，便于回查
	Err      string
}

// Parse 解析整个 CSV。
func Parse(r io.Reader) (Kind, []Row, error) {
	reader := csv.NewReader(r)
	reader.FieldsPerRecord = -1 // 负数 = 不校验列数，运营的 CSV 经常列数不齐
	reader.TrimLeadingSpace = true

	records, err := reader.ReadAll()
	if err != nil {
		return KindUnknown, nil, err
	}
	if len(records) == 0 {
		return KindUnknown, nil, errors.New("CSV 是空的")
	}

	headers := records[0]
	kind := DetectKind(headers)

	idCol := findColumn(headers, idHeaders)
	nameCol := findColumn(headers, nameHeaders)
	waveCol := findColumn(headers, waveHeaders)
	durCol := findColumn(headers, durHeaders)
	rankCol := findColumn(headers, rankHeaders)
	douyinCol := findColumn(headers, douyinHeaders)

	rows := make([]Row, 0, len(records)-1)
	for i, rec := range records[1:] {
		if isBlank(rec) {
			continue
		}
		row := Row{RawIndex: i + 2} // +2：跳过表头且行号从 1 开始

		if idCol >= 0 && idCol < len(rec) {
			row.AnchorID = strings.TrimSpace(rec[idCol])
		}
		if douyinCol >= 0 && douyinCol < len(rec) {
			row.DouyinNo = strings.TrimSpace(rec[douyinCol])
			if row.AnchorID == "" {
				row.AnchorID = row.DouyinNo // 没有主播 id 列时用抖音号顶替（与 615 一致）
			}
		}
		if nameCol >= 0 && nameCol < len(rec) {
			row.Name = strings.TrimSpace(rec[nameCol])
		}
		if waveCol >= 0 && waveCol < len(rec) {
			v, err := ParseWave(rec[waveCol])
			if err != nil {
				row.Err = err.Error()
			}
			row.Wave = v
		}
		if durCol >= 0 && durCol < len(rec) {
			v, err := ParseDuration(rec[durCol])
			if err != nil {
				row.Err = err.Error()
			}
			row.Minutes = v
		}
		if rankCol >= 0 && rankCol < len(rec) {
			if v, err := strconv.Atoi(strings.TrimSpace(rec[rankCol])); err == nil {
				row.Rank = &v
			}
		}
		rows = append(rows, row)
	}
	return kind, rows, nil
}

func isBlank(rec []string) bool {
	for _, f := range rec {
		if strings.TrimSpace(f) != "" {
			return false
		}
	}
	return true
}
