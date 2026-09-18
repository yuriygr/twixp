package domain

import "time"

// Badge — бейдж зрителя (модератор, подписчик, VIP и т.п.).
type Badge struct {
	Name    string
	Version string
}

// ReplyTo — если сообщение отправлено в ответ на другое, здесь то, на
// что отвечали: автор и текст родительского сообщения (ровно то, что
// Twitch присылает в event.reply, см. eventsub/message.go). nil в
// ChatMessage.ReplyTo — сообщение обычное, не ответ.
type ReplyTo struct {
	MessageID   string
	AuthorLogin string
	AuthorName  string
	Text        string
}

// MessageDeletion — сигнал о том, что конкретное сообщение было
// удалено модератором (EventSub channel.chat.message_delete). Это не
// часть истории сообщений, как ChatMessage, а инструкция "найди в уже
// накопленной истории и пометь" — сама история хранится на стороне UI
// (см. ui.chatPane), тут только идентификатор.
type MessageDeletion struct {
	MessageID string
}

// ChatMessage — одно сообщение, полученное из чата Twitch.
// Это чистая структура данных: она не знает, откуда взялась
// (EventSub, IRC или что угодно ещё) — это забота infra-слоя.
//
// Совмещает два разных события Twitch под одним типом: обычное
// сообщение (EventSub channel.chat.message) и системное уведомление о
// событии в чате — подписка, подарок подписки, рейд и т.п. (EventSub
// channel.chat.notification). Разные структуры для них плодили бы
// дублирование почти всех полей (автор, бейджи, время) ради разницы
// всего в паре — решает SystemMessage: пусто для обычных сообщений,
// заполнено для уведомлений (см. eventsub/message.go,
// decodeChatMessage/decodeChatNotification).
type ChatMessage struct {
	ID      string
	Channel Channel
	Author  User
	Text    string
	Badges  []Badge
	SentAt  time.Time
	// ReplyTo — см. одноимённый тип. nil, если это не ответ.
	ReplyTo *ReplyTo
	// Highlighted — оплачено ли сообщение баллами канала для
	// подсветки ("Highlight My Message", EventSub
	// message_type == "channel_points_highlighted").
	Highlighted bool
	// SystemMessage — готовый человекочитаемый текст системного
	// уведомления о событии в чате (Twitch сам формирует его — не
	// нужно самим собирать фразу из notice_type/resub/sub_gift и
	// т.п.). Пусто для обычных сообщений — это и есть признак, что
	// ChatMessage представляет уведомление, а не сообщение.
	SystemMessage string
}
