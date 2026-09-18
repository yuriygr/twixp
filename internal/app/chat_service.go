package app

import (
	"fmt"

	"twixp/internal/domain"
)

// ChatService оркестрирует чтение и отправку сообщений для одного
// канала. Он не знает ничего про WebSocket, HTTP или формат ответов
// Twitch — вся эта работа делегирована ChatReader/ChatSender,
// которые ему передали при создании.
type ChatService struct {
	reader  ChatReader
	sender  ChatSender
	channel domain.Channel
}

// NewChatService связывает ChatService с конкретной парой reader/sender.
func NewChatService(reader ChatReader, sender ChatSender) *ChatService {
	return &ChatService{reader: reader, sender: sender}
}

// Connect открывает соединение чата для указанного канала и только затем его сохраняет
func (s *ChatService) Connect(channel domain.Channel) error {
	if err := s.reader.Connect(channel); err != nil {
		return fmt.Errorf("connect chat reader: %v", err)
	}
	s.channel = channel
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

// Send отправляет сообщение в текущий подключённый канал.
// replyToMessageID — ID сообщения, на которое отвечаем, либо "" для
// обычного сообщения (не Twitch-реплая).
func (s *ChatService) Send(text string, replyToMessageID string) error {
	if text == "" {
		return fmt.Errorf("message text must not be empty")
	}
	if err := s.sender.Send(s.channel, text, replyToMessageID); err != nil {
		return fmt.Errorf("send chat message: %v", err)
	}
	return nil
}

// Close освобождает соединение чата.
func (s *ChatService) Close() error {
	return s.reader.Close()
}
