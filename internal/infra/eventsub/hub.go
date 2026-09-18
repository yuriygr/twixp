package eventsub

import (
	"bufio"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/url"
	"sync"
	"time"

	"twixp/internal/app"
	"twixp/internal/domain"
	"twixp/internal/infra/nettls"
)

const (
	defaultHost = "eventsub.wss.twitch.tv"
	defaultPath = "/ws"
)

const messageBufferSize = 50

// handshakeTimeout ограничивает TCP-dial + TLS + WS-хендшейк + ожидание
// session_welcome одним разумным сроком: без этого зависший на этом
// этапе dial никогда не даст ensureConnected ни успеха, ни ошибки.
const handshakeTimeout = 15 * time.Second

// defaultKeepaliveTimeout используется, только если Twitch почему-то
// не прислал keepalive_timeout_seconds в session_welcome — на практике
// не должно случаться, это подстраховка.
const defaultKeepaliveTimeout = 10 * time.Second

// keepaliveGrace — запас поверх заявленного Twitch'ом
// keepalive_timeout_seconds на дрожание сети и локальных часов, прежде
// чем считать соединение мёртвым и разрывать его самим (см. readLoop).
const keepaliveGrace = 5 * time.Second

// SubscriptionManager — то, что Hub требует от Helix для управления
// подписками. Узкий контракт вместо прямой зависимости от
// конкретного *helix.Client — helix реализует его структурно, ничего
// не зная про этот пакет.
type SubscriptionManager interface {
	CreateChatSubscription(sessionID string, broadcaster domain.Channel, viewer domain.User) (subscriptionID string, err error)
	// CreateChatNotificationSubscription — отдельная подписка на
	// системные уведомления чата (подписки, рейды, объявления и т.п.,
	// см. eventsub/message.go decodeChatNotification). Тот же scope,
	// что и у CreateChatSubscription — реализация (helix.Client)
	// отвечает за то, чтобы это было именно так.
	CreateChatNotificationSubscription(sessionID string, broadcaster domain.Channel, viewer domain.User) (subscriptionID string, err error)
	// CreateChatMessageDeleteSubscription — отдельная подписка на
	// удаление сообщений (модератор/бот стёр конкретное сообщение, см.
	// eventsub/message.go decodeMessageDelete). Тот же scope, что и у
	// CreateChatSubscription.
	CreateChatMessageDeleteSubscription(sessionID string, broadcaster domain.Channel, viewer domain.User) (subscriptionID string, err error)
	DeleteSubscription(subscriptionID string) error
}

// Hub держит одно WebSocket-соединение к Twitch EventSub и раздаёт
// входящие сообщения чата по подписанным каналам. Одно соединение
// обслуживает произвольное число одновременно открытых чатов.
type Hub struct {
	subs   SubscriptionManager
	viewer domain.User

	// OnRevoked вызывается, когда Twitch отзывает подписку не по
	// нашей инициативе (например, протух токен). Может быть nil.
	// Hub сознательно не печатает и не логирует это сам — решение,
	// как показать это пользователю, остаётся за composition root.
	OnRevoked func(channel domain.Channel, status string)

	// OnResubscribeFailed вызывается, когда после аварийного
	// реконнекта не удалось пересоздать подписку на канал — это
	// ошибка на нашей стороне (сеть, Helix вернул ошибку и т.п.), а
	// не отзыв Twitch, поэтому отдельный колбэк, а не OnRevoked.
	// Канал в этом случае убирается из хаба точно так же, как при
	// отзыве — дальше не пытаемся сами, что делать (переподключать
	// заново через /add или показать ошибку) решает composition root.
	// Может быть nil — тогда ошибка просто молча теряется, как было
	// раньше.
	OnResubscribeFailed func(channel domain.Channel, err error)

	// OnConnected вызывается при каждом успешном подключении к
	// EventSub — и при самом первом (см. ensureConnected), и после
	// каждого аварийного реконнекта (см. handleDisconnect). Может быть
	// nil. Composition root вешает на это обновление статус-бара
	// ("Подключено") — сам Hub ничего не знает про UI.
	OnConnected func()

	// OnDisconnected вызывается сразу при обнаружении аварийного
	// разрыва, до попыток переподключиться (см. handleDisconnect) — то
	// есть "переподключаемся прямо сейчас", а не "уже переподключились".
	// Не вызывается на штатной миграции по session_reconnect
	// (migrateTo) — там соединение не рвётся, а плавно переезжает, для
	// пользователя ничего не меняется. Может быть nil.
	OnDisconnected func()

	mu          sync.Mutex
	conn        *tls.Conn
	br          *bufio.Reader
	sessionID   string
	connectedCh chan struct{}
	connectErr  error
	// keepaliveTimeout — интервал keepalive, который Twitch объявил в
	// session_welcome текущей сессии. readLoop выставляет дедлайн
	// чтения на этот интервал плюс keepaliveGrace перед каждым чтением
	// — если за это время ничего не пришло (даже keepalive), считаем
	// соединение мёртвым и уходим в handleDisconnect, а не висим в
	// io.ReadFull бесконечно.
	keepaliveTimeout time.Duration

	channels map[string]*channelState
}

