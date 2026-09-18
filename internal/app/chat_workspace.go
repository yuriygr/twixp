package app

import (
	"fmt"
	"sync"

	"twixp/internal/domain"
)

// ChatWorkspace держит коллекцию одновременно открытых чатов — по
// одному ChatService на канал — и то, какой из них сейчас активен.
// Это ровно то, что нужно сайдбару со списком чатов: Add на клик
// "добавить канал", SetActive на клик по элементу списка.
type ChatWorkspace struct {
	newReader func() ChatReader
	sender    ChatSender
	store     ChannelStore

	mu       sync.Mutex
	sessions map[string]*chatSession
	order    []string
	active   string
}

type chatSession struct {
	channel domain.Channel
	service *ChatService
}

// NewChatWorkspace создаёт пустой workspace.
//
// newReader вызывается один раз на каждый добавляемый канал — чтобы
// создать для него свежий ChatReader. Это фабрика, а не готовый
// экземпляр, потому что у каждого канала своя подписка (деталь того,
// как именно устроено соединение — общий ли сокет на всех или нет —
// целиком остаётся внутри конкретной реализации ChatReader).
//
// sender — один общий на все каналы: Helix Send Chat Message не
// привязан к соединению конкретного канала.
//
// store — опциональное хранение списка открытых каналов между
// запусками. Может быть nil, если персистентность не нужна (например,
// в коротких тестовых прогонах).
func NewChatWorkspace(newReader func() ChatReader, sender ChatSender, store ChannelStore) *ChatWorkspace {
	return &ChatWorkspace{
		newReader: newReader,
		sender:    sender,
		store:     store,
		sessions:  make(map[string]*chatSession),
	}
}

