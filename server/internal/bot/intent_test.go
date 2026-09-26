package bot

import (
	"testing"
	"time"
)

func TestParseIntent(t *testing.T) {
	now := time.Date(2026, 9, 20, 10, 0, 0, 0, time.Local)

	tests := []struct {
		in     string
		kind   IntentKind
		date   string
		period string
		year   int
		query  string
	}{
		{"帮助", IntentHelp, "", "", 0, ""},
		{"help", IntentHelp, "", "", 0, ""},

		// 日报默认 T-1：数据是次日才出的，20 号发的日报是 19 号的榜
		{"每日报告", IntentDailyReport, "2026-09-19", "", 0, ""},
		{"日报", IntentDailyReport, "2026-09-19", "", 0, ""},
		{"今天", IntentDailyReport, "2026-09-20", "", 0, ""},
		{"昨天", IntentDailyReport, "2026-09-19", "", 0, ""},
		{"18号报告", IntentDailyReport, "2026-09-18", "", 0, ""},

		// 裸数字日期 = 预告导入日（615 语义），不是日报
		{"9.11", IntentImportDate, "2026-09-11", "", 0, ""},
		{"2026年9月19日", IntentImportDate, "2026-09-19", "", 0, ""},

		{"9月", IntentMonthlyReport, "", "2026-09", 0, ""},
		{"2026年3月", IntentMonthlyReport, "", "2026-03", 0, ""},
		{"月报", IntentMonthlyReport, "", "2026-09", 0, ""},

		{"2026年", IntentYearlyReport, "", "", 2026, ""},
		{"年报", IntentYearlyReport, "", "", 2026, ""},

		{"每日之星", IntentDailyStar, "2026-09-19", "", 0, ""},

		{"柚子", IntentPersonQuery, "", "", 0, "柚子"},
		{"柚子 9月", IntentPersonQuery, "2026-09-01", "2026-09", 2026, "柚子"},
		{"柚子音浪", IntentPersonQuery, "", "", 0, "柚子"},

		{"开启日报推送", IntentPushToggle, "", "", 0, ""},
		{"关闭日报推送", IntentPushToggle, "", "", 0, ""},
		{"日报推送状态", IntentPushStatus, "", "", 0, ""},

		{"第1组", IntentPKGroup, "", "", 0, ""},
		{"分组", IntentPKGroup, "", "", 0, ""},
	}

	for _, tt := range tests {
		got := ParseIntent(tt.in, now)
		if got.Kind != tt.kind {
			t.Errorf("ParseIntent(%q).Kind = %s, 期望 %s", tt.in, got.Kind, tt.kind)
			continue
		}
		if tt.date != "" && got.Date != tt.date {
			t.Errorf("ParseIntent(%q).Date = %q, 期望 %q", tt.in, got.Date, tt.date)
		}
		if tt.period != "" && got.Period != tt.period {
			t.Errorf("ParseIntent(%q).Period = %q, 期望 %q", tt.in, got.Period, tt.period)
		}
		if tt.year != 0 && got.Year != tt.year {
			t.Errorf("ParseIntent(%q).Year = %d, 期望 %d", tt.in, got.Year, tt.year)
		}
		if tt.query != "" && got.Query != tt.query {
			t.Errorf("ParseIntent(%q).Query = %q, 期望 %q", tt.in, got.Query, tt.query)
		}
	}
}

func TestPushToggleEnable(t *testing.T) {
	now := time.Now()
	if got := ParseIntent("开启日报推送", now); !got.Enable {
		t.Error("开启应为 Enable=true")
	}
	if got := ParseIntent("关闭日报推送", now); got.Enable {
		t.Error("关闭应为 Enable=false")
	}
}

// 每日数据自动推送是独立于「日报推送」（1 点索要）的开关，
// 「拒绝每日推送」这种说法不能被通用推送判断吞掉。
func TestAutoReportToggle(t *testing.T) {
	now := time.Now()
	cases := []struct {
		in     string
		kind   IntentKind
		enable bool
	}{
		{"开启每日推送", IntentAutoReport, true},
		{"接受每日推送", IntentAutoReport, true},
		{"拒绝每日推送", IntentAutoReport, false},
		{"关闭每日推送", IntentAutoReport, false},
		{"不要自动推送", IntentAutoReport, false},
		{"每日推送", IntentAutoReport, true},
		// 不含「每日/自动推送」的照旧走 1 点索要开关
		{"开启日报推送", IntentPushToggle, true},
	}
	for _, c := range cases {
		got := ParseIntent(c.in, now)
		if got.Kind != c.kind {
			t.Errorf("ParseIntent(%q).Kind = %s, 期望 %s", c.in, got.Kind, c.kind)
			continue
		}
		if got.Enable != c.enable {
			t.Errorf("ParseIntent(%q).Enable = %v, 期望 %v", c.in, got.Enable, c.enable)
		}
	}
}

func TestNormalizeText(t *testing.T) {
	if got := NormalizeText("  日报。  "); got != "日报" {
		t.Errorf("NormalizeText = %q", got)
	}
	if got := NormalizeText("@机器人 日报"); got != "日报" {
		t.Errorf("去掉 @ 提及后 = %q", got)
	}
}

func TestCleanQuery(t *testing.T) {
	if got := cleanQuery("柚子报告"); got != "柚子" {
		t.Errorf("cleanQuery = %q", got)
	}
	if got := cleanQuery("柚子音浪"); got != "柚子" {
		t.Errorf("cleanQuery = %q", got)
	}
}
