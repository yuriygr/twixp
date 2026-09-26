package domain

import (
	"testing"
	"unicode/utf16"
)

func TestRuneIndexFromUTF16(t *testing.T) {
	// "a😀b" — 'a' (1 рун/1 UTF-16), 😀 U+1F600 (1 руна, но 2 UTF-16
	// code unit — суррогатная пара), 'b' (1/1). В рунах: a=0, 😀=1, b=2.
	// В UTF-16: a=0, 😀=1..2, b=3.
	const s = "a😀b"

	tests := []struct {
		name       string
		caretUTF16 int
		want       int
	}{
		{"до эмодзи", 1, 1},
		{"сразу после эмодзи (2 code unit)", 3, 2},
		{"в самом конце строки", 4, 3},
		{"каретка за концом строки — не паникуем, клампим", 99, 3},
		{"отрицательная каретка — 0", -1, 0},
		{"каретка в начале", 0, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := RuneIndexFromUTF16(s, tc.caretUTF16); got != tc.want {
				t.Errorf("RuneIndexFromUTF16(%q, %d) = %d, хочу %d", s, tc.caretUTF16, got, tc.want)
			}
		})
	}
}

func TestUTF16IndexFromRune(t *testing.T) {
	const s = "a😀b"

	tests := []struct {
		name      string
		runeIndex int
		want      int
	}{
		{"до эмодзи", 1, 1},
		{"сразу после эмодзи", 2, 3},
		{"конец строки", 3, 4},
		{"за концом строки — клампим", 99, 4},
		{"отрицательный индекс — 0", -1, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := UTF16IndexFromRune(s, tc.runeIndex); got != tc.want {
				t.Errorf("UTF16IndexFromRune(%q, %d) = %d, хочу %d", s, tc.runeIndex, got, tc.want)
			}
		})
	}
}

func TestUTF16RoundTrip(t *testing.T) {
	// Для любой позиции в UTF-16 (не разрезающей суррогатную пару)
	// туда-обратно должно возвращать исходное значение.
	const s = "привет 😀 мир 🚀!"

	units := utf16.Encode([]rune(s))
	for utf16Pos := 0; utf16Pos <= len(units); utf16Pos++ {
		// Суррогатная пара — второй code unit в диапазоне 0xDC00-0xDFFF;
		// позиция ВНУТРИ пары не может прийти от реального EM_GETSEL
		// (Windows не ставит туда каретку), поэтому такие utf16Pos в
		// round-trip тесте пропускаем.
		if utf16Pos > 0 && utf16Pos < len(units) {
			c := units[utf16Pos]
			if c >= 0xDC00 && c <= 0xDFFF {
				continue
			}
		}

		runeIdx := RuneIndexFromUTF16(s, utf16Pos)
		back := UTF16IndexFromRune(s, runeIdx)
		if back != utf16Pos {
			t.Errorf("round-trip сломался на utf16Pos=%d: RuneIndexFromUTF16 -> %d -> UTF16IndexFromRune -> %d", utf16Pos, runeIdx, back)
		}
	}
}
