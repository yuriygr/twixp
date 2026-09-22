package domain

import (
	"image"
	"image/color"
	"testing"
)

func TestResizeNearest(t *testing.T) {
	t.Run("размер результата — ровно size×size", func(t *testing.T) {
		src := image.NewRGBA(image.Rect(0, 0, 64, 32))
		dst := ResizeNearest(src, 16)

		b := dst.Bounds()
		if b.Dx() != 16 || b.Dy() != 16 {
			t.Errorf("размер результата = %dx%d, хочу 16x16", b.Dx(), b.Dy())
		}
	})

	t.Run("однотонная картинка остаётся однотонной", func(t *testing.T) {
		want := color.RGBA{R: 10, G: 20, B: 30, A: 255}
		src := image.NewRGBA(image.Rect(0, 0, 8, 8))
		for y := 0; y < 8; y++ {
			for x := 0; x < 8; x++ {
				src.Set(x, y, want)
			}
		}

		dst := ResizeNearest(src, 4)
		for y := 0; y < 4; y++ {
			for x := 0; x < 4; x++ {
				got := dst.At(x, y)
				r, g, b, a := got.RGBA()
				wr, wg, wb, wa := want.RGBA()
				if r != wr || g != wg || b != wb || a != wa {
					t.Fatalf("dst.At(%d,%d) = %+v, хочу %+v", x, y, got, want)
				}
			}
		}
	})

	t.Run("не паникует на прямоугольнике со смещённым Min", func(t *testing.T) {
		src := image.NewRGBA(image.Rect(5, 5, 21, 21)) // 16x16, но Min != (0,0)
		dst := ResizeNearest(src, 8)
		if dst.Bounds().Dx() != 8 || dst.Bounds().Dy() != 8 {
			t.Errorf("размер результата = %v, хочу 8x8", dst.Bounds())
		}
	})
}
