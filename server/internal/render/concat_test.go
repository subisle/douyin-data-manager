package render

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func testPNG(t *testing.T, w, h int, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("编码测试图失败: %v", err)
	}
	return buf.Bytes()
}

func pngHeight(t *testing.T, data []byte) int {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	return img.Bounds().Dy()
}

func TestConcatVerticalPNG(t *testing.T) {
	a := testPNG(t, 10, 8, color.RGBA{R: 255, A: 255})
	b := testPNG(t, 6, 4, color.RGBA{G: 255, A: 255})

	got, err := ConcatVerticalPNG(a, b)
	if err != nil {
		t.Fatalf("拼接失败: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(got))
	if err != nil {
		t.Fatalf("解码拼接结果失败: %v", err)
	}
	bounds := img.Bounds()
	if bounds.Dx() != 10 || bounds.Dy() != 12 {
		t.Fatalf("拼接尺寸 = %dx%d，期望 10x12", bounds.Dx(), bounds.Dy())
	}
	// 窄图水平居中：(10-6)/2 = 2，第二段起点应是纯绿
	r, g, bl, _ := img.At(2, 8).RGBA()
	if g == 0 || r != 0 || bl != 0 {
		t.Errorf("第二段未水平居中：(2,8) 处颜色 = (%d,%d,%d)", r>>8, g>>8, bl>>8)
	}
	// 左侧留白应是 #F8FAFC 画布底色
	r2, g2, b2, _ := img.At(0, 8).RGBA()
	if r2 != 0xF8*0x101 || g2 != 0xFA*0x101 || b2 != 0xFC*0x101 {
		t.Errorf("留白底色异常: (%d,%d,%d)", r2>>8, g2>>8, b2>>8)
	}

	if _, err := ConcatVerticalPNG(); err == nil {
		t.Error("空入参应报错")
	}
}

// TestConcatVerticalPNGWithRendered 用真实渲染产物做集成验证：
// 合并图高度必须等于两段之和，防止将来改版式时把拼接弄坏。
func TestConcatVerticalPNGWithRendered(t *testing.T) {
	p1, err := RenderPNG(sampleReport("female"), StyleClassic)
	if err != nil {
		t.Fatalf("渲染女团失败: %v", err)
	}
	p2, err := RenderPNG(sampleReport("male"), StyleApple)
	if err != nil {
		t.Fatalf("渲染男团失败: %v", err)
	}
	h1, h2 := pngHeight(t, p1), pngHeight(t, p2)

	merged, err := ConcatVerticalPNG(p1, p2)
	if err != nil {
		t.Fatalf("拼接失败: %v", err)
	}
	if got := pngHeight(t, merged); got != h1+h2 {
		t.Errorf("合并高度 = %d，期望 %d+%d = %d", got, h1, h2, h1+h2)
	}
}
