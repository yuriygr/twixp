package domain

// PointsRedemption — какое действие за баллы канала оплачено у
// сообщения. Значения взаимоисключающие (у сообщения один
// message_type), поэтому это перечисление, а не набор булевых флагов.
type PointsRedemption int

const (
	// NoRedemption — обычное сообщение.
	NoRedemption PointsRedemption = iota
	// RedeemedHighlight — "Выделить моё сообщение" (EventSub
	// message_type == "channel_points_highlighted").
	RedeemedHighlight
	// RedeemedSubOnly — "Написать в режиме только для подписчиков":
	// не-подписчик оплатил баллами право написать, пока в чате
	// включён режим "Только подписчики" (EventSub message_type ==
	// "channel_points_sub_only").
	RedeemedSubOnly
)

// Label — подпись для серой строки над таким сообщением. Пусто для
// NoRedemption (баннера нет).
func (r PointsRedemption) Label() string {
	switch r {
	case RedeemedHighlight:
		return "Использовано: Выделить моё сообщение"
	case RedeemedSubOnly:
		return "Использовано: Сообщение в режиме «Только подписчики»"
	default:
		return ""
	}
}

// PointsRedemptionFromMessageType сопоставляет EventSub message_type
// сообщения действию за баллы. Остальные типы ("text", "user_intro" и
// т.п.) — NoRedemption: их сейчас не различаем.
func PointsRedemptionFromMessageType(messageType string) PointsRedemption {
	switch messageType {
	case "channel_points_highlighted":
		return RedeemedHighlight
	case "channel_points_sub_only":
		return RedeemedSubOnly
	default:
		return NoRedemption
	}
}