type channelState struct {
	channel        domain.Channel
	subscriptionID string
	// notificationSubscriptionID — подписка на channel.chat.notification
	// для этого канала (см. subscribe). Пусто, если её не удалось
	// создать — необязательная часть, канал при этом всё равно рабочий,
	// просто без системных уведомлений (см. комментарий в subscribe).
	notificationSubscriptionID string
	// deleteSubscriptionID — подписка на channel.chat.message_delete
	// (см. subscribe). Та же логика необязательности, что и у
	// notificationSubscriptionID: пусто — просто без пометки удалённых
	// сообщений для этого канала.
	deleteSubscriptionID string
	messages             chan domain.ChatMessage
	// deletions — отдельный поток "это сообщение удалено" (см.
	// app.ChatReader.Deletions). Закрывается вместе с messages в
	// dropChannel.
	deletions chan domain.MessageDeletion
}

// NewHub создаёт Hub. viewer — авторизованный пользователь, от чьего
// имени создаются подписки (тот же аккаунт, что отправляет сообщения
// через Helix).
func NewHub(subs SubscriptionManager, viewer domain.User) *Hub {
	return &Hub{
		subs:     subs,
		viewer:   viewer,
		channels: make(map[string]*channelState),
	}
}

// NewReader создаёт несвязанный ChatReader поверх этого хаба. Канал
// привязывается позже вызовом Connect — соответствует фабрике,
// которую ожидает app.ChatWorkspace.
func (h *Hub) NewReader() app.ChatReader {
	return &hubReader{hub: h}
}

// ensureConnected поднимает соединение при первом обращении и
// блокирует конкурентные вызовы до готовности сессии.
//
// Неудача НЕ кэшируется навсегда: если h.connect() вернул ошибку,
// connectedCh сбрасывается обратно в nil, чтобы следующий вызов —
// будь то повторный /add или очередная итерация retry-луп в
// handleDisconnect — реально передёрнул dial заново, а не получил тот
// же самый протухший результат бесконечно.
func (h *Hub) ensureConnected() error {
	h.mu.Lock()
	if h.connectedCh != nil {
		ch := h.connectedCh
		h.mu.Unlock()
		<-ch
		h.mu.Lock()
		err := h.connectErr
		h.mu.Unlock()
		return err
	}

	h.connectedCh = make(chan struct{})
	h.mu.Unlock()

	err := h.connect()

	h.mu.Lock()
	h.connectErr = err
	close(h.connectedCh)
	if err != nil {
		h.connectedCh = nil
	}
	h.mu.Unlock()

	if err == nil {
		go h.readLoop()
		if h.OnConnected != nil {
			h.OnConnected()
		}
	}

	return err
}

func (h *Hub) connect() error {
	conn, br, sessionID, keepalive, err := dialAndHandshake(defaultHost, defaultPath)
	if err != nil {
		return err
	}

	h.applySession(conn, br, sessionID, keepalive)
	return nil
}

