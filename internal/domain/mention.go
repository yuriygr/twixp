package domain

import (
	"strings"
	"unicode"
)

// isMentionRune — допустимый символ внутри логина/отображаемого имени
// для целей IsMentioned/MentionTokenBefore: буквы, цифры,
// подчёркивание. Не обязан в точности повторять реальные правила
// Twitch на допустимые логины — нужен только чтобы отличить "@логин"
// от "@логин," и не более того.
func isMentionRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// IsMentioned сообщает, упоминает ли текст сообщения viewer'а через
// "@логин" или "@ОтображаемоеИмя" — так Twitch сам подсвечивает
// упоминания в вебе, и веб-клиент, откликаясь на клик по нику, вставляет
// именно один из этих двух вариантов. Пустой viewer.Login (до входа
// или пока viewer ещё не выставлен) — сигнал "сравнивать не с чем",
// сообщение никогда не считается упоминанием.
func IsMentioned(text string, viewer User) bool {
	if viewer.Login == "" {
		return false
	}

	for _, word := range strings.Fields(text) {
		if !strings.HasPrefix(word, "@") {
			continue
		}
		// Обрезаем случайную пунктуацию по краям ("@логин," "@логин:" и
		// т.п.) — просто хвостовой знак препинания в предложении не
		// должен мешать сравнению.
		name := strings.TrimFunc(word[len("@"):], func(r rune) bool {
			return !isMentionRune(r)
		})
		if name == "" {
			continue
		}
		if strings.EqualFold(name, viewer.Login) {
			return true
		}
		if viewer.DisplayName != "" && strings.EqualFold(name, viewer.DisplayName) {
			return true
		}
	}

	return false
}

// MentionTokenBefore ищет незакрытое "@токен" непосредственно перед
// caret (в рунах — вызывающий код сам решает, что считать текстом,
// см. onInputTextChanged в chatpane.go, почему не в байтах).
// "Незакрытое" — значит caret стоит сразу после последней буквы токена,
// без пробела между ними: "@ivan|" (| — курсор) даёт token="ivan",
// а "@ivan |" уже нет (пробел завершил токен, дальше это просто текст).
//
// "@" засчитывается только в начале сообщения или после пробела —
// иначе "text@example.com" тоже попал бы под автодополнение, а это
// обычный текст, не упоминание.
func MentionTokenBefore(text []rune, caret int) (token string, start int, ok bool) {
	if caret < 0 || caret > len(text) {
		return "", 0, false
	}

	i := caret
	for i > 0 && isMentionRune(text[i-1]) {
		i--
	}

	if i == 0 || text[i-1] != '@' {
		return "", 0, false
	}
	atPos := i - 1

	if atPos > 0 && !unicode.IsSpace(text[atPos-1]) {
		return "", 0, false
	}

	return string(text[i:caret]), atPos, true
}
