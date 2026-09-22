package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sampleReport(gender string) Report {
	return Report{
		Date:               "2026-09-19",
		Gender:             gender,
		Columns:            DefaultColumns(),
		PageIndex:          1,
		PageCount:          1,
		ShowInactiveFooter: true,
		InactiveLines:      []string{"薇笑: 大鹏、青禾  /  无师傅: 南风、木子、阿岩"},
		Rows: []Row{
			{Name: "柚子", DailyWave: 386000, TotalWave: 15560000, DurationMinutes: 320, Tier: "B", IsLive: true, MasterName: "薇笑"},
			{Name: "小满", DailyWave: 214000, TotalWave: 9210000, DurationMinutes: 280, Tier: "B", IsLive: true, MasterName: "薇笑"},
			{Name: "初夏", DailyWave: 96000, TotalWave: 4130000, DurationMinutes: 190, Tier: "C", IsLive: true, MasterName: "薇笑"},
			{Name: "老K", NotLiveDays: 3, DailyWave: 0, TotalWave: 8720000, Tier: "C", IsLive: false, MasterName: "薇笑"},
			{Name: "南风", NotLiveDays: 2, TotalWave: 3210000, Tier: "D", IsLive: false},
			{Name: "阿岩", NotLiveDays: 1, TotalWave: 1180000, Tier: "D", IsLive: false},
		},
	}
}

// TestRenderGolden 生成两套样式的样例 SVG 并落盘，同时断言关键配色没被改坏。
//
// 这些颜色是 615 运营认过的样子，改了就等于图变样了。
func TestRenderGolden(t *testing.T) {
	dir := filepath.Join("testdata")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建 testdata 失败: %v", err)
	}

	cases := []struct {
		name   string
		style  Style
		gender string
		want   []string
	}{
		{
			name:   "classic",
			style:  StyleClassic,
			gender: "female",
			want: []string{
				`#1E293B`, // 标题栏
				`#E2E8F0`, // 表头
				`#FEF2F2`, // 未播行
				`#DC2626`, // 未播左侧竖条
				`#DBEAFE`, // 进度条轨道
				`#60A5FA`, // 进度条填充
				`#FEF3C7`, // 第一名
				`薇笑传媒主播数据统计`,
			},
		},
		{
			name:   "apple",
			style:  StyleApple,
			gender: "male",
			want: []string{
				`#007AFF`,     // 进度条填充（apple 主色）
				`#EAF3FF`,     // 进度条轨道
				`#F2F4F7`,     // 表头
				`#FFF7F7`,     // 未播行
				`#F04438`,     // 未播左侧标记
				`内部数据 · 请勿外传`, // 水印
				`星嗨艺创主播数据统计`,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svg := string(RenderSVG(sampleReport(tc.gender), tc.style))
			for _, want := range tc.want {
				if !strings.Contains(svg, want) {
					t.Errorf("%s 缺少 %q，样式可能被改坏了", tc.name, want)
				}
			}
			path := filepath.Join(dir, tc.name+".svg")
			if err := os.WriteFile(path, []byte(svg), 0o644); err != nil {
				t.Fatalf("写出样例失败: %v", err)
			}
			t.Logf("已生成 %s（%d 字节）", path, len(svg))
		})
	}
}

func TestFormatWave(t *testing.T) {
	tests := []struct {
		in   int64
		want string
	}{
		{0, "0"},
		{-5, "0"},
		{9999, "9,999"},
		{10000, "1 万"},
		{12500, "1.3 万"},
		{386000, "38.6 万"},
		{100000000, "1 亿"},
		{1556000, "155.6 万"},
		{15560000, "1556 万"},
	}
	for _, tt := range tests {
		if got := FormatWave(tt.in); got != tt.want {
			t.Errorf("FormatWave(%d) = %q, 期望 %q", tt.in, got, tt.want)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	if got := FormatDuration(320); got != "5小时20分" {
		t.Errorf("FormatDuration(320) = %q", got)
	}
	if got := FormatDuration(45); got != "45分钟" {
		t.Errorf("FormatDuration(45) = %q", got)
	}
}

func TestResolveStyle(t *testing.T) {
	if ResolveStyle("female", "") != StyleClassic {
		t.Error("女队应默认 classic")
	}
	if ResolveStyle("male", "") != StyleApple {
		t.Error("男团应默认 apple")
	}
	if ResolveStyle("male", "classic") != StyleClassic {
		t.Error("显式指定应覆盖默认值")
	}
}

func TestParseColumnSet(t *testing.T) {
	set, err := ParseColumnSet("rank,name,dailyWave,duration")
	if err != nil {
		t.Fatalf("合法组合报错: %v", err)
	}
	if !set.Rank || !set.Name || !set.DailyWave || !set.Duration {
		t.Error("勾选列未生效")
	}
	if set.TotalWave || set.NotLiveDays {
		t.Error("未勾选列不应生效")
	}
	if _, err := ParseColumnSet("rank,bogus"); err == nil {
		t.Error("未知列应报错")
	}
	if _, err := ParseColumnSet(","); err == nil {
		t.Error("空组合应报错")
	}
}
