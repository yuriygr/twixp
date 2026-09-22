package domain

import "image"

// ResizeNearest уменьшает img до size×size. Нарочно простой
// (nearest-neighbor): для маленькой иконки (аватарка канала, бейдж)
// качество некритично, а тащить внешнюю зависимость
// (golang.org/x/image, ещё один коммит для пиновки под Go 1.10.8) ради
// этого не стоит.
func ResizeNearest(src image.Image, size int) image.Image {
	bounds := src.Bounds()
	sw, sh := bounds.Dx(), bounds.Dy()

	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		sy := bounds.Min.Y + y*sh/size
		for x := 0; x < size; x++ {
			sx := bounds.Min.X + x*sw/size
			dst.Set(x, y, src.At(sx, sy))
		}
	}
	return dst
}
