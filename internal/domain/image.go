package domain

import (
	"image"
	"image/color"
)

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

// PlaceholderAvatar рисует заглушку аватарки size×size: серый фон и
// светлее — силуэт "голова и плечи". Нужна, чтобы пока настоящая картинка
// скачивается, на её месте не зияла пустота. Без внешних зависимостей и
// без ресурсов в бинарнике: силуэт считается по формулам круга и эллипса.
func PlaceholderAvatar(size int) image.Image {
	bg := color.RGBA{R: 205, G: 205, B: 205, A: 255}
	fg := color.RGBA{R: 232, G: 232, B: 232, A: 255}

	s := float64(size)
	// Голова — круг, плечи — эллипс, уходящий за нижний край.
	headX, headY, headR := s*0.5, s*0.38, s*0.18
	shX, shY, shRX, shRY := s*0.5, s*0.98, s*0.36, s*0.32

	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			// +0.5 — центр пикселя, чтобы силуэт получился симметричным.
			px, py := float64(x)+0.5, float64(y)+0.5

			dx, dy := px-headX, py-headY
			inHead := dx*dx+dy*dy <= headR*headR

			ex, ey := (px-shX)/shRX, (py-shY)/shRY
			inShoulders := ex*ex+ey*ey <= 1

			if inHead || inShoulders {
				img.SetRGBA(x, y, fg)
			} else {
				img.SetRGBA(x, y, bg)
			}
		}
	}
	return img
}
