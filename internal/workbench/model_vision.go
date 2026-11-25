package workbench

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/draw"
	"image/png"
	"strings"

	"research-agent/internal/inference"
)

const visionModelID = "vision"
const visionProbePrompt = "数一数图中左列和右列各有多少个黑色方块。先写左列数量，再写右列数量，只用逗号分隔两个数字。"

func checkVisionModel(ctx context.Context, model *inference.Client) (bool, error) {
	picture := image.NewGray(image.Rect(0, 0, 256, 256))
	draw.Draw(picture, picture.Bounds(), image.White, image.Point{}, draw.Src)
	for index, count := range []int{2, 3} {
		x := 44 + 128*index
		for row := range count {
			y := 38 + 66*row
			draw.Draw(picture, image.Rect(x, y, x+40, y+40), image.Black, image.Point{}, draw.Src)
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, picture); err != nil {
		return false, err
	}
	var reply strings.Builder
	err := model.StreamImage(ctx, visionProbePrompt, "png", encoded.Bytes(), 256, func(text string) error {
		if len(text) > 256-reply.Len() {
			return errors.New("视觉诊断回复过长")
		}
		reply.WriteString(text)
		return nil
	})
	if err != nil {
		return false, err
	}
	left, right, found := strings.Cut(strings.ReplaceAll(reply.String(), "，", ","), ",")
	return found && strings.TrimSpace(left) == "2" && strings.TrimSpace(right) == "3", nil
}
