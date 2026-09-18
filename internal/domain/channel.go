package domain

// Channel identifies a Twitch channel (broadcaster).
type Channel struct {
	// ID — числовой Twitch user ID вещателя.
	ID string
	// Name — логин канала, например "somechannel".
	Name string
	// DisplayName — то, как канал показывает своё имя (может отличаться
	// от Name регистром или использовать нелатинские символы).
	DisplayName string
	// AvatarURL — ссылка на аватар канала (profile_image_url в Helix).
	AvatarURL string
}

// User identifies a Twitch user — автора сообщения в чате или
// авторизованного зрителя.
type User struct {
	ID          string
	Login       string
	DisplayName string
	AvatarURL   string
	// Color — цвет ника в чате (hex, например "#1E90FF"). Пустая
	// строка означает "цвет неизвестен" — либо пользователь его не
	// задавал, либо мы его ещё не запрашивали.
	Color string
}
