package app

import (
	"context"
	"errors"

	"twixp/internal/domain"
)

// ErrNoToken возвращается TokenStore.Load, если токен ещё не был
// сохранён. AuthService трактует это точно так же, как истёкший
// токен — просто запускает AuthFlow заново.
var ErrNoToken = errors.New("no token stored")

// ChatReader — источник входящих сообщений чата для канала.
// Реализуется в infra/eventsub. Ни app, ни ui ничего не знают
// про WebSocket или формат событий Twitch.
//
// Жизненный цикл каналов Messages()/Deletions()/ChatModes(): все
// закрываются при Close() — это единственная гарантия, на которую
// вправе полагаться вызывающий код (см. ui.chatPane.watchMessages: цикл
// там продолжается, пока хотя бы один из потоков ещё не закрыт).
// Реализация вольна закрыть любые из них РАНЬШЕ Close() — например, при разрыве соединения ещё
// до того, как кто-то явно вызвал Close() — вызывающий код это уже
// переживает (проверяет ok у каждого чтения независимо), но закрыть их
// СРАЗУ ПОСЛЕ Close(), не откладывая, реализация обязана всегда.
type ChatReader interface {
	Connect(channel domain.Channel) error
	Messages() <-chan domain.ChatMessage
	// Deletions — отдельный поток: "это сообщение удалено модератором"
	// (EventSub channel.chat.message_delete). Не часть Messages() —
	// удаление относится к УЖЕ показанному сообщению, а не добавляет
	// новое, и обрабатывается по-другому (см. ui.chatPane.deleteMessage).
	Deletions() <-chan domain.MessageDeletion
	// ChatModes — отдельный поток: "режимы чата канала (только
	// смайлики, только подписчики, медленный режим и т.п.) сейчас
	// такие". Каждое значение — полное текущее состояние, а не
	// дельта: потребителю достаточно запомнить последнее. Первое
	// значение — начальное состояние (запрашивается при подключении),
	// дальше — по каждому изменению. Может не прийти вовсе, если
	// узнать режимы не удалось — тогда плашек просто нет.
	ChatModes() <-chan domain.ChatModes
	Close() error
}

// ChatSender — отправка сообщений в чат канала.
// Реализуется в infra/helix. replyToMessageID — ID сообщения, на
// которое отвечаем (Twitch "reply"), либо "" для обычного сообщения.
//
// Стейтлесс — в отличие от ChatReader, канал передаётся в каждом
// вызове, а не запоминается: Send Chat Message в Helix — обычный
// одноразовый HTTP-запрос, никакого долгоживущего соединения на
// канал нет и не нужно (в отличие от ChatReader, где WebSocket-сессия
// реально живёт между вызовами). Если когда-нибудь понадобится
// per-channel токен или отдельный rate-limit bucket на отправку —
// асимметрию с ChatReader придётся выравнивать явно, а не тихо.
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
// onPrompt вызывается РОВНО ОДИН РАЗ за вызов Authorize, как только
// код готов к показу пользователю, — именно так UI узнаёт, что
// показать на экране. Может быть вызван из другой горутины, чем та,
// что вызвала Authorize, и Authorize в этот момент ещё не возвращается
// (он продолжает опрашивать сервер дальше, пока пользователь не введёт
// код или не истечёт таймаут) — так что onPrompt вызывается СТРОГО ДО
// возврата из Authorize, а не после и не одновременно с ним. onPrompt
// может быть nil, если показывать код некому (например, в тестах);
// в этом случае реализация не обязана (и не должна) сама придумывать,
// куда девать код — она просто продолжает попытку молча, а вызывающая
// сторона (см. AuthService.EnsureAuthenticated) сама решает, что с
// этим делать. onPrompt не должен паниковать и не должен делать долгих
// блокирующих операций — он вызывается из глубины цикла опроса, и пока
// он не вернётся, Authorize не продолжит.
//
// ctx позволяет прервать долгую (до нескольких минут — пока
// пользователь не введёт код) интерактивную попытку входа снаружи —
// см. AuthService.Logout, который отменяет текущий Authorize, если он
// в процессе. Реализация обязана проверять ctx.Done() в цикле опроса
// и возвращать ctx.Err() при отмене, а не игнорировать её.
type AuthFlow interface {
	Authorize(ctx context.Context, onPrompt func(userCode, verificationURI string)) (domain.Token, error)
}

// TokenRefresher — тихое обновление access token по refresh token,
// без участия пользователя и без похода в браузер. Может быть nil в
// AuthService — тогда используется только полный AuthFlow при каждом
// истечении токена.
type TokenRefresher interface {
	Refresh(refreshToken string) (domain.Token, error)
}
