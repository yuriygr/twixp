package app

import (
	"errors"

	"twitchclient/internal/domain"
)

// ErrNoToken возвращается TokenStore.Load, если токен ещё не был
// сохранён. AuthService трактует это точно так же, как истёкший
// токен — просто запускает AuthFlow заново.
var ErrNoToken = errors.New("no token stored")

// ChatReader — источник входящих сообщений чата для канала.
// Реализуется в infra/eventsub. Ни app, ни ui ничего не знают
// про WebSocket или формат событий Twitch.
type ChatReader interface {
	Connect(channel domain.Channel) error
	Messages() <-chan domain.ChatMessage
	// Deletions — отдельный поток: "это сообщение удалено модератором"
	// (EventSub channel.chat.message_delete). Не часть Messages() —
	// удаление относится к УЖЕ показанному сообщению, а не добавляет
	// новое, и обрабатывается по-другому (см. ui.chatPane.deleteMessage).
	Deletions() <-chan domain.MessageDeletion
	Close() error
}

// ChatSender — отправка сообщений в чат канала.
// Реализуется в infra/helix. replyToMessageID — ID сообщения, на
// которое отвечаем (Twitch "reply"), либо "" для обычного сообщения.
type ChatSender interface {
	Send(channel domain.Channel, text string, replyToMessageID string) error
}

// TokenStore — хранение OAuth-токена между запусками приложения.
// Реализуется в infra/auth.
type TokenStore interface {
	// Load возвращает сохранённый токен либо ErrNoToken, если
	// сохранённого токена ещё нет.
	Load() (domain.Token, error)
	Save(domain.Token) error
	Clear() error
}

// ChannelStore — хранение списка открытых чатов между запусками
// приложения. Реализуется в infra/store.
type ChannelStore interface {
	// Load возвращает сохранённый список каналов. Пустой список без
	// ошибки — нормальный случай для первого запуска.
	Load() ([]domain.Channel, error)
	Save(channels []domain.Channel) error
}

// AuthFlow — получение нового OAuth-токена через авторизацию
// пользователя (например, Device Code Flow).
//
// onPrompt вызывается, как только код готов к показу пользователю —
// именно так UI узнаёт, что показать на экране. Может быть вызван
// из другой горутины, чем та, что вызвала Authorize. onPrompt может
// быть nil, если показывать код некому (например, в тестах).
type AuthFlow interface {
	Authorize(onPrompt func(userCode, verificationURI string)) (domain.Token, error)
}

// TokenRefresher — тихое обновление access token по refresh token,
// без участия пользователя и без похода в браузер. Может быть nil в
// AuthService — тогда используется только полный AuthFlow при каждом
// истечении токена.
type TokenRefresher interface {
	Refresh(refreshToken string) (domain.Token, error)
}
