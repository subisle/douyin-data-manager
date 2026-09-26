// Package bot 是机器人的框架层：把两个 IM 通道（微信 iLink / QQ）收敛成
// 一套意图解析 + 技能路由，与 615 的 project-bots.js 思路一致——
// 一个 Agent，多种 Transport。
package bot

import (
	"regexp"
	"strings"
	"time"

	"douyin-server/internal/dateparse"
)

// IntentKind 指令类型。
type IntentKind string

const (
	IntentHelp          IntentKind = "help"
	IntentDailyReport   IntentKind = "daily_report"
	IntentMonthlyReport IntentKind = "monthly_report"
	IntentYearlyReport  IntentKind = "yearly_report"
	IntentDailyStar     IntentKind = "daily_star"
	IntentPersonQuery   IntentKind = "person_query"
	IntentPushToggle    IntentKind = "push_toggle"
	IntentPushStatus    IntentKind = "push_status"
	IntentAutoReport    IntentKind = "auto_report"
	IntentPKGroup       IntentKind = "pk_group"
	IntentImportDate    IntentKind = "import_date"
	IntentAddAnchor     IntentKind = "add_anchor"
	IntentImportLogs    IntentKind = "import_logs"
	IntentQuit          IntentKind = "quit"
	IntentUnknown       IntentKind = "unknown"
)

// Intent 解析后的指令。
type Intent struct {
	Kind     IntentKind
	Date     string // YYYY-MM-DD，日粒度指令用
	Period   string // YYYY-MM，月粒度用
	Year     int
	Gender   string // male / female，留空表示全团
	Query    string // 艺名 / 新主播姓名
	Enable   bool   // push_toggle 用
	Group    string // PK 分组名
	DouyinNo string // add_anchor 用：要绑的抖音号
	RawText  string
}

// reAtMention 群消息里 @机器人 的前缀，要整段去掉
var reAtMention = regexp.MustCompile(`@[^\s@]+`)
var reGroupIndex = regexp.MustCompile(`第\s*\d+\s*组`)

// reImportDateToken 整条消息就是一个日期（照抄 615 的 importDateToken 正则）。
// 「9.11 / 9月11日 / 11号 / 2026-09-11 / 2026年9月11日」→ 预告导入日。
// 刻意不含「昨天/今天」和「2026年9月」：前者走日报，后者是月报。
var reImportDateToken = regexp.MustCompile(
	`^/?(\d{1,2}[.．]\d{1,3}[日号]?|\d{1,2}月\d{1,3}[日号]?|\d{1,2}[日号]|20\d{2}[年./-]\d{1,2}[月./-]\d{1,2}[日号]?)$`)

// reAddAnchor 「姓名-抖音号」→ 新增主播。姓名不含数字与连字符（防误吞日期），
// 抖音号是字母数字下划线点（≥4 位）。连字符支持全角、em/en dash 等变体。
var reAddAnchor = regexp.MustCompile(
	`^([^\d\-—–－―_]{1,32})\s*[-—–－―]\s*([a-zA-Z0-9._]{4,64})$`)

// ParseAddAnchor 从文本解析「姓名-抖音号」，不匹配返回 nil。
func ParseAddAnchor(text string) (name, douyinNo string) {
	m := reAddAnchor.FindStringSubmatch(strings.TrimSpace(text))
	if m == nil {
		return "", ""
	}
	return strings.TrimSpace(m[1]), strings.TrimSpace(m[2])
}

// isDayGranularity 三种"天"粒度：完整年月日、月日、仅日。
func isDayGranularity(t dateparse.SpecType) bool {
	return t == dateparse.TypeDate || t == dateparse.TypeMonthDay || t == dateparse.TypeDay
}

// NormalizeText 去掉 @ 提及、首尾空白与尾部标点。
func NormalizeText(s string) string {
	s = strings.TrimSpace(s)
	s = reAtMention.ReplaceAllString(s, "")
	s = strings.TrimSpace(strings.TrimRight(s, "。！!，,；;"))
	return s
}

