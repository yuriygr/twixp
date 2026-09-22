package domain

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// TruncateRunes обрезает строку до max рун (а не байт — русский текст
// не должен разваливаться на середине буквы), добавляя многоточие,
// если что-то отрезано.
func TruncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// StripReplyMentionPrefix убирает начальный "@Автор " из текста
// ответа. Twitch сам добавляет такой префикс в текст ЛЮБОГО ответа
// (reply != nil — надёжное тому подтверждение, а не догадка по
// содержимому) — раз кому отвечали, теперь показывается отдельной
// серой строкой сверху (см. chatLine.ReplyTo в internal/ui,
// chatView.layoutLine), повторять то же самое ещё и в начале текста
// незачем.
//
// Вызывается ПОСЛЕ IsMentioned (см. appendMessage в chatpane.go), не
// до: если родитель сообщения — сам viewer, "@viewer" в начале обязан
// остаться настоящим упоминанием при подсчёте Mentioned, а не исчезнуть
// вместе с этой чисто отображательной обрезкой.
func StripReplyMentionPrefix(text string, reply *ReplyTo) string {
	if reply == nil || reply.AuthorLogin == "" {
		return text
	}

	prefix := "@" + reply.AuthorLogin
	if !strings.HasPrefix(strings.ToLower(text), strings.ToLower(prefix)) {
		return text
	}

	rest := text[len(prefix):]
	if rest != "" {
		r, size := utf8.DecodeRuneInString(rest)
		if !unicode.IsSpace(r) {
			// Дальше идут ещё буквы/цифры того же слова — то есть это
			// более длинный ник, случайно начинающийся с тех же
			// символов, а не искомое "@Автор" целиком.
			return text
		}
		rest = rest[size:]
	}

	return strings.TrimLeft(rest, " ")
}

// ReplyLineText — текст серой строки над сообщением-ответом. Тот же
// формат "Ответ Автору: текст", что уже использует replyBanner над
// полем ввода (см. chatPane.startReply в internal/ui) — единообразно
// с тем, что пользователь уже видел там, когда сам нажимал "Ответить".
func ReplyLineText(reply *ReplyTo) string {
	author := reply.AuthorName
	if author == "" {
		author = reply.AuthorLogin
	}
	return "Ответ @" + author + ": " + TruncateRunes(reply.Text, 60)
}
