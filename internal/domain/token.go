package domain

import "time"

// Token — OAuth user access token для обращения к Twitch Helix и EventSub.
type Token struct {
	AccessToken  string
	RefreshToken string
	Scopes       []string
	ExpiresAt    time.Time
}

// Expired сообщает, истёк ли токен. Нулевое значение ExpiresAt
// трактуется как "срок неизвестен" и не считается истёкшим —
// в этом случае источником истины становится 401-ответ сервера,
// а не наше собственное предположение о времени жизни токена.
func (t Token) Expired() bool {
	return !t.ExpiresAt.IsZero() && time.Now().After(t.ExpiresAt)
}