// Add подключается к каналу и добавляет его в workspace. Если канал
// уже открыт, повторное соединение не создаётся — возвращается
// существующая сессия.
func (w *ChatWorkspace) Add(channel domain.Channel) (*ChatService, error) {
	w.mu.Lock()
	if existing, ok := w.sessions[channel.ID]; ok {
		w.mu.Unlock()
		return existing.service, nil
	}
	w.mu.Unlock()

	// service.Connect — блокирующий сетевой I/O (dial+handshake
	// eventsub-хаба, если это первый открытый канал, плюс HTTP-вызов
	// подписки), может занять секунды. Намеренно вне лока: иначе на
	// это время замирают все остальные операции над workspace —
	// Active/List/SetActive/Remove, в том числе фоновая чистка из
	// eventsub.Hub при отзыве подписки или провале ресабскрайба
	// (main.go's forgetChannel), которая крутится в отдельной
	// горутине и не должна ждать, пока пользователь дозвонится до
	// нового канала.
	reader := w.newReader()
	service := NewChatService(reader, w.sender)

	if err := service.Connect(channel); err != nil {
		return nil, fmt.Errorf("connect to channel %s: %v", channel.Name, err)
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	// Пока коннектились без лока, кто-то мог успеть добавить тот же
	// канал (сейчас Add вызывается только с основной горутины
	// последовательно, так что этой гонки не бывает на практике — но
	// метод экспортируемый, и это не тот инвариант, на который стоит
	// молча полагаться). Если так — отдаём чужой результат, а
	// свежесозданное соединение закрываем, чтобы не плодить лишние
	// подписки на одном канале.
	if existing, ok := w.sessions[channel.ID]; ok {
		_ = service.Close()
		return existing.service, nil
	}

	w.sessions[channel.ID] = &chatSession{channel: channel, service: service}
	w.order = append(w.order, channel.ID)

	if w.active == "" {
		w.active = channel.ID
	}

	w.persistLocked()

	return service, nil
}

// Remove закрывает и убирает канал из workspace. Если удалённый канал
// был активным, активным не остаётся никто — какой канал сделать
// активным дальше, решает вызывающий код (например, UI выбирает
// соседний элемент в сайдбаре).
//
// Канал пропадает из workspace сразу, ещё до того как реально
// отработает сетевое закрытие (session.service.Close(), которое для
// eventsub идёт вплоть до HTTP DeleteSubscription). Так надёжнее для
// вызывающего кода: если это UI, "удалить чат" должно ощущаться
// мгновенно и не зависеть от того, ответит ли Twitch на отписку
// вовремя. Если закрытие всё же не удалось, ошибка возвращается для
// логирования, но откатывать удаление из workspace не пытаемся —
// заново подписаться, если что, всегда можно через Add.
func (w *ChatWorkspace) Remove(channelID string) error {
	w.mu.Lock()

	session, ok := w.sessions[channelID]
	if !ok {
		w.mu.Unlock()
		return fmt.Errorf("channel %s is not open", channelID)
	}

	delete(w.sessions, channelID)
	w.order = removeString(w.order, channelID)

	if w.active == channelID {
		w.active = ""
	}

	w.persistLocked()
	w.mu.Unlock()

	if err := session.service.Close(); err != nil {
		return fmt.Errorf("close channel %s: %v", channelID, err)
	}

	return nil
}

// SetActive помечает канал как активный для отображения.
func (w *ChatWorkspace) SetActive(channelID string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if _, ok := w.sessions[channelID]; !ok {
		return fmt.Errorf("channel %s is not open", channelID)
	}

	w.active = channelID
	return nil
}

// Active возвращает активный канал и его ChatService. ok=false, если
// сейчас ничего не активно (например, все чаты закрыты).
func (w *ChatWorkspace) Active() (channel domain.Channel, service *ChatService, ok bool) {
	w.mu.Lock()
	defer w.mu.Unlock()

	session, exists := w.sessions[w.active]
	if !exists {
		return domain.Channel{}, nil, false
	}
	return session.channel, session.service, true
}

// Get возвращает канал и его ChatService по ID, независимо от того,
// активен ли он сейчас. В отличие от Active(), нужен, когда
// вызывающему коду интересны ВСЕ открытые каналы, а не только тот,
// что сейчас отображается — например, чтобы держать по одному
// постоянному читателю сообщений на каждый открытый чат (см.
// internal/ui), а не только на активный.
func (w *ChatWorkspace) Get(channelID string) (channel domain.Channel, service *ChatService, ok bool) {
	w.mu.Lock()
	defer w.mu.Unlock()

	session, exists := w.sessions[channelID]
	if !exists {
		return domain.Channel{}, nil, false
	}
	return session.channel, session.service, true
}

// List возвращает открытые каналы в порядке добавления — именно в
// этом порядке их рисует сайдбар.
func (w *ChatWorkspace) List() []domain.Channel {
	w.mu.Lock()
	defer w.mu.Unlock()

	channels := make([]domain.Channel, 0, len(w.order))
	for _, id := range w.order {
		channels = append(channels, w.sessions[id].channel)
	}
	return channels
}

// Close закрывает все открытые чаты. Предназначен для завершения
// работы приложения.
func (w *ChatWorkspace) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	var firstErr error
	for id, session := range w.sessions {
		if err := session.service.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("close channel %s: %v", id, err)
		}
	}

	w.sessions = make(map[string]*chatSession)
	w.order = nil
	w.active = ""

	return firstErr
}

func removeString(items []string, target string) []string {
	out := items[:0]
	for _, item := range items {
		if item != target {
			out = append(out, item)
		}
	}
	return out
}

// persistLocked сохраняет текущий список открытых каналов через
// ChannelStore, если он задан. ВАЖНО: должен вызываться только пока
// w.mu уже захвачен вызывающим кодом — сам лок не берёт. Ошибка
// сохранения игнорируется намеренно: потеря сохранённого списка не
// должна ронять саму операцию над чатом.
func (w *ChatWorkspace) persistLocked() {
	if w.store == nil {
		return
	}

	channels := make([]domain.Channel, 0, len(w.order))
	for _, id := range w.order {
		channels = append(channels, w.sessions[id].channel)
	}
	_ = w.store.Save(channels)
}
