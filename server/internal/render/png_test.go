package render

import (
	"bytes"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// pngSampleRows 造一批覆盖各种状态的样例行。
func pngSampleRows() []Row {
	return []Row{
		{Name: "小鹿", NotLiveDays: 0, DailyWave: 1_234_567, TotalWave: 234_567_890, IsLive: true},
		{Name: "阿柚Yoyo", NotLiveDays: 0, DailyWave: 987_654, TotalWave: 98_765_432, IsLive: true},
		{Name: "Nana酱", NotLiveDays: 0, DailyWave: 456_789, TotalWave: 56_789_000, IsLive: true},
		{Name: "栗子", NotLiveDays: 0, DailyWave: 12_345, TotalWave: 12_345_678, IsLive: true},
		{Name: "Momo", NotLiveDays: 3, DailyWave: 0, TotalWave: 8_888_888, IsLive: false},
	}
}

func TestRenderPNGClassic(t *testing.T) {
	rows := pngSampleRows()
	report := Report{
		Date: "2026-09-19", Gender: "female", Rows: rows, Stats: rows,
		Columns: DefaultColumns(), PageIndex: 1, PageCount: 1, ShowInactiveFooter: true,
	}
	data, err := RenderPNG(report, StyleClassic)
	if err != nil {
		t.Fatalf("classic PNG: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("解码 PNG 失败: %v", err)
	}
	if img.Bounds().Dx() < 520 || img.Bounds().Dy() < 200 {
		t.Fatalf("尺寸异常: %v", img.Bounds())
	}
	out := filepath.Join(os.TempDir(), "report-classic.png")
	if err := os.WriteFile(out, data, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("classic: %s %v", out, img.Bounds())
}

func TestRenderPNGApple(t *testing.T) {
	rows := pngSampleRows()
	report := Report{
		Date: "2026-09-19", Gender: "male", Rows: rows, Stats: rows,
		Columns: DefaultColumns(), PageIndex: 1, PageCount: 1, ShowInactiveFooter: true,
	}
	data, err := RenderPNG(report, StyleApple)
	if err != nil {
		t.Fatalf("apple PNG: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("解码 PNG 失败: %v", err)
	}
	out := filepath.Join(os.TempDir(), "report-apple.png")
	if err := os.WriteFile(out, data, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("apple: %s %v", out, img.Bounds())
}