// migrateTo выполняет штатную миграцию сессии по session_reconnect:
// открывает НОВОЕ соединение по reconnectURL, дожидается его
// собственного session_welcome, и только потом закрывает старое.
// Подписки при этом не трогаются — Twitch сам переносит их на новую
// сессию, в отличие от аварийного обрыва.
func (h *Hub) migrateTo(reconnectURL string) error {
	u, err := url.Parse(reconnectURL)
	if err != nil {
		return fmt.Errorf("parse reconnect_url: %v", err)
	}

	host := u.Hostname()
	path := u.RequestURI()

	newConn, newBR, newSessionID, keepalive, err := dialAndHandshake(host, path)
	if err != nil {
		return fmt.Errorf("connect to reconnect_url: %v", err)
	}

	if oldConn := h.applySession(newConn, newBR, newSessionID, keepalive); oldConn != nil {
		oldConn.Close()
	}

	return nil
}

// applySession делает переданное соединение активной WS-сессией
// хаба под локом и отдаёт то, что было активно до этого (nil при
// самом первом connect — закрывать там нечего). Закрывать старое
// соединение или нет и когда — решает вызывающий код: migrateTo ждёт,
// пока не убедится, что новое соединение уже отвечает (см. её
// комментарий), прежде чем закрыть старое.
func (h *Hub) applySession(conn *tls.Conn, br *bufio.Reader, sessionID string, keepalive time.Duration) *tls.Conn {
	h.mu.Lock()
	defer h.mu.Unlock()

	old := h.conn
	h.conn = conn
	h.br = br
	h.sessionID = sessionID
	h.keepaliveTimeout = keepalive

	return old
}

// currentSessionID отдаёт актуальный на данный момент sessionID под
// локом — используется, когда подписку нужно ПЕРЕсоздать со свежим
// значением после того, как первая попытка отклонена из-за гонки с
// конкурентным реконнектом (см. subscribe/resubscribeAll и комментарии
// там же).
func (h *Hub) currentSessionID() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sessionID
}

// dialAndHandshake открывает TLS-соединение, проходит WebSocket-
// хендшейк и дожидается первого сообщения, которое обязано быть
// session_welcome. Общий код для начального подключения и для
// миграции на reconnect_url.
//
// Весь этот путь (TCP dial + TLS + WS upgrade + ожидание welcome)
// укладывается в один дедлайн handshakeTimeout — без него зависший на
// любом из этих шагов dial никогда не вернёт управление вызывающему.
// Перед возвратом дедлайн снимается: дальше конкретный лимит на каждое
// чтение выставляет readLoop, исходя из keepalive_timeout_seconds этой
// конкретной сессии.
func dialAndHandshake(host, path string) (*tls.Conn, *bufio.Reader, string, time.Duration, error) {
	dialer := &net.Dialer{Timeout: handshakeTimeout}
	conn, err := tls.DialWithDialer(dialer, "tcp", host+":443", nettls.DialTLSConfig(host))
	if err != nil {
		return nil, nil, "", 0, fmt.Errorf("dial: %v", err)
	}

	if err := conn.SetDeadline(time.Now().Add(handshakeTimeout)); err != nil {
		conn.Close()
		return nil, nil, "", 0, fmt.Errorf("set handshake deadline: %v", err)
	}

	br := bufio.NewReader(conn)
	if err := performHandshake(conn, br, host, path); err != nil {
		conn.Close()
		return nil, nil, "", 0, fmt.Errorf("handshake: %v", err)
	}

	msg, err := readMessage(br, conn)
	if err != nil {
		conn.Close()
		return nil, nil, "", 0, fmt.Errorf("read welcome: %v", err)
	}

	var welcome eventSubEnvelope
	if err := json.Unmarshal([]byte(msg), &welcome); err != nil {
		conn.Close()
		return nil, nil, "", 0, fmt.Errorf("decode welcome: %v", err)
	}
	if welcome.Metadata.MessageType != "session_welcome" {
		conn.Close()
		return nil, nil, "", 0, fmt.Errorf("unexpected first message type: %s", welcome.Metadata.MessageType)
	}

	if err := conn.SetDeadline(time.Time{}); err != nil {
		conn.Close()
		return nil, nil, "", 0, fmt.Errorf("clear handshake deadline: %v", err)
	}

	keepalive := time.Duration(welcome.Payload.Session.KeepaliveTimeoutSeconds) * time.Second
	if keepalive <= 0 {
		keepalive = defaultKeepaliveTimeout
	}

	return conn, br, welcome.Payload.Session.ID, keepalive, nil
}

