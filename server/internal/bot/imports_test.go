package bot

import (
	"strings"
	"testing"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"
)

func TestImportDateIntent(t *testing.T) {
	// now 固定，避免月末月初跑测试结果漂移
	now := time.Date(2026, 9, 20, 10, 0, 0, 0, time.Local)

	cases := []struct {
		text string
		want IntentKind
		date string
	}{
		{"9.11", IntentImportDate, "2026-09-11"},
		{"9.11号", IntentImportDate, "2026-09-11"},
		{"9月11日", IntentImportDate, "2026-09-11"},
		{"11号", IntentImportDate, "2026-09-11"},
		{"2026-09-11", IntentImportDate, "2026-09-11"},
		{"2026年9月11日", IntentImportDate, "2026-09-11"},
		// 这些都不该是导入日期
		{"18号报告", IntentDailyReport, ""},     // 带报告 → 日报
		{"昨天", IntentDailyReport, ""},        // 相对词 → 日报
		{"9月", IntentMonthlyReport, ""},      // 月粒度 → 月报
		{"2026年9月", IntentMonthlyReport, ""}, // 月粒度 → 月报
		{"柚子 9月", IntentPersonQuery, ""},     // 艺名优先
		{"9月11日报", IntentDailyReport, ""},    // 日粒度 + 报告 → 指定日日报
	}
	for _, c := range cases {
		got := ParseIntent(c.text, now)
		if got.Kind != c.want {
			t.Errorf("ParseIntent(%q).Kind = %q, want %q", c.text, got.Kind, c.want)
			continue
		}
		if c.date != "" && got.Date != c.date {
			t.Errorf("ParseIntent(%q).Date = %q, want %q", c.text, got.Date, c.date)
		}
	}
}

func TestPendingImportWindow(t *testing.T) {
	m := NewManager(nil)
	conv := "c2c:testuser"

	// 没预告过 → 昨天兜底
	date, source, pending := m.resolveInboundImportDate(conv)
	if source != "yesterday" || pending != nil {
		t.Fatalf("未预告时应为 yesterday, got %s", source)
	}

	// 预告 9.11
	m.rememberImportDate(conv, "2026-09-11")
	date, source, pending = m.resolveInboundImportDate(conv)
	if source != "pending" || pending == nil {
		t.Fatalf("预告后应为 pending, got %s", source)
	}
	if got := date.Format("2006-01-02"); got != "2026-09-11" {
		t.Fatalf("预告日期 = %s, want 2026-09-11", got)
	}

	// 连传文件：第一个扣掉后还剩 11（上限提到 12 以容纳多工会），第二个继续可用
	m.consumePending(pending, "wave")
	_, source, pending2 := m.resolveInboundImportDate(conv)
	if source != "pending" || pending2 == nil || pending2.remaining != pendingImportMaxFiles-1 {
		t.Fatalf("第一个文件后应剩 %d 个名额, got remaining=%v source=%s", pendingImportMaxFiles-1, pending2, source)
	}
	m.consumePending(pending2, "duration")

	// 没用满：口令仍在，继续可传（多工会会连发很多个文件）
	_, source, pending3 := m.resolveInboundImportDate(conv)
	if source != "pending" || pending3 == nil || pending3.remaining != pendingImportMaxFiles-2 {
		t.Fatalf("前两个文件后应剩 %d 个名额, got remaining=%v source=%s", pendingImportMaxFiles-2, pending3, source)
	}
}

func TestPendingImportExpiry(t *testing.T) {
	m := NewManager(nil)
	conv := "c2c:expire"

	m.rememberImportDate(conv, "2026-09-11")
	// 手动把口令改成已过期
	m.mu.Lock()
	m.pending[conv].expiresAt = time.Now().Add(-time.Minute)
	m.mu.Unlock()

	_, source, _ := m.resolveInboundImportDate(conv)
	if source != "yesterday" {
		t.Fatalf("过期口令应失效回退 yesterday, got %s", source)
	}
}

func TestDecodeCSV(t *testing.T) {
	// UTF-8 带 BOM
	if got, err := decodeCSV([]byte("\xEF\xBB\xBF主播名,音浪\n柚子,100")); err != nil || got == "" {
		t.Errorf("UTF-8 解码失败: %v", err)
	}
	// GBK：先编码再解码，对称验证（运营常用 WPS 导出 GBK 文件）
	enc := simplifiedchinese.GB18030.NewEncoder()
	gbk, err := enc.Bytes([]byte("主播名,音浪\n柚子,100"))
	if err != nil {
		t.Fatalf("GBK 编码失败: %v", err)
	}
	got, err := decodeCSV(gbk)
	if err != nil {
		t.Fatalf("GBK 解码失败: %v", err)
	}
	if !strings.Contains(got, "柚子") || !strings.Contains(got, "音浪") {
		t.Errorf("GBK 解码结果不对: %q", got)
	}
}

func TestParseAddAnchor(t *testing.T) {
	cases := []struct {
		text     string
		name     string
		douyinNo string
	}{
		{"柚子-123456", "柚子", "123456"},
		{"柚子－123456", "柚子", "123456"},   // 全角连字符
		{"柚子—123456", "柚子", "123456"},   // em dash
		{"柚子 - 123456", "柚子", "123456"}, // 带空格
		{"小K-douyin.888", "小K", "douyin.888"},
		// 不该命中
		{"9.11", "", ""},      // 日期
		{"柚子 9月", "", ""},     // 无连字符
		{"柚子-abc", "", ""},    // 抖音号太短
		{"123456-柚子", "", ""}, // 姓名含数字开头部分不合法
		{"18号报告", "", ""},     // 日报
	}
	for _, c := range cases {
		name, no := ParseAddAnchor(c.text)
		if name != c.name || no != c.douyinNo {
			t.Errorf("ParseAddAnchor(%q) = (%q,%q), want (%q,%q)", c.text, name, no, c.name, c.douyinNo)
		}
	}
}
