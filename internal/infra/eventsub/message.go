package eventsub

import (
	"fmt"
	"time"

	"twixp/internal/domain"
)

// eventSubEnvelope — общая форма всех сообщений EventSub WebSocket.
// В зависимости от metadata.message_type заполнена либо Session
// (session_welcome/keepalive/reconnect), либо Event (notification).
type eventSubEnvelope struct {
	Metadata struct {
		MessageType      string `json:"message_type"`
		MessageTimestamp string `json:"message_timestamp"`
	} `json:"metadata"`
	Payload struct {
		Session struct {
			ID                      string `json:"id"`
			ReconnectURL            string `json:"reconnect_url"`
			KeepaliveTimeoutSeconds int    `json:"keepalive_timeout_seconds"`
		} `json:"session"`
		Event struct {
			BroadcasterUserID    string `json:"broadcaster_user_id"`
			BroadcasterUserLogin string `json:"broadcaster_user_login"`
			BroadcasterUserName  string `json:"broadcaster_user_name"`
			ChatterUserID        string `json:"chatter_user_id"`
			ChatterUserLogin     string `json:"chatter_user_login"`
			ChatterUserName      string `json:"chatter_user_name"`
			MessageID            string `json:"message_id"`
			Message              struct {
				Text string `json:"text"`
			} `json:"message"`
			Color  string `json:"color"`
			Badges []struct {
				SetID string `json:"set_id"`
				ID    string `json:"id"`
			} `json:"badges"`
			// Reply — nil, если сообщение не является ответом. Twitch
			// присылает его как есть только для настоящих ответов (через
			// reply_parent_message_id при отправке, см. helix.Client.Send)
			// — не пытаемся угадывать это как-то иначе, например по
			// содержимому текста.
			Reply *struct {
				ParentMessageID string `json:"parent_message_id"`
				ParentUserLogin string `json:"parent_user_login"`
				ParentUserName  string `json:"parent_user_name"`
				ParentMessage   string `json:"parent_message_body"`
			} `json:"reply"`
			// MessageType — только у channel.chat.message. "text" — обычное
			// сообщение; "channel_points_highlighted" — оплачено баллами
			// канала для подсветки ("Highlight My Message"). Остальные
			// значения (user_intro и т.п.) сейчас не различаем.
			MessageType string `json:"message_type"`
			// SystemMessage — только у channel.chat.notification: готовый
			// текст уведомления (см. domain.ChatMessage.SystemMessage).
			SystemMessage string `json:"system_message"`
			// TargetUserID/Login/Name — только у channel.chat.message_delete:
			// тот, чьё сообщение удалили (не тот, кто удалил — модератор
			// нигде в этом событии не называется). См.
			// decodeMessageDelete.
			TargetUserID    string `json:"target_user_id"`
			TargetUserLogin string `json:"target_user_login"`
			TargetUserName  string `json:"target_user_name"`
		} `json:"event"`
		Subscription struct {
			// Type — тип EventSub-подписки, которой принадлежит это
			// уведомление ("channel.chat.message" или
			// "channel.chat.notification", см. Hub.dispatch) — Event выше
			// общий на оба, различать нужно по этому полю, а не по
			// содержимому.
			Type      string `json:"type"`
			Status    string `json:"status"`
			Condition struct {
				BroadcasterUserID string `json:"broadcaster_user_id"`
			} `json:"condition"`
		} `json:"subscription"`
	} `json:"payload"`
}

