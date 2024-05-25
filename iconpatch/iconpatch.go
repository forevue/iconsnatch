package iconpatch

import (
	"image"
	"image/color"
)

func isWhiteish(r, g, b float64) bool {
	return (((0.2126*r + 0.7152*g + 0.0722*b) / 255) * 100) >= 95
}

func Patch(icon image.Image) *image.NRGBA64 {
	bounds := icon.Bounds()
	rgba := image.NewNRGBA64(bounds)

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r32, g32, b32, a32 := icon.At(x, y).RGBA()
			rF, gF, bF, aF := float64(r32>>8), float64(g32>>8), float64(b32>>8), float64(a32>>8)
			r, g, b, a := uint8(rF), uint8(gF), uint8(bF), uint8(aF)

			if isWhiteish(rF, gF, bF) || a <= 5 {
				rgba.Set(x, y, color.RGBA{255, 255, 255, 0})
			} else {
				rgba.Set(x, y, color.RGBA{r, g, b, a})
			}
		}
	}

	return rgba
}
