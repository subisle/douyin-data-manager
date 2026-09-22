package csvparse

import (
	"strings"
	"testing"
)

func TestParseWave(t *testing.T) {
	tests := []struct {
		raw     string
		want    int64
		wantErr bool
	}{
		{"12.5万", 125000, false},
		{"125000", 125000, false},
		{"12,500", 12500, false},
		{"3亿", 300000000, false},
		{"2w", 20000, false},
		{"45512音浪", 45512, false},
		{"12.5万音浪", 125000, false},
		{"3000元", 3000, false},
		{"100钻", 100, false},
		{"88,888音浪", 88888, false},
		{"45512 未知尾巴", 45512, false},
		{"", 0, false},
		{"-", 0, false},
		{"0", 0, false},
		{"abc", 0, true},
	}
	for _, tt := range tests {
		got, err := ParseWave(tt.raw)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseWave(%q) err=%v, wantErr=%v", tt.raw, err, tt.wantErr)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseWave(%q) = %d, 期望 %d", tt.raw, got, tt.want)
		}
	}
}

func TestParseDuration(t *testing.T) {
	tests := []struct {
		raw  string
		want int
	}{
		{"2:30:45", 150},     // 2小时30分45秒
		{"1:30", 90},         // 1小时30分
		{"37小时16分钟5秒", 2236}, // 2236 分钟
		{"1小时30分", 90},
		{"150分钟", 150},
		{"150", 150},
		{"", 0},
	}
	for _, tt := range tests {
		got, err := ParseDuration(tt.raw)
		if err != nil {
			t.Errorf("ParseDuration(%q) 报错: %v", tt.raw, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseDuration(%q) = %d, 期望 %d", tt.raw, got, tt.want)
		}
	}
}

func TestDetectKind(t *testing.T) {
	tests := []struct {
		headers []string
		want    Kind
	}{
		{[]string{"主播ID", "主播昵称", "音浪"}, KindWave},
		{[]string{"主播ID", "有效时长"}, KindDuration},
		{[]string{"主播ID", "开播时长(分钟)"}, KindDuration},
		{[]string{"主播名", "抖音号", "性别"}, KindAnchors},
		{[]string{"foo", "bar"}, KindUnknown},
	}
	for _, tt := range tests {
		if got := DetectKind(tt.headers); got != tt.want {
			t.Errorf("DetectKind(%v) = %q, 期望 %q", tt.headers, got, tt.want)
		}
	}
}

func TestParseMixedHeaders(t *testing.T) {
	// 表头带下划线、括号、空格，必须都能认出来
	in := "主播_ID, 抖音昵称 , 音浪(万)\nA1,柚子,12.5万\nA2,小满,8万\n"
	kind, rows, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if kind != KindWave {
		t.Errorf("类型 = %q, 期望 wave", kind)
	}
	if len(rows) != 2 {
		t.Fatalf("行数 = %d, 期望 2", len(rows))
	}
	if rows[0].AnchorID != "A1" || rows[0].Name != "柚子" {
		t.Errorf("首行 = %+v", rows[0])
	}
	if rows[0].Wave != 125000 {
		t.Errorf("首行音浪 = %d, 期望 125000", rows[0].Wave)
	}
	if rows[1].Wave != 80000 {
		t.Errorf("次行音浪 = %d, 期望 80000", rows[1].Wave)
	}
}

func TestParseDurationCSV(t *testing.T) {
	in := "主播id,时长\nA1,2:30:00\nA2,150分钟\n"
	kind, rows, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if kind != KindDuration {
		t.Errorf("类型 = %q, 期望 duration", kind)
	}
	if rows[0].Minutes != 150 || rows[1].Minutes != 150 {
		t.Errorf("时长解析错误: %d / %d, 期望 150 / 150", rows[0].Minutes, rows[1].Minutes)
	}
}

func TestParseAnchorsCSV(t *testing.T) {
	// 主播名单：只有 姓名 + 抖音号（最常见的运营名单）
	in := "\uFEFF姓名,抖音号\n柚子,123456\n小虎,789\n\n"
	kind, rows, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if kind != KindAnchors {
		t.Errorf("类型 = %q, 期望 anchors", kind)
	}
	if len(rows) != 2 {
		t.Fatalf("行数 = %d, 期望 2", len(rows))
	}
	if rows[0].Name != "柚子" || rows[0].AnchorID != "123456" || rows[0].DouyinNo != "123456" {
		t.Errorf("首行 = %+v", rows[0])
	}
}

func TestParseAnchorsCSVWithIDColumn(t *testing.T) {
	// 带 主播ID 和 抖音号 两列：AnchorID 用主播 ID，DouyinNo 单独保留
	in := "排名,主播ID,抖音号,主播名\n1,111222333,cyl765,浩龙\n"
	kind, rows, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	// 含"音浪/时长"词的才归 wave/duration；此表归 anchors（有主播名/抖音号）
	if kind != KindAnchors {
		t.Errorf("类型 = %q, 期望 anchors", kind)
	}
	if rows[0].AnchorID != "111222333" || rows[0].DouyinNo != "cyl765" || rows[0].Name != "浩龙" {
		t.Errorf("首行 = %+v", rows[0])
	}
}
