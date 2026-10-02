package domain

import (
	"fmt"
	"time"
)

// UserProfile — публичные данные пользователя Twitch, которые окно
// "Профиль пользователя" берёт из Get Users (права не нужны). Не путать
// с User: тот — минимум, необходимый чату (ID, логин, имя, цвет), а это
// — всё, что стоит показать человеку, посмотревшему на профиль.
type UserProfile struct {
	ID          string
	Login       string
	DisplayName string
	AvatarURL   string
	// Description — "О себе" из профиля; может быть пустым.
	Description string
	// BroadcasterType — "partner", "affiliate" или "" (обычный
	// аккаунт), как отдаёт Twitch.
	BroadcasterType string
	// CreatedAt — когда создан аккаунт; нулевое время, если Twitch не
	// прислал или не удалось разобрать.
	CreatedAt time.Time
}

// BroadcasterLabel — подпись типа канала ("Партнёр Twitch") либо пустая
// строка для обычного аккаунта и неизвестных значений.
func (p UserProfile) BroadcasterLabel() string {
	switch p.BroadcasterType {
	case "partner":
		return "Партнёр Twitch"
	case "affiliate":
		return "Аффилиат Twitch"
	default:
		return ""
	}
}

// AccountAgeText — "На Twitch с 14.12.2016 (9 лет)"; пустая строка, если
// дата создания неизвестна. now передаётся параметром, а не берётся из
// time.Now(), чтобы функция была детерминированной (и проверяемой).
func AccountAgeText(created, now time.Time) string {
	if created.IsZero() {
		return ""
	}
	created = created.UTC()
	now = now.UTC()

	years := now.Year() - created.Year()
	// Годовщина в этом году ещё не наступила — полных лет на один меньше.
	if now.Month() < created.Month() || (now.Month() == created.Month() && now.Day() < created.Day()) {
		years--
	}

	date := created.Format("02.01.2006")
	if years < 1 {
		return fmt.Sprintf("На Twitch с %s (меньше года)", date)
	}
	return fmt.Sprintf("На Twitch с %s (%d %s)", date, years, yearsWord(years))
}

// yearsWord — "год"/"года"/"лет" (см. pluralRu).
func yearsWord(n int) string { return pluralRu(n, "год", "года", "лет") }

// badgeTitles — русские названия самых распространённых бейджей по set_id
// (domain.Badge.Name). Названия наши, не официальные: Twitch отдаёт
// свои title только для каталога конкретного канала, а нам для окна
// профиля хватает короткой подписи.
var badgeTitles = map[string]string{
	"broadcaster":  "Стример",
	"moderator":    "Модератор",
	"vip":          "VIP",
	"subscriber":   "Подписчик",
	"founder":      "Основатель",
	"staff":        "Сотрудник Twitch",
	"admin":        "Администратор Twitch",
	"global_mod":   "Глобальный модератор",
	"partner":      "Партнёр",
	"premium":      "Prime Gaming",
	"turbo":        "Turbo",
	"bits":         "Bits",
	"sub-gifter":   "Даритель подписок",
	"artist-badge": "Артист",
}

// BadgeTitle — подпись бейджа для окна профиля. Неизвестный set_id
// показываем как есть: лучше сырое имя, чем пустое место.
func BadgeTitle(setID string) string {
	if title, ok := badgeTitles[setID]; ok {
		return title
	}
	return setID
}
