package eventsub

import (
	"fmt"

	"twitchclient/internal/app"
	"twitchclient/internal/domain"
)

// hubReader реализует app.ChatReader для одного конкретного канала.
// Само соединение общее для всех hubReader'ов одного Hub — здесь
// только состояние привязки к каналу. Создаётся исключительно через
// Hub.NewReader, поэтому не экспортируется.
type hubReader struct {
	hub     *Hub
	channel domain.Channel
	state   *channelState
}

// Connect реализует app.ChatReader.
func (r *hubReader) Connect(channel domain.Channel) error {
	state, err := r.hub.subscribe(channel)
	if err != nil {
		return err
	}
	r.channel = channel
	r.state = state
	return nil
}

// Messages реализует app.ChatReader.
func (r *hubReader) Messages() <-chan domain.ChatMessage {
	if r.state == nil {
		return nil
	}
	return r.state.messages
}

// Deletions реализует app.ChatReader.
func (r *hubReader) Deletions() <-chan domain.MessageDeletion {
	if r.state == nil {
		return nil
	}
	return r.state.deletions
}

// Close реализует app.ChatReader.
func (r *hubReader) Close() error {
	if r.state == nil {
		return fmt.Errorf("reader is not connected")
	}
	return r.hub.unsubscribe(r.channel.ID)
}

// Компиляционная проверка: hubReader действительно реализует app.ChatReader.
var _ app.ChatReader = (*hubReader)(nil)
