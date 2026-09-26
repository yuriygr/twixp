package domain

import "unicode/utf16"

// RuneIndexFromUTF16 переводит caretUTF16 — позицию в UTF-16 code
// units, как её возвращают сырые win32-сообщения EM_GETSEL/EM_SETSEL
// (см. walk.LineEdit.TextSelection/SetTextSelection в
// internal/ui/chatpane.go) — в индекс той же позиции в рунах s.
//
// Для символов из Basic Multilingual Plane (вся кириллица, латиница,
// обычная пунктуация) единицы совпадают один в один, поэтому раньше
// разницу можно было не замечать. Расхождение — только на суррогатных
// парах (большинство эмодзи и некоторые редкие символы вне BMP):
// каждая такая пара занимает 2 UTF-16 code units, но всего 1 руну, и
// без этой конвертации caret из TextSelection нельзя напрямую
// использовать как индекс в []rune(text) — после эмодзи он будет
// систематически "убегать" вперёд.
func RuneIndexFromUTF16(s string, caretUTF16 int) int {
	if caretUTF16 <= 0 {
		return 0
	}

	units := utf16.Encode([]rune(s))
	if caretUTF16 >= len(units) {
		return len([]rune(s))
	}

	return len(utf16.Decode(units[:caretUTF16]))
}

// UTF16IndexFromRune — обратное преобразование к RuneIndexFromUTF16:
// позиция runeIndex (индекс в рунах s) в UTF-16 code units, как их
// ожидает walk.LineEdit.SetTextSelection.
func UTF16IndexFromRune(s string, runeIndex int) int {
	r := []rune(s)
	if runeIndex <= 0 {
		return 0
	}
	if runeIndex >= len(r) {
		runeIndex = len(r)
	}

	return len(utf16.Encode(r[:runeIndex]))
}
