package domain

import (
	"fmt"
	"strings"
	"time"
)

// StreamInfo — состояние трансляции канала на момент запроса (Get
// Streams). Live == false — канал не в эфире, остальные поля пусты.
type StreamInfo struct {
	Live      bool
	Title     string
	Game      string
	Viewers   int
	StartedAt time.Time // нулевое, если Twitch не прислал или не разобралось
}

// StatusText — короткая подпись статуса.
func (s StreamInfo) StatusText() string {
	if s.Live {
		return "В эфире"
	}
	return "Не в эфире"
}

// Details — многострочное описание эфира для окна канала: игра, название
// трансляции, затем "1,2 тыс. зрителей · идёт 2 ч 15 мин". Пустые части
// пропускаются. Не в эфире — "Сейчас не в эфире". now передаётся
// параметром ради детерминированных тестов.
func (s StreamInfo) Details(now time.Time) string {
	if !s.Live {
		return "Сейчас не в эфире"
	}

	var lines []string
	if s.Game != "" {
		lines = append(lines, s.Game)
	}
	if s.Title != "" {
		lines = append(lines, s.Title)
	}

	stats := viewersText(s.Viewers)
	if up := uptimeText(s.StartedAt, now); up != "" {
		stats += " · " + up
	}
	lines = append(lines, stats)

	return strings.Join(lines, "\n")
}

// viewersText — "1 зритель", "3 зрителя", "15 зрителей", "1,2 тыс. зрителей".
func viewersText(n int) string {
	if n >= 1000 {
		return formatViewers(n) + " зрителей"
	}
	return fmt.Sprintf("%d %s", n, pluralRu(n, "зритель", "зрителя", "зрителей"))
}

// uptimeText — "идёт 2 ч 15 мин", "идёт 15 мин", "идёт меньше минуты";
// пустая строка, если время старта неизвестно или оказалось в будущем
// (расхождение часов).
func uptimeText(start, now time.Time) string {
	if start.IsZero() || now.Before(start) {
		return ""
	}

	minutes := int(now.Sub(start) / time.Minute)
	switch {
	case minutes < 1:
		return "идёт меньше минуты"
	case minutes < 60:
		return fmt.Sprintf("идёт %d мин", minutes)
	default:
		h, m := minutes/60, minutes%60
		if m == 0 {
			return fmt.Sprintf("идёт %d ч", h)
		}
		return fmt.Sprintf("идёт %d ч %d мин", h, m)
	}
}

// FollowStatus — подписан ли пользователь на канал (follow, не платная
// подписка) и с какой даты.
type FollowStatus struct {
	Following bool
	Since     time.Time // нулевое, если не подписан или дата неизвестна
}

// Text — подпись для окна канала.
func (f FollowStatus) Text() string {
	switch {
	case !f.Following:
		return "Вы не подписаны"
	case f.Since.IsZero():
		return "Вы подписаны"
	default:
		return "Вы подписаны с " + f.Since.UTC().Format("02.01.2006")
	}
}

// pluralRu выбирает форму слова по правилам русского языка:
// 1 год, 2 года, 5 лет, 21 год, 11–14 лет.
func pluralRu(n int, one, few, many string) string {
	if n%100 >= 11 && n%100 <= 14 {
		return many
	}
	switch n % 10 {
	case 1:
		return one
	case 2, 3, 4:
		return few
	default:
		return many
	}
}
