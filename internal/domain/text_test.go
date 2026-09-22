package domain

import "testing"

func TestTruncateRunes(t *testing.T) {
	tests := []struct {
		name string
		s    string
		max  int
		want string
	}{
		{"короче лимита — не трогаем", "привет", 10, "привет"},
		{"ровно по лимиту — без многоточия", "привет", 6, "привет"},
		{"обрезка по рунам, не байтам", "привет мир", 6, "привет…"},
		{"пустая строка", "", 5, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := TruncateRunes(tc.s, tc.max); got != tc.want {
				t.Errorf("TruncateRunes(%q, %d) = %q, хочу %q", tc.s, tc.max, got, tc.want)
			}
		})
	}
}

func TestStripReplyMentionPrefix(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		reply *ReplyTo
		want  string
	}{
		{
			name:  "reply == nil — текст не трогаем",
			text:  "@ivan привет",
			reply: nil,
			want:  "@ivan привет",
		},
		{
			name:  "обычный случай — префикс убран",
			text:  "@ivan привет",
			reply: &ReplyTo{AuthorLogin: "ivan"},
			want:  "привет",
		},
		{
			name:  "без учёта регистра",
			text:  "@Ivan привет",
			reply: &ReplyTo{AuthorLogin: "ivan"},
			want:  "привет",
		},
		{
			name:  "более длинный ник — не совпадение, не трогаем",
			text:  "@ivan2 привет",
			reply: &ReplyTo{AuthorLogin: "ivan"},
			want:  "@ivan2 привет",
		},
		{
			name:  "AuthorLogin пуст — сигнал \"нет ответа\", не трогаем",
			text:  "@ivan привет",
			reply: &ReplyTo{AuthorLogin: ""},
			want:  "@ivan привет",
		},
		{
			name:  "после @Автора сразу конец строки — тоже валидно",
			text:  "@ivan",
			reply: &ReplyTo{AuthorLogin: "ivan"},
			want:  "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := StripReplyMentionPrefix(tc.text, tc.reply); got != tc.want {
				t.Errorf("StripReplyMentionPrefix(%q, %+v) = %q, хочу %q", tc.text, tc.reply, got, tc.want)
			}
		})
	}
}

func TestReplyLineText(t *testing.T) {
	tests := []struct {
		name  string
		reply *ReplyTo
		want  string
	}{
		{
			name:  "есть DisplayName — используем его",
			reply: &ReplyTo{AuthorName: "Ivan", AuthorLogin: "ivan", Text: "привет"},
			want:  "Ответ @Ivan: привет",
		},
		{
			name:  "DisplayName пуст — падаем на Login",
			reply: &ReplyTo{AuthorLogin: "ivan", Text: "привет"},
			want:  "Ответ @ivan: привет",
		},
		{
			name:  "длинный текст — обрезается через TruncateRunes",
			reply: &ReplyTo{AuthorLogin: "ivan", Text: stringOfLength(100, 'x')},
			want:  "Ответ @ivan: " + stringOfLength(60, 'x') + "…",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ReplyLineText(tc.reply); got != tc.want {
				t.Errorf("ReplyLineText(%+v) = %q, хочу %q", tc.reply, got, tc.want)
			}
		})
	}
}

func stringOfLength(n int, r rune) string {
	rs := make([]rune, n)
	for i := range rs {
		rs[i] = r
	}
	return string(rs)
}
