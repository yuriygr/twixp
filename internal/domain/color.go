package domain

import "strconv"

// Color — цвет в чистом RGB, без завязки на конкретный GUI-тулкит.
// internal/domain не может знать про walk.Color (это утащило бы за
// собой Windows-сборку туда, где её быть не должно) — ui сам
// оборачивает результат через walk.RGB(c.R, c.G, c.B) на месте
// вызова.
type Color struct {
	R, G, B byte
}

// defaultNickColors — палитра для зрителей, которые не задали себе
// цвет ника в Twitch (Color == ""). Тот же набор из 15 цветов, что
// использует сам веб-клиент Twitch в этом случае — так поведение не
// выглядит придуманным нами произвольно, и цвет одного и того же
// зрителя выглядит привычно тем, кто видел его в других клиентах.
var defaultNickColors = []Color{
	{255, 0, 0}, {0, 0, 255}, {0, 255, 0},
	{178, 34, 34}, {255, 127, 80}, {154, 205, 50},
	{255, 69, 0}, {46, 139, 87}, {218, 165, 32},
	{210, 105, 30}, {95, 158, 160}, {30, 144, 255},
	{255, 105, 180}, {138, 43, 226}, {0, 255, 127},
}

// NicknameColor — цвет ника для сообщения. Если у зрителя есть
// собственный цвет в Twitch (пришёл прямо в EventSub-событии, см.
// eventsub/message.go), используем его. Если нет — детерминированный
// хеш от ID (или логина, если ID почему-то пуст) по палитре выше: без
// единого сетевого запроса, и один и тот же зритель всегда получает
// один и тот же цвет в рамках сессии (и не только — хеш от ID не
// меняется между запусками).
func NicknameColor(userID, login, twitchColor string) Color {
	if c, ok := ParseHexColor(twitchColor); ok {
		return c
	}

	key := userID
	if key == "" {
		key = login
	}
	if key == "" {
		return defaultNickColors[0]
	}

	return defaultNickColors[fnv32a(key)%uint32(len(defaultNickColors))]
}

// fnv32a — тот же алгоритм, что и hash/fnv.New32a(), но без лишней
// зависимости от интерфейса hash.Hash ради одного Write+Sum32.
func fnv32a(s string) uint32 {
	const (
		offset32 = 2166136261
		prime32  = 16777619
	)
	h := uint32(offset32)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= prime32
	}
	return h
}

// ParseHexColor разбирает цвет в формате Twitch ("#RRGGBB"). ok=false
// для пустой строки (зритель цвет не задавал) или любого неожиданного
// формата — вызывающий код в этом случае берёт цвет из палитры (см.
// NicknameColor).
func ParseHexColor(s string) (Color, bool) {
	if len(s) != 7 || s[0] != '#' {
		return Color{}, false
	}
	v, err := strconv.ParseUint(s[1:], 16, 32)
	if err != nil {
		return Color{}, false
	}
	return Color{R: byte(v >> 16), G: byte(v >> 8), B: byte(v)}, true
}
