package app

import (
	"fmt"
	"strings"
	"sync"

	"twixp/internal/domain"
)

// ChatService оркестрирует чтение и отправку сообщений для одного
// канала. Он не знает ничего про WebSocket, HTTP или формат ответов
// Twitch — вся эта работа делегирована ChatReader/ChatSender,
// которые ему передали при создании.
type ChatService struct {
	reader ChatReader
	sender ChatSender

	// mu защищает channel — единственное изменяемое после создания
	// поле. На практике и Connect (через ChatWorkspace.Add), и Send
	// (из UI-потока) сейчас вызываются с одной и той же горутины, так
	// что гонки не бывает, но ChatService — публичный API app-слоя, и
	// полагаться на то, что этот порядок вызовов никогда не изменится
	// (например, если Connect однажды начнут звать из фоновой
	// горутины при переподключении) — не тот инвариант, который стоит
	// держать в уме молча.
	mu        sync.Mutex
	channel   domain.Channel
	connected bool
}

// NewChatService связывает ChatService с конкретной парой reader/sender.
func NewChatService(reader ChatReader, sender ChatSender) *ChatService {
	return &ChatService{reader: reader, sender: sender}
}

// Connect открывает соединение чата для указанного канала и только
// затем его сохраняет.
//
// Не идемпотентен НАМЕРЕННО — повторный вызов (в том числе с другим
// каналом) возвращает ошибку, а не тихо переподключается или молча
// открывает второе соединение поверх первого: ChatWorkspace создаёт
// ровно один ChatService на канал и вызывает Connect ровно один раз
// (см. ChatWorkspace.Add), так что повторный вызов — всегда признак
// ошибки в вызывающем коде, а не законный сценарий "переподключиться к
// другому каналу", который стоило бы поддерживать молча.
func (s *ChatService) Connect(channel domain.Channel) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.connected {
		return fmt.Errorf("chat service is already connected to channel %s", s.channel.Name)
	}

	if err := s.reader.Connect(channel); err != nil {
		return fmt.Errorf("connect chat reader: %v", err)
	}
	s.channel = channel
	s.connected = true
	return nil
}

// Messages отдаёт поток входящих сообщений чата.
func (s *ChatService) Messages() <-chan domain.ChatMessage {
	return s.reader.Messages()
}

// Deletions отдаёт поток удалений сообщений (см. ChatReader.Deletions).
func (s *ChatService) Deletions() <-chan domain.MessageDeletion {
	return s.reader.Deletions()
}

// ChatModes отдаёт поток режимов чата (см. ChatReader.ChatModes).
func (s *ChatService) ChatModes() <-chan domain.ChatModes {
	return s.reader.ChatModes()
}

// Send отправляет сообщение в текущий подключённый канал.
// replyToMessageID — ID сообщения, на которое отвечаем, либо "" для
// обычного сообщения (не Twitch-реплая).
func (s *ChatService) Send(text string, replyToMessageID string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("message text must not be empty")
	}

	s.mu.Lock()
	channel := s.channel
	connected := s.connected
	s.mu.Unlock()

	if !connected || channel.ID == "" {
		return fmt.Errorf("chat service is not connected")
	}

	if err := s.sender.Send(channel, text, replyToMessageID); err != nil {
		return fmt.Errorf("send chat message to channel %s: %v", channel.Name, err)
	}
	return nil
}

// Close освобождает соединение чата.
func (s *ChatService) Close() error {
	return s.reader.Close()
}
