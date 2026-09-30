package domain

import "fmt"

// ChatModes — ограничения чата канала, которые видит зритель: кто и
// как часто может писать (EventSub channel.chat_settings.update и Get
// Chat Settings). Нулевое значение — "чат без ограничений".
//
// Поля, доступные только модератору (например, задержка чата для
// не-модераторов), сюда сознательно не входят — зритель их всё равно
// не получит.
type ChatModes struct {
	EmoteOnly       bool // писать можно только смайликами
	SubscribersOnly bool // только подписчики

	FollowersOnly    bool // только фолловеры...
	FollowersMinutes int  // ...которые подписаны не меньше стольки минут (0 — любые)

	SlowMode    bool // между сообщениями нужна пауза...
	SlowSeconds int  // ...в столько секунд

	UniqueOnly bool // повторять одно и то же сообщение нельзя
}

// Any сообщает, есть ли хоть одно ограничение.
func (m ChatModes) Any() bool {
	return m.EmoteOnly || m.SubscribersOnly || m.FollowersOnly || m.SlowMode || m.UniqueOnly
}

// Labels возвращает короткие подписи активных ограничений в
// фиксированном порядке (пустой список, если чат свободный) — для
// плашек над полем ввода.
func (m ChatModes) Labels() []string {
	var labels []string

	if m.EmoteOnly {
		labels = append(labels, "Только смайлики")
	}
	if m.SubscribersOnly {
		labels = append(labels, "Только подписчики")
	}
	if m.FollowersOnly {
		if m.FollowersMinutes > 0 {
			labels = append(labels, fmt.Sprintf("Только фолловеры (%s)", formatMinutes(m.FollowersMinutes)))
		} else {
			labels = append(labels, "Только фолловеры")
		}
	}
	if m.SlowMode {
		if m.SlowSeconds > 0 {
			labels = append(labels, fmt.Sprintf("Медленный режим: %s", formatSeconds(m.SlowSeconds)))
		} else {
			labels = append(labels, "Медленный режим")
		}
	}
	if m.UniqueOnly {
		labels = append(labels, "Только уникальные сообщения")
	}

	return labels
}

// formatMinutes — "10 мин", "2 ч", "30 д": самая крупная единица, в
// которую значение делится нацело (Twitch даёт готовые варианты от
// минут до месяцев, так что "90 мин" превращать в "1,5 ч" незачем).
func formatMinutes(min int) string {
	switch {
	case min%1440 == 0:
		return fmt.Sprintf("%d д", min/1440)
	case min%60 == 0:
		return fmt.Sprintf("%d ч", min/60)
	default:
		return fmt.Sprintf("%d мин", min)
	}
}

// formatSeconds — "30 с" или "2 мин" (Twitch допускает паузу до 120 с).
func formatSeconds(sec int) string {
	if sec >= 60 && sec%60 == 0 {
		return fmt.Sprintf("%d мин", sec/60)
	}
	return fmt.Sprintf("%d с", sec)
}
