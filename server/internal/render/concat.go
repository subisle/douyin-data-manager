// 纵向拼接 PNG：日报把女团+男团两个工会合并成单张长图发送用。
//
// 两个团各走各的版式（classic/apple）渲染成 PNG，再逐像素拼到一张画布上；
// 窄图水平居中，两侧留白用 #F8FAFC 画布底色补齐。两段样式互不干扰，
// 不用为"合并"重排版式。
package render

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
)

// ConcatVerticalPNG 把多张 PNG 纵向拼成一张，窄图水平居中。至少传一张。
func ConcatVerticalPNG(images ...[]byte) ([]byte, error) {
	if len(images) == 0 {
		return nil, fmt.Errorf("没有可拼接的图片")
	}
	decoded := make([]image.Image, 0, len(images))
	maxW, totalH := 0, 0
	for i, b := range images {
		img, err := png.Decode(bytes.NewReader(b))
		if err != nil {
			return nil, fmt.Errorf("解码第 %d 张图片失败: %w", i+1, err)
		}
		bounds := img.Bounds()
		if bounds.Dx() > maxW {
			maxW = bounds.Dx()
		}
		totalH += bounds.Dy()
		decoded = append(decoded, img)
	}
	if maxW <= 0 || totalH <= 0 {
		return nil, fmt.Errorf("图片尺寸异常")
	}

	canvas := image.NewRGBA(image.Rect(0, 0, maxW, totalH))
	draw.Draw(canvas, canvas.Bounds(),
		&image.Uniform{color.RGBA{R: 0xF8, G: 0xFA, B: 0xFC, A: 255}}, image.Point{}, draw.Src)

	y := 0
	for _, img := range decoded {
		bounds := img.Bounds()
		x := (maxW - bounds.Dx()) / 2
		draw.Draw(canvas, image.Rect(x, y, x+bounds.Dx(), y+bounds.Dy()),
			img, image.Point{}, draw.Src)
		y += bounds.Dy()
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, canvas); err != nil {
		return nil, fmt.Errorf("编码拼接图失败: %w", err)
	}
	return buf.Bytes(), nil
}