// ParseIntent 解析一条消息。now 用于补全相对日期。
//
// 匹配顺序刻意从具体到宽泛：先认"开启日报推送"这类固定指令，
// 再认日期，最后才当成艺名查询——否则"9月"会被当成艺名。
func ParseIntent(raw string, now time.Time) Intent {
	text := NormalizeText(raw)
	intent := Intent{RawText: text}

	if text == "" {
		intent.Kind = IntentUnknown
		return intent
	}

	lower := strings.ToLower(text)

	// 导入记录/导入日志：查看最近导入
	if lower == "导入记录" || lower == "导入日志" {
		intent.Kind = IntentImportLogs
		return intent
	}

	// 退出口令单独认：Manager.Handle 里比这里更早处理，留在这儿是为了
	// /bots/parse 这种只解析不走技能的调用也能得到正确答案。
	if IsQuitCommand(text) {
		intent.Kind = IntentQuit
		return intent
	}

	// 帮助
	for _, kw := range []string{"帮助", "help", "菜单", "指令"} {
		if lower == kw || strings.Contains(lower, kw+"指令") {
			intent.Kind = IntentHelp
			return intent
		}
	}

	// 每日数据自动推送：「开启每日推送 / 接受每日推送」开，「拒绝每日推送 /
	// 关闭每日推送」关。与「日报推送」（1 点索要提醒）是两个独立开关。
	// 必须放在通用「推送」判断之前，否则「拒绝每日推送」会被吞掉。
	if strings.Contains(text, "每日推送") || strings.Contains(text, "自动推送") {
		switch {
		case strings.Contains(text, "拒绝") || strings.Contains(text, "关闭") ||
			strings.Contains(text, "不要") || strings.Contains(text, "取消"):
			intent.Kind = IntentAutoReport
			intent.Enable = false
		default: // 开启 / 接受 / 打开 / 单发「每日推送」都当开启
			intent.Kind = IntentAutoReport
			intent.Enable = true
		}
		return intent
	}

	// 日报推送开关
	if strings.Contains(text, "推送") {
		switch {
		case strings.HasPrefix(text, "开启") || strings.HasPrefix(text, "打开") || strings.Contains(text, "开启日报推送"):
			intent.Kind = IntentPushToggle
			intent.Enable = true
			return intent
		case strings.HasPrefix(text, "关闭") || strings.HasPrefix(text, "关掉"):
			intent.Kind = IntentPushToggle
			intent.Enable = false
			return intent
		case strings.Contains(text, "状态"):
			intent.Kind = IntentPushStatus
			return intent
		}
	}

	// PK 分组
	if strings.Contains(text, "分组") || strings.Contains(text, "PK") ||
		strings.Contains(text, "pk") || reGroupIndex.MatchString(text) {
		intent.Kind = IntentPKGroup
		intent.Group = strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(text, "分组"), "PK"))
		return intent
	}

	// 每日之星（同样按 T+1：没说哪天就是昨天）
	if strings.Contains(text, "每日之星") || strings.Contains(text, "之星") {
		intent.Kind = IntentDailyStar
		spec := dateparse.ParseDateSpec(text)
		if spec == nil {
			intent.Date = now.AddDate(0, 0, -1).Format("2006-01-02")
		} else if d, err := dateparse.ResolveDate(spec, now); err == nil {
			intent.Date = d.Format("2006-01-02")
		}
		return intent
	}

	// 年报：年粒度
	if spec := dateparse.ParseDateSpec(text); spec != nil && spec.Type == dateparse.TypeYear && !strings.Contains(text, "报告") {
		intent.Kind = IntentYearlyReport
		intent.Year = dateparse.ResolveYear(spec, now)
		return intent
	}

	// 预告导入日期：「9.11」「9月11日」「11号」「2026-09-11」单发
	// → 记住日期，10 分钟内连传的 CSV 都导入该日（615 同款语义）。
	// 注意与日报的边界：只有**纯数字日期**才算，「昨天」「今天」仍走日报；
	// 带报告/之星等关键词的（如「18号报告」）也不算。
	if reImportDateToken.MatchString(text) {
		if spec := dateparse.ParseDateSpec(text); spec != nil && isDayGranularity(spec.Type) {
			if d, err := dateparse.ResolveDate(spec, now); err == nil {
				intent.Kind = IntentImportDate
				intent.Date = d.Format("2006-01-02")
				return intent
			}
		}
	}

	// 「姓名-抖音号」→ 新增主播。放在 import_date 之后（日期无连字符不冲突），
	// SplitQuery 之前（否则姓名会被当成艺名查询）。
	if name, douyinNo := ParseAddAnchor(text); name != "" {
		intent.Kind = IntentAddAnchor
		intent.Query = name
		intent.DouyinNo = douyinNo
		return intent
	}

	// 报告类：先把「艺名 + 日期」拆开（日期可能在句尾也可能在句中）
	query, spec := dateparse.SplitQuery(text)
	if name := cleanQuery(query); isName(name) {
		intent.Query = name
	}
	intent.Gender = extractGender(text)

	if intent.Query != "" {
		intent.Kind = IntentPersonQuery
		if spec != nil {
			if d, err := dateparse.ResolveDate(spec, now); err == nil {
				intent.Date = d.Format("2006-01-02")
			}
			if p, err := dateparse.ResolveMonth(spec, now); err == nil {
				intent.Period = p
			}
			intent.Year = dateparse.ResolveYear(spec, now)
		}
		return intent
	}

	// 没有艺名：由粒度决定指令类型
	// 关键词优先于粒度——"月报"两个字就该出月报
	switch {
	case strings.Contains(text, "月报") ||
		(spec != nil && (spec.Type == dateparse.TypeMonth || spec.Type == dateparse.TypeMonthOnly)):
		intent.Kind = IntentMonthlyReport
		if p, err := dateparse.ResolveMonth(spec, now); err == nil {
			intent.Period = p
		}
	case strings.Contains(text, "年报") ||
		(spec != nil && spec.Type == dateparse.TypeYear):
		intent.Kind = IntentYearlyReport
		intent.Year = dateparse.ResolveYear(spec, now)
	default:
		intent.Kind = IntentDailyReport
		// 没说哪天 → 昨天。数据是 T+1 出的：24 号发的日报就是 23 号的榜，
		// 用今天去查只会出一张空图。明确说了「今天」的仍然按今天走。
		if spec == nil {
			intent.Date = now.AddDate(0, 0, -1).Format("2006-01-02")
		} else if d, err := dateparse.ResolveDate(spec, now); err == nil {
			intent.Date = d.Format("2006-01-02")
		}
	}
	return intent
}

