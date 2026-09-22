package domain

import "testing"

func TestIsMentioned(t *testing.T) {
	viewer := User{Login: "ivan", DisplayName: "Ivan_TV"}

	tests := []struct {
		name string
		text string
		want bool
	}{
		{"по логину", "привет @ivan как дела", true},
		{"по DisplayName", "привет @Ivan_TV как дела", true},
		{"без учёта регистра", "привет @IVAN", true},
		{"пунктуация после ника не мешает", "@ivan, привет!", true},
		{"более длинный ник — не совпадение", "@ivan2 привет", false},
		{"обычное упоминание другого", "@someone_else привет", false},
		{"без @ вообще — совпадение по имени не считается", "ivan привет", false},
		{"пустой текст", "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsMentioned(tc.text, viewer); got != tc.want {
				t.Errorf("IsMentioned(%q, %+v) = %v, хочу %v", tc.text, viewer, got, tc.want)
			}
		})
	}

	t.Run("viewer.Login пуст — никогда не совпадает", func(t *testing.T) {
		if IsMentioned("@ivan привет", User{}) {
			t.Error("пустой viewer.Login должен давать false всегда")
		}
	})
}

func TestMentionTokenBefore(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		caret     int
		wantToken string
		wantStart int
		wantOK    bool
	}{
		{"незакрытый токен в начале строки", "@ivan", 5, "ivan", 0, true},
		{"незакрытый токен после пробела", "привет @iv", 10, "iv", 7, true},
		{"токен закрыт пробелом — не автодополняем", "@ivan ", 6, "", 0, false},
		{"только что напечатали @ — токен пуст, но валиден", "@", 1, "", 0, true},
		{"@ не после пробела — обычный текст (email)", "text@example", 12, "", 0, false},
		{"нет @ вообще", "привет", 6, "", 0, false},
		{"caret вне диапазона", "@ivan", 99, "", 0, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			token, start, ok := MentionTokenBefore([]rune(tc.text), tc.caret)
			if token != tc.wantToken || start != tc.wantStart || ok != tc.wantOK {
				t.Errorf("MentionTokenBefore(%q, %d) = (%q, %d, %v), хочу (%q, %d, %v)",
					tc.text, tc.caret, token, start, ok, tc.wantToken, tc.wantStart, tc.wantOK)
			}
		})
	}
}
