package domain

import "testing"

func TestParseHexColor(t *testing.T) {
	tests := []struct {
		name string
		s    string
		want Color
		ok   bool
	}{
		{"обычный цвет", "#1E90FF", Color{R: 0x1E, G: 0x90, B: 0xFF}, true},
		{"чёрный", "#000000", Color{0, 0, 0}, true},
		{"белый", "#FFFFFF", Color{255, 255, 255}, true},
		{"пустая строка — цвет не задан", "", Color{}, false},
		{"нет решётки", "1E90FF", Color{}, false},
		{"неверная длина", "#1E90F", Color{}, false},
		{"не hex", "#GGGGGG", Color{}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseHexColor(tc.s)
			if ok != tc.ok || (ok && got != tc.want) {
				t.Errorf("ParseHexColor(%q) = (%+v, %v), хочу (%+v, %v)", tc.s, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestNicknameColor(t *testing.T) {
	t.Run("собственный цвет Twitch — используем его", func(t *testing.T) {
		got := NicknameColor("123", "ivan", "#1E90FF")
		want := Color{0x1E, 0x90, 0xFF}
		if got != want {
			t.Errorf("NicknameColor(...) = %+v, хочу %+v", got, want)
		}
	})

	t.Run("нет своего цвета — детерминированный выбор из палитры", func(t *testing.T) {
		a := NicknameColor("123", "ivan", "")
		b := NicknameColor("123", "ivan", "")
		if a != b {
			t.Errorf("NicknameColor должен быть детерминированным: %+v != %+v", a, b)
		}

		found := false
		for _, c := range defaultNickColors {
			if c == a {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("NicknameColor(...) = %+v — не входит в палитру", a)
		}
	})

	t.Run("ID пуст — падаем на login", func(t *testing.T) {
		byID := NicknameColor("someID", "someID", "")
		byLogin := NicknameColor("", "someID", "")
		if byID != byLogin {
			t.Errorf("одинаковый ключ по ID и по login должен давать одинаковый цвет: %+v != %+v", byID, byLogin)
		}
	})

	t.Run("и ID, и login пусты — не паникуем", func(t *testing.T) {
		got := NicknameColor("", "", "")
		if got != defaultNickColors[0] {
			t.Errorf("NicknameColor(\"\",\"\",\"\") = %+v, хочу первый цвет палитры %+v", got, defaultNickColors[0])
		}
	})
}