// nonNameWords 这些词是指令的一部分，不是艺名。
// 少了这张表，"每日报告"会被解析成查一个叫「每日」的主播。
var nonNameWords = map[string]bool{
	"": true, "每日": true, "日": true, "月": true, "年": true, "报告": true,
	"报":  true, // 日期剥落后可能剩单字（"9月11日报"→"报"），不会有人叫这个
	"数据": true, "今日": true, "今天": true, "昨天": true, "昨日": true,
	"之星": true, "每日之星": true, "的": true, "音浪": true, "时长": true,
	"分组": true, "组": true, "文件": true,
}

func isName(s string) bool {
	if s == "" {
		return false
	}
	return !nonNameWords[s]
}

// cleanQuery 去掉"报告/音浪/时长/数据"这类尾巴，剩下的才是艺名。
func cleanQuery(s string) string {
	s = strings.TrimSpace(s)
	for _, suffix := range []string{"报告", "日报", "月报", "年报", "音浪", "时长", "数据", "文件", "的"} {
		s = strings.TrimSuffix(s, suffix)
	}
	return strings.TrimSpace(s)
}

func extractGender(text string) string {
	switch {
	case strings.Contains(text, "男团"), strings.Contains(text, "男"):
		return "male"
	case strings.Contains(text, "女队"), strings.Contains(text, "女"):
		return "female"
	}
	return ""
}