// readLoop работает в отдельной горутине на весь срок жизни
// соединения: разбирает входящие сообщения по message_type и
// раздаёт notification'ы подписанным каналам.
func (h *Hub) readLoop() {
	for {
		h.mu.Lock()
		br := h.br
		conn := h.conn
		keepalive := h.keepaliveTimeout
		h.mu.Unlock()

		// Если за keepalive-интервал этой сессии (плюс запас) не
		// пришло вообще ничего, даже штатный session_keepalive —
		// соединение мертво. Без этого дедлайна тихо оборвавшееся
		// (без RST/FIN) TCP-соединение висело бы в io.ReadFull
		// навсегда, и реконнект никогда бы не запустился.
		if err := conn.SetReadDeadline(time.Now().Add(keepalive + keepaliveGrace)); err != nil {
			h.handleDisconnect()
			return
		}

		msg, err := readMessage(br, conn)
		if err != nil {
			h.handleDisconnect()
			return
		}

		var env eventSubEnvelope
		if err := json.Unmarshal([]byte(msg), &env); err != nil {
			continue // повреждённое сообщение — пропускаем, не роняем всё соединение
		}

		switch env.Metadata.MessageType {
		case "session_keepalive":
			// соединение живо, действий не требуется
		case "notification":
			h.dispatch(env)
		case "session_reconnect":
			if err := h.migrateTo(env.Payload.Session.ReconnectURL); err != nil {
				// Штатная миграция не удалась — падаем в обычный
				// аварийный путь с пересозданием подписок, это хуже,
				// но не оставляет пользователя вообще без чата.
				h.handleDisconnect()
				return
			}
			// h.conn/h.br уже обновлены migrateTo — следующая итерация
			// этого же цикла продолжит чтение из новой сессии.
		case "revocation":
			h.handleRevocation(env)
		}
	}
}

// dispatch маршрутизирует уведомление по трём известным типам подписки
// (channel.chat.message/notification — как раньше, единым путём через
// dispatchMessage с разными декодерами; channel.chat.message_delete —
// отдельно, в другой канал состояния, см. dispatchDeletion) —
// неизвестный/пустой тип подписки идёт на прежний путь
// (channel.chat.message), а не отбрасывается: так было и до появления
// notification/message_delete, лишняя строгость тут не нужна.
func (h *Hub) dispatch(env eventSubEnvelope) {
	switch env.Payload.Subscription.Type {
	case "channel.chat.message_delete":
		h.dispatchDeletion(env)
	case "channel.chat.notification":
		h.dispatchMessage(env, decodeChatNotification)
	default:
		h.dispatchMessage(env, decodeChatMessage)
	}
}

