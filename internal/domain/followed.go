package domain

import (
	"fmt"
	"sort"
	"strings"
)

// LiveStream — идущая сейчас трансляция канала из подписок (Get
// Followed Streams). Логин и имя дублируют данные канала: если список
// подписок обрезан лимитом страниц, а трансляция канала из хвоста
// всё же пришла, её можно показать, не имея канала в основном списке
// (см. MergeFollowed).
type LiveStream struct {
	UserID      string
	Login       string
	DisplayName string
	Game        string
	Viewers     int
}

// FollowedChannel — канал из подписок пользователя вместе с тем, идёт
// ли сейчас эфир. Нужен списку "Добавить канал": там это справочник, из
// которого выбирают, какой чат открыть.
type FollowedChannel struct {
	// Channel — ID, логин и отображаемое имя; AvatarURL здесь пуст
	// (Get Followed Channels аватарок не отдаёт), его подтягивает уже
	// открытие канала.
	Channel Channel
	Live    bool
	Game    string // пусто, если не в эфире или игра не указана
	Viewers int    // 0, если не в эфире
}

// Title — как называть канал человеку: отображаемое имя, а если его
// нет — логин.
func (f FollowedChannel) Title() string {
	if f.Channel.DisplayName != "" {
		return f.Channel.DisplayName
	}
	return f.Channel.Name
}

// ListLabel — строка в списке: "Имя" для канала не в эфире и
// "● Имя — Игра, 1,2 тыс." для идущей трансляции.
func (f FollowedChannel) ListLabel() string {
	if !f.Live {
		return f.Title()
	}

	details := formatViewers(f.Viewers)
	if f.Game != "" {
		details = f.Game + ", " + details
	}
	return "● " + f.Title() + " — " + details
}

// formatViewers — "123", "1,2 тыс.", "15 тыс.": тысячи с одним знаком
// после запятой, без хвостового ",0".
func formatViewers(n int) string {
	if n < 1000 {
		return fmt.Sprint(n)
	}

	tenths := n / 100 // 1234 → 12 десятых тысячи
	whole, frac := tenths/10, tenths%10
	if frac == 0 {
		return fmt.Sprintf("%d тыс.", whole)
	}
	return fmt.Sprintf("%d,%d тыс.", whole, frac)
}

// MergeFollowed собирает единый список: все каналы из подписок, у тех, чья
// трансляция идёт, — отметка и подробности. Порядок: сначала те, кто в
// эфире (по убыванию зрителей, при равенстве — по имени), затем
// остальные по алфавиту без учёта регистра.
//
// Трансляции каналов, которых нет в followed (список подписок мог
// оборваться на лимите страниц), добавляются тоже — иначе человек не
// увидел бы канал, который прямо сейчас в эфире.
func MergeFollowed(followed []Channel, live []LiveStream) []FollowedChannel {
	liveByID := make(map[string]LiveStream, len(live))
	for _, s := range live {
		liveByID[s.UserID] = s
	}

	result := make([]FollowedChannel, 0, len(followed)+len(live))
	seen := make(map[string]bool, len(followed))

	for _, ch := range followed {
		seen[ch.ID] = true

		item := FollowedChannel{Channel: ch}
		if s, ok := liveByID[ch.ID]; ok {
			item.Live, item.Game, item.Viewers = true, s.Game, s.Viewers
		}
		result = append(result, item)
	}

	for _, s := range live {
		if seen[s.UserID] {
			continue
		}
		seen[s.UserID] = true
		result = append(result, FollowedChannel{
			Channel: Channel{ID: s.UserID, Name: s.Login, DisplayName: s.DisplayName},
			Live:    true,
			Game:    s.Game,
			Viewers: s.Viewers,
		})
	}

	sort.SliceStable(result, func(i, j int) bool {
		a, b := result[i], result[j]
		if a.Live != b.Live {
			return a.Live
		}
		if a.Live && a.Viewers != b.Viewers {
			return a.Viewers > b.Viewers
		}
		return strings.ToLower(a.Title()) < strings.ToLower(b.Title())
	})

	return result
}

// FilterFollowed оставляет в списке каналы, у которых логин или имя
// содержат query (без учёта регистра; пустой query — все), и выбрасывает
// те, чьи ID есть в exclude (уже открытые чаты — их незачем предлагать
// ещё раз). Порядок сохраняется.
func FilterFollowed(list []FollowedChannel, query string, exclude map[string]bool) []FollowedChannel {
	query = strings.ToLower(strings.TrimSpace(query))

	result := make([]FollowedChannel, 0, len(list))
	for _, f := range list {
		if exclude[f.Channel.ID] {
			continue
		}
		if query != "" &&
			!strings.Contains(strings.ToLower(f.Channel.Name), query) &&
			!strings.Contains(strings.ToLower(f.Channel.DisplayName), query) {
			continue
		}
		result = append(result, f)
	}
	return result
}
