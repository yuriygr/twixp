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
	onChanged func([]domain.Channel)

	// addMu сериализует Add сам с собой — единственная операция,
	// которая делает блокирующий сетевой I/O (см. Add) вне w.mu.
	// Без этого при гипотетическом параллельном Add на один и тот же
	// канал оба вызова успевали бы законнектиться, и один из
	// результатов закрывался бы — то есть дважды тратился бы
	// rate-limit Twitch (Subscribe + DeleteSubscription) на один и тот
	// же канал впустую. addMu не даёт этой ситуации возникнуть вообще:
	// пока один Add коннектится, второй ждёт своей очереди именно у
	// addMu, а не у w.mu — Active/List/Get/SetActive/Remove им не
	// блокируются, только другие Add.
	addMu sync.Mutex

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
// создать для него свежий ChatReader. Конкретная реализация вправе
// сама решать, сколько за этим реально стоит физических соединений:
// например, eventsub.Hub.NewReader возвращает лёгкий per-channel
// объект поверх одного общего WebSocket-хаба, а не открывает новый
// сокет на каждый вызов — с точки зрения этого интерфейса неважно, как
// это устроено внутри, важно только чтобы Connect/Messages/Deletions/
// Close вели себя ровно так, как описано в ChatReader.
//
// sender — один общий на все каналы: Helix Send Chat Message не
// привязан к соединению конкретного канала.
//
// onChanged вызывается после каждого изменения списка каналов —
// добавления или удаления (но НЕ после Close, см. его комментарий) —
// со снапшотом полного списка в порядке добавления. Обычно это и есть
// персистентность (см. main.go: onChanged оборачивает
// ChannelStore.Save), но ChatWorkspace сам не знает, что это —
// он просто сообщает "список изменился, вот новый". Может быть nil,
// если это не нужно (например, в коротких тестовых прогонах).
// ChatWorkspace также не умеет ЧИТАТЬ сохранённый список при старте —
// это остаётся на вызывающей стороне: обычный ChannelStore.Load с
// последующими Add на каждый канал.
func NewChatWorkspace(newReader func() ChatReader, sender ChatSender, onChanged func([]domain.Channel)) *ChatWorkspace {
	return &ChatWorkspace{
		newReader: newReader,
		sender:    sender,
		onChanged: onChanged,
		sessions:  make(map[string]*chatSession),
	}
}

// Add подключается к каналу и добавляет его в workspace. Если канал
// уже открыт, повторное соединение не создаётся — возвращается
// существующая сессия.
func (w *ChatWorkspace) Add(channel domain.Channel) (*ChatService, error) {
	if channel.ID == "" {
		return nil, fmt.Errorf("empty channel ID")
	}

	// addMu держим на весь метод, включая блокирующий Connect ниже —
	// см. комментарий у поля. w.mu (быстрый, только для state) при
	// этом занимаем отдельно и ненадолго, а не на всё время I/O.
	w.addMu.Lock()
	defer w.addMu.Unlock()

	w.mu.Lock()
	if existing, ok := w.sessions[channel.ID]; ok {
		w.mu.Unlock()
		return existing.service, nil
	}
	w.mu.Unlock()

	// service.Connect — блокирующий сетевой I/O (dial+handshake
	// eventsub-хаба, если это первый открытый канал, плюс HTTP-вызов
	// подписки), может занять секунды. Вне w.mu намеренно: иначе на это
	// время замирают все остальные операции над workspace —
	// Active/List/SetActive/Remove, в том числе фоновая чистка из
	// eventsub.Hub при отзыве подписки или провале ресабскрайба,
	// которая крутится в отдельной горутине и не должна ждать, пока
	// пользователь дозвонится до нового канала. addMu выше при этом
	// всё равно не даёт второму Add начать точно то же самое параллельно
	// — см. комментарий у поля.
	reader := w.newReader()
	service := NewChatService(reader, w.sender)

	if err := service.Connect(channel); err != nil {
		return nil, fmt.Errorf("connect to channel %s: %v", channel.Name, err)
	}

	w.mu.Lock()
	w.sessions[channel.ID] = &chatSession{channel: channel, service: service}
	w.order = append(w.order, channel.ID)
	if w.active == "" {
		w.active = channel.ID
	}
	snapshot := w.snapshotChannelsLocked()
	w.mu.Unlock()

	w.notifyChanged(snapshot)

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
	w.order = removeStringInPlace(w.order, channelID)

	if w.active == channelID {
		w.active = ""
	}

	snapshot := w.snapshotChannelsLocked()
	w.mu.Unlock()

	w.notifyChanged(snapshot)

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

	return w.snapshotChannelsLocked()
}

// Close закрывает все открытые чаты. Предназначен для завершения
// работы приложения.
//
// Персистентный список НЕ трогает и НЕ обнуляет (onChanged не
// зовётся) — это осознанно: Close вызывается при выходе из
// приложения, а не как "забыть все каналы", и пользователь ожидает
// увидеть тот же список при следующем запуске, а не пустой сайдбар.
// Если когда-нибудь понадобится ещё и явный сброс — это должен быть
// отдельный, отдельно названный метод, а не другое поведение того же
// Close в зависимости от контекста вызова.
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

// removeStringInPlace убирает первое совпадение target из items,
// мутируя исходный слайс (классический in-place filter из Go idioms —
// НЕ чистая функция, несмотря на то, что возвращает результат: старый
// backing array переиспользуется). Безопасно ровно потому, что
// единственный вызывающий (Remove) — единственный владелец w.order и
// не хранит других ссылок на тот же слайс где-то ещё.
func removeStringInPlace(items []string, target string) []string {
	out := items[:0]
	for _, item := range items {
		if item != target {
			out = append(out, item)
		}
	}
	return out
}

// snapshotChannelsLocked — список каналов в порядке добавления, для
// List() и для onChanged после каждого изменения. Вызывать только под
// w.mu.
//
// ok-проверка при обращении к w.sessions[id] — не паранойя: order и
// sessions поддерживаются в паре везде в этом файле, но явная проверка
// тут дешевле, чем паника где-то в глубине рендеринга сайдбара, если
// они когда-нибудь разъедутся из-за будущего бага в Add/Remove.
func (w *ChatWorkspace) snapshotChannelsLocked() []domain.Channel {
	channels := make([]domain.Channel, 0, len(w.order))
	for _, id := range w.order {
		if s, ok := w.sessions[id]; ok {
			channels = append(channels, s.channel)
		}
	}
	return channels
}

// notifyChanged сообщает о новом списке каналов через onChanged, если
// он задан. Вызывать БЕЗ w.mu — наружу должен уходить уже готовый
// снапшот, а не сам workspace, и вызывающая сторона (обычно —
// сохранение на диск) не должна иметь возможность случайно дёрнуть
// что-то из ChatWorkspace изнутри колбэка и словить deadlock на том же
// w.mu.
func (w *ChatWorkspace) notifyChanged(channels []domain.Channel) {
	if w.onChanged != nil {
		w.onChanged(channels)
	}
}