// decodeCommonFields разбирает то, что общее у channel.chat.message и
// channel.chat.notification — один и тот же payload.event (см.
// eventSubEnvelope выше), одинаковые имена полей автора/канала/бейджей
// что там, что там. ok=false при отсутствующем broadcaster_user_id —
// тот же признак "событие отбрасываем целиком", что раньше был отдельно
// продублирован в обеих decode-функциях.
func decodeCommonFields(env eventSubEnvelope) (msg domain.ChatMessage, broadcasterID string, ok bool) {
	e := env.Payload.Event
	if e.BroadcasterUserID == "" {
		return domain.ChatMessage{}, "", false
	}

	badges := make([]domain.Badge, 0, len(e.Badges))
	for _, b := range e.Badges {
		badges = append(badges, domain.Badge{Name: b.SetID, Version: b.ID})
	}

	msg = domain.ChatMessage{
		ID: e.MessageID,
		Channel: domain.Channel{
			ID:          e.BroadcasterUserID,
			Name:        e.BroadcasterUserLogin,
			DisplayName: e.BroadcasterUserName,
		},
		Author: domain.User{
			ID:          e.ChatterUserID,
			Login:       e.ChatterUserLogin,
			DisplayName: e.ChatterUserName,
			Color:       e.Color,
		},
		// Text — общее поле payload.event.message.text, но что оно
		// значит — уже зависит от вызывающей стороны: для
		// channel.chat.message это сам текст сообщения, для
		// channel.chat.notification — то, что подписчик написал вместе
		// с событием (например, комментарий к ресабу), часто пусто.
		Text:   e.Message.Text,
		Badges: badges,
		SentAt: parseSentAt(env.Metadata.MessageTimestamp),
	}

	return msg, e.BroadcasterUserID, true
}

// decodeChatMessage превращает payload.event уведомления
// channel.chat.message в domain.ChatMessage. Возвращает отдельно
// broadcaster_user_id — по нему Hub находит, какому каналу
// адресовано сообщение.
func decodeChatMessage(env eventSubEnvelope) (domain.ChatMessage, string, error) {
	msg, broadcasterID, ok := decodeCommonFields(env)
	if !ok {
		return domain.ChatMessage{}, "", fmt.Errorf("missing broadcaster_user_id")
	}

	e := env.Payload.Event

	if e.Reply != nil {
		msg.ReplyTo = &domain.ReplyTo{
			MessageID:   e.Reply.ParentMessageID,
			AuthorLogin: e.Reply.ParentUserLogin,
			AuthorName:  e.Reply.ParentUserName,
			Text:        e.Reply.ParentMessage,
		}
	}
	msg.Highlighted = e.MessageType == "channel_points_highlighted"

	return msg, broadcasterID, nil
}

// decodeChatNotification превращает payload.event уведомления
// channel.chat.notification (подписка, подарок подписки, рейд,
// объявление и т.п. — EventSub USERNOTICE-замена) в domain.ChatMessage
// с заполненным SystemMessage. ReplyTo/Highlighted тут не при делах —
// уведомление не является ни ответом, ни оплаченным за баллы обычным
// сообщением, поэтому в decodeCommonFields их и нет вовсе.
func decodeChatNotification(env eventSubEnvelope) (domain.ChatMessage, string, error) {
	msg, broadcasterID, ok := decodeCommonFields(env)
	if !ok {
		return domain.ChatMessage{}, "", fmt.Errorf("missing broadcaster_user_id")
	}

	msg.SystemMessage = env.Payload.Event.SystemMessage

	return msg, broadcasterID, nil
}

// decodeMessageDelete превращает payload.event удаления сообщения
// (channel.chat.message_delete) в domain.MessageDeletion — тут нужен
// только message_id, всё остальное (кто удалил, чьё сообщение) не
// участвует в отображении: chatPane просто находит строку с таким же
// MessageID в уже накопленной истории и красит её серым.
func decodeMessageDelete(env eventSubEnvelope) (domain.MessageDeletion, string, error) {
	e := env.Payload.Event
	if e.BroadcasterUserID == "" || e.MessageID == "" {
		return domain.MessageDeletion{}, "", fmt.Errorf("missing broadcaster_user_id or message_id")
	}

	return domain.MessageDeletion{MessageID: e.MessageID}, e.BroadcasterUserID, nil
}

// parseSentAt разбирает metadata.message_timestamp — момент, когда
// Twitch отправил это уведомление, а не когда мы его получили
// (то есть уже не время локального конверта dispatch/буферизации).
// Если поле пустое или почему-то не парсится как RFC3339, откатываемся
// на время получения — лучше приблизительная метка, чем падать на
// корректном в остальном сообщении.
func parseSentAt(raw string) time.Time {
	if raw == "" {
		return time.Now()
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Now()
	}
	return t
}