func (h *Hub) dispatchMessage(env eventSubEnvelope, decode func(eventSubEnvelope) (domain.ChatMessage, string, error)) {
	chatMsg, broadcasterID, err := decode(env)
	if err != nil {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	state, ok := h.channels[broadcasterID]
	if !ok {
		return
	}

	select {
	case state.messages <- chatMsg:
	default:
		// Буфер полон — освобождаем место, теряя самое старое
		// сообщение, а не блокируя read-loop. Зависший потребитель
		// не должен рвать keepalive для всех остальных каналов.
		select {
		case <-state.messages:
		default:
		}
		select {
		case state.messages <- chatMsg:
		default:
		}
	}
}

// dispatchDeletion — тот же приём, что и dispatchMessage (отдельный
// небуферизуемый-блокирующий select с вытеснением самого старого при
// переполнении), только для канала deletions, а не messages.
func (h *Hub) dispatchDeletion(env eventSubEnvelope) {
	deletion, broadcasterID, err := decodeMessageDelete(env)
	if err != nil {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	state, ok := h.channels[broadcasterID]
	if !ok {
		return
	}

	select {
	case state.deletions <- deletion:
	default:
		select {
		case <-state.deletions:
		default:
		}
		select {
		case state.deletions <- deletion:
		default:
		}
	}
}

func (h *Hub) handleDisconnect() {
	h.mu.Lock()
	h.conn = nil
	h.br = nil
	h.connectedCh = nil
	h.mu.Unlock()

	if h.OnDisconnected != nil {
		h.OnDisconnected()
	}

	// Аварийный обрыв — в отличие от штатного session_reconnect,
	// подписки его не переживают. Переподключаемся и пересоздаём
	// подписки на все каналы, которые были активны.
	for {
		if err := h.ensureConnected(); err == nil {
			h.resubscribeAll()
			return
		}
		time.Sleep(5 * time.Second)
	}
}

func (h *Hub) resubscribeAll() {
	h.mu.Lock()
	states := make([]*channelState, 0, len(h.channels))
	for _, s := range h.channels {
		states = append(states, s)
	}
	sessionID := h.sessionID
	h.mu.Unlock()

	for _, s := range states {
		subID, err := h.subs.CreateChatSubscription(sessionID, s.channel, h.viewer)
		if err != nil {
			// Тот же приём, что и в subscribe(): сессия могла смениться
			// уже ПОСЛЕ того, как мы её захватили выше — при переборе
			// нескольких каналов подряд шанс попасть именно в этот
			// момент чуть выше, чем при подписке на один канал, поэтому
			// проверяем на каждой неудаче, а не только один раз в
			// начале функции.
			freshSessionID := h.currentSessionID()
			if freshSessionID != sessionID {
				sessionID = freshSessionID
				subID, err = h.subs.CreateChatSubscription(sessionID, s.channel, h.viewer)
			}
		}
		if err != nil {
			// Не удалось пересоздать подписку — канал остался бы
			// висеть в реестре с подпиской от уже закрытой сессии,
			// то есть тихо перестал бы получать сообщения без единого
			// намёка пользователю. Вместо этого убираем его тем же
			// путём, что и при явном отзыве, и сообщаем наружу.
			if state, ok := h.dropChannel(s.channel.ID); ok && h.OnResubscribeFailed != nil {
				h.OnResubscribeFailed(state.channel, err)
			}
			continue
		}

		// Уведомления — необязательная часть (см. subscribe): провал
		// тут не роняет канал целиком, просто он временно останется
		// без системных уведомлений до следующего реконнекта.
		notifID, err := h.subs.CreateChatNotificationSubscription(sessionID, s.channel, h.viewer)
		if err != nil {
			log.Printf("подписка на уведомления канала %s: %v", s.channel.Name, err)
		}

		// Удаления сообщений — та же необязательность.
		deleteID, err := h.subs.CreateChatMessageDeleteSubscription(sessionID, s.channel, h.viewer)
		if err != nil {
			log.Printf("подписка на удаление сообщений канала %s: %v", s.channel.Name, err)
		}

		h.mu.Lock()
		s.subscriptionID = subID
		s.notificationSubscriptionID = notifID
		s.deleteSubscriptionID = deleteID
		h.mu.Unlock()
	}
}

// dropChannel убирает канал из реестра и закрывает его канал
// сообщений — общий финальный шаг для трёх разных путей, которыми
// канал может перестать быть частью хаба: явный отзыв подписки
// Twitch'ем (handleRevocation), провал пересоздания подписки после
// аварийного реконнекта (resubscribeAll) и ручной unsubscribe. ok=false,
// если канал уже не был в реестре (например, гонка с параллельным
// Remove) — в этом случае вызывающий код не должен звать колбэки
// повторно.
func (h *Hub) dropChannel(channelID string) (*channelState, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	state, ok := h.channels[channelID]
	if !ok {
		return nil, false
	}
	delete(h.channels, channelID)
	close(state.messages)
	close(state.deletions)
	return state, true
}

// handleRevocation убирает канал из реестра при отзыве подписки не по
// нашей инициативе. helix.DeleteSubscription намеренно не вызывается —
// подписки на стороне Twitch уже нет, повторное удаление бессмысленно.
func (h *Hub) handleRevocation(env eventSubEnvelope) {
	broadcasterID := env.Payload.Subscription.Condition.BroadcasterUserID
	if broadcasterID == "" {
		return
	}

	state, ok := h.dropChannel(broadcasterID)
	if ok && h.OnRevoked != nil {
		h.OnRevoked(state.channel, env.Payload.Subscription.Status)
	}
}

// subscribe добавляет канал в реестр хаба и создаёт EventSub-подписку.
func (h *Hub) subscribe(channel domain.Channel) (*channelState, error) {
	if err := h.ensureConnected(); err != nil {
		return nil, fmt.Errorf("connect: %v", err)
	}

	h.mu.Lock()
	if existing, ok := h.channels[channel.ID]; ok {
		h.mu.Unlock()
		return existing, nil
	}
	sessionID := h.sessionID
	h.mu.Unlock()

	subID, err := h.subs.CreateChatSubscription(sessionID, channel, h.viewer)
	if err != nil {
		// Между чтением sessionID выше и этим HTTP-вызовом сессия могла
		// успеть смениться: конкурентный реконнект в другой горутине
		// (см. applySession) обновляет h.sessionID независимо от того,
		// что мы уже собрались с ним делать, и Twitch в этом случае
		// просто отклонит подписку как созданную на неактуальную сессию
		// — отличить эту причину от любой другой ошибки иначе, чем по
		// факту совпадения/несовпадения sessionID, нечем: у Twitch нет
		// отдельного машиночитаемого кода именно под неё.
		//
		// Если sessionID и правда сменился — один повтор со свежим
		// значением закрывает практически всё окно гонки (оно и так
		// узкое, миллисекунды между Unlock и HTTP-вызовом; поймать в
		// него ещё и ВТОРОЙ реконнект подряд было бы уже статистической
		// аномалией). Если НЕ сменился — retry не делаем вовсе: это
		// была бы просто трата ещё одного HTTP-вызова на ошибку с
		// совсем другой причиной (сеть, невалидный канал и т.п.), где
		// повтор ничего не изменит.
		freshSessionID := h.currentSessionID()
		if freshSessionID == sessionID {
			return nil, fmt.Errorf("create subscription: %v", err)
		}

		sessionID = freshSessionID
		subID, err = h.subs.CreateChatSubscription(sessionID, channel, h.viewer)
		if err != nil {
			return nil, fmt.Errorf("create subscription: %v", err)
		}
	}

	// Уведомления (подписки, рейды, объявления и т.п.) — отдельная
	// подписка поверх той же сессии и того же scope (см.
	// helix.Client.CreateChatNotificationSubscription). Необязательная:
	// если она не удалась, канал всё равно открывается и работает —
	// просто без системных уведомлений, а не совсем.
	notifID, err := h.subs.CreateChatNotificationSubscription(sessionID, channel, h.viewer)
	if err != nil {
		log.Printf("подписка на уведомления канала %s: %v", channel.Name, err)
	}

	// Удаления сообщений — та же логика необязательности, что и у
	// уведомлений выше (см. helix.Client.CreateChatMessageDeleteSubscription).
	deleteID, err := h.subs.CreateChatMessageDeleteSubscription(sessionID, channel, h.viewer)
	if err != nil {
		log.Printf("подписка на удаление сообщений канала %s: %v", channel.Name, err)
	}

	state := &channelState{
		channel:                    channel,
		subscriptionID:             subID,
		notificationSubscriptionID: notifID,
		deleteSubscriptionID:       deleteID,
		messages:                   make(chan domain.ChatMessage, messageBufferSize),
		deletions:                  make(chan domain.MessageDeletion, messageBufferSize),
	}

	h.mu.Lock()
	h.channels[channel.ID] = state
	h.mu.Unlock()

	return state, nil
}

// unsubscribe удаляет подписку, убирает канал из реестра и закрывает
// его канал сообщений.
func (h *Hub) unsubscribe(channelID string) error {
	state, ok := h.dropChannel(channelID)
	if !ok {
		return nil
	}

	err := h.subs.DeleteSubscription(state.subscriptionID)

	if state.notificationSubscriptionID != "" {
		if notifyErr := h.subs.DeleteSubscription(state.notificationSubscriptionID); notifyErr != nil && err == nil {
			err = notifyErr
		}
	}

	if state.deleteSubscriptionID != "" {
		if delErr := h.subs.DeleteSubscription(state.deleteSubscriptionID); delErr != nil && err == nil {
			err = delErr
		}
	}

	return err
}
