package main

import (
	"github.com/mat/besticon/v3/ico"
	"golang.org/x/image/bmp"
	"golang.org/x/image/webp"
	"image"
	"image/color"
	"image/draw"
)

func PatchIcon(resolvedIcon *ResolvedIcon) (*image.RGBA64, error) {
	var icon image.Image
	var err error

	switch resolvedIcon.Type {
	case Ico:
		icon, err = ico.Decode(resolvedIcon.Body)
	case Gif:
		fallthrough
	case Png:
		fallthrough
	case Jpg:
		icon, _, err = image.Decode(resolvedIcon.Body)
	case Webp:
		icon, err = webp.Decode(resolvedIcon.Body)
	case Bmp:
		icon, err = bmp.Decode(resolvedIcon.Body)
	}

	if err != nil {
		return nil, err
	}

	return convertImage(icon), nil
}

const Ish = 650

func convertImage(icon image.Image) *image.RGBA64 {
	bounds := icon.Bounds()
	rgba := image.NewRGBA64(bounds)

	draw.Draw(rgba, bounds, icon, bounds.Min, draw.Over)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.Y; x++ {
			r, g, b, a := rgba.At(x, y).RGBA()

			if r >= 0xFFFF-Ish && g >= 0xFFFF-Ish && b >= 0xFFFF-Ish {
				rgba.Set(x, y, color.RGBA{0, 0, 0, 0})
			} else if r == 0 && g == 0 && b == 0 {
				rgba.Set(x, y, color.RGBA{0, 0, 0, 0})
			} else {
				rgba.Set(x, y, color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), uint8(a >> 8)})
			}
		}
	}

	return rgba
}
