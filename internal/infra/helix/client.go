package helix

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"twixp/internal/app"
	"twixp/internal/domain"
	"twixp/internal/infra/nettls"
)

const defaultBaseURL = "https://api.twitch.tv/helix"

// TokenProvider возвращает текущий рабочий access token. В боевом коде
// это обёртка над app.AuthService.EnsureAuthenticated — пакет helix
// намеренно не знает про AuthService напрямую, чтобы отвечать только
// за HTTP-контракт Helix, а не за то, как устроена авторизация.
type TokenProvider func() (domain.Token, error)

// Client — HTTP-клиент Twitch Helix API.
type Client struct {
	ClientID string
	HTTP     *http.Client
	Token    TokenProvider
	BaseURL  string

	// SenderID — Twitch user ID авторизованного пользователя, от чьего
	// имени отправляются сообщения. Должен быть установлен до первого
	// вызова Send — обычно через GetAuthenticatedUser при старте
	// приложения.
	SenderID string

	// InvalidateToken — опциональный колбэк, вызываемый, когда Twitch
	// живьём ответил 401 на токен, который Token() только что выдал
	// как рабочий (обычно обёртка над app.AuthService.MarkInvalid).
	// nil-safe как Hub.OnRevoked: если не задан, живой 401 просто
	// возвращается вызывающему кодом как обычная ошибка — поведение
	// как было раньше, без повторных попыток.
	InvalidateToken func(domain.Token)
}

// NewClient создаёт Client с реальным Helix API и разумным таймаутом.
// TLS-конфигурация (доверенные корни, минимальная версия) — общая для
// всего приложения, см. internal/infra/nettls.
func NewClient(clientID string, token TokenProvider) *Client {
	return &Client{
		ClientID: clientID,
		HTTP:     nettls.NewHTTPClient(nettls.DefaultTimeout),
		Token:    token,
		BaseURL:  defaultBaseURL,
	}
}

// userDTO — один пользователь в ответе Get Users (только нужные поля).
type userDTO struct {
	ID              string `json:"id"`
	Login           string `json:"login"`
	DisplayName     string `json:"display_name"`
	ProfileImageURL string `json:"profile_image_url"`
	Description     string `json:"description"`
	BroadcasterType string `json:"broadcaster_type"`
	CreatedAt       string `json:"created_at"`
}

type usersResponse struct {
	Data []userDTO `json:"data"`
}

type apiError struct {
	Error   string `json:"error"`
	Status  int    `json:"status"`
	Message string `json:"message"`
}

// GetUserByLogin ищет пользователя (канал) по логину.
func (c *Client) GetUserByLogin(login string) (domain.User, error) {
	return c.getUser(url.Values{"login": {login}})
}

// GetAuthenticatedUser возвращает пользователя, которому принадлежит
// текущий токен — то есть аккаунт, от чьего имени будут отправляться
// сообщения. Обычно вызывается один раз при старте, чтобы заполнить
// SenderID.
func (c *Client) GetAuthenticatedUser() (domain.User, error) {
	return c.getUser(url.Values{})
}

func (c *Client) getUser(query url.Values) (domain.User, error) {
	users, err := c.getUsers(query)
	if err != nil {
		return domain.User{}, err
	}
	if len(users) == 0 {
		return domain.User{}, fmt.Errorf("user not found")
	}
	return users[0], nil
}

// fetchUsers — общая часть Get Users: возвращает всех найденных
// пользователей (может быть меньше запрошенных — например, если
// аккаунт удалён или заблокирован; пустой результат — не ошибка).
func (c *Client) fetchUsers(query url.Values) ([]userDTO, error) {
	resp, err := c.do("GET", "/users", query, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, decodeAPIError(resp)
	}

	var out usersResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// getUsers — fetchUsers, сведённый к минимуму, нужному чату (domain.User).
func (c *Client) getUsers(query url.Values) ([]domain.User, error) {
	dtos, err := c.fetchUsers(query)
	if err != nil {
		return nil, err
	}

	users := make([]domain.User, 0, len(dtos))
	for _, u := range dtos {
		users = append(users, domain.User{
			ID:          u.ID,
			Login:       u.Login,
			DisplayName: u.DisplayName,
			AvatarURL:   u.ProfileImageURL,
		})
	}
	return users, nil
}

// GetUserProfile возвращает публичный профиль пользователя по ID — для
// окна "Профиль пользователя". По ID, а не по логину: ID есть в каждом
// сообщении и не меняется, а логин может смениться. Права не нужны.
func (c *Client) GetUserProfile(userID string) (domain.UserProfile, error) {
	dtos, err := c.fetchUsers(url.Values{"id": {userID}})
	if err != nil {
		return domain.UserProfile{}, err
	}
	if len(dtos) == 0 {
		return domain.UserProfile{}, fmt.Errorf("пользователь не найден")
	}

	u := dtos[0]
	profile := domain.UserProfile{
		ID:              u.ID,
		Login:           u.Login,
		DisplayName:     u.DisplayName,
		AvatarURL:       u.ProfileImageURL,
		Description:     u.Description,
		BroadcasterType: u.BroadcasterType,
	}
	// Дата — "2016-12-14T20:32:28Z". Не разобралась — оставляем нулевой,
	// окно просто не покажет строку про возраст аккаунта.
	if t, err := time.Parse(time.RFC3339, u.CreatedAt); err == nil {
		profile.CreatedAt = t
	}
	return profile, nil
}

// maxUsersPerRequest — сколько id Twitch принимает в одном Get Users.
const maxUsersPerRequest = 100

// GetChannelsByIDs возвращает актуальные данные каналов по их ID —
// для обновления того, что могло измениться с момента, когда канал
// был добавлен (аватарка, отображаемое имя, логин). По ID, а не по
// логину, потому что логин как раз и может смениться; ID — нет.
//
// Запрос идёт пачками по 100 (лимит Twitch), обычно это один вызов
// на все каналы сразу. Каналы, которых Twitch не вернул (аккаунт
// удалён или заблокирован), в результате просто отсутствуют.
func (c *Client) GetChannelsByIDs(ids []string) ([]domain.Channel, error) {
	var channels []domain.Channel

	for start := 0; start < len(ids); start += maxUsersPerRequest {
		end := start + maxUsersPerRequest
		if end > len(ids) {
			end = len(ids)
		}

		users, err := c.getUsers(url.Values{"id": ids[start:end]})
		if err != nil {
			return nil, err
		}
		for _, u := range users {
			channels = append(channels, domain.Channel{
				ID:          u.ID,
				Name:        u.Login,
				DisplayName: u.DisplayName,
				AvatarURL:   u.AvatarURL,
			})
		}
	}

	return channels, nil
}

// GetChannelByLogin находит канал (вещателя) по логину — удобная
// обёртка над GetUserByLogin для случаев, когда нужен именно
// domain.Channel (например, пользователь добавляет новый чат в
// сайдбар по имени канала).
func (c *Client) GetChannelByLogin(login string) (domain.Channel, error) {
	user, err := c.GetUserByLogin(login)
	if err != nil {
		return domain.Channel{}, err
	}
	return domain.Channel{
		ID:          user.ID,
		Name:        user.Login,
		DisplayName: user.DisplayName,
		AvatarURL:   user.AvatarURL,
	}, nil
}

type sendMessageRequest struct {
	BroadcasterID        string `json:"broadcaster_id"`
	SenderID             string `json:"sender_id"`
	Message              string `json:"message"`
	ReplyParentMessageID string `json:"reply_parent_message_id,omitempty"`
}

// sendMessageResponse — тело ответа Send Chat Message. Ключевое место —
// IsSent/DropReason: Twitch решает вопросы модерации (бан, таймаут,
// режим "только для подписчиков", медленный режим, AutoMod и т.п.) уже
// ПОСЛЕ приёма запроса и сообщает об отказе тем же 200 OK, через эти
// поля — НЕ HTTP-ошибкой. Проверки одного StatusCode недостаточно, см.
// Client.Send.
type sendMessageResponse struct {
	Data []struct {
		MessageID  string `json:"message_id"`
		IsSent     bool   `json:"is_sent"`
		DropReason *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"drop_reason"`
	} `json:"data"`
}

type createSubscriptionRequest struct {
	Type      string            `json:"type"`
	Version   string            `json:"version"`
	Condition map[string]string `json:"condition"`
	Transport map[string]string `json:"transport"`
}

type createSubscriptionResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

// CreateChatSubscription создаёт EventSub-подписку на сообщения чата
// канала broadcaster, доставляемую в уже открытую WebSocket-сессию
// sessionID. viewer — авторизованный пользователь, от чьего имени
// делается подписка (тот же аккаунт, что и SenderID для отправки).
//
// Реализует eventsub.SubscriptionManager структурно — этот пакет
// ничего не знает про eventsub и не импортирует его.
func (c *Client) CreateChatSubscription(sessionID string, broadcaster domain.Channel, viewer domain.User) (string, error) {
	return c.createChatSubscription("channel.chat.message", sessionID, broadcaster, viewer)
}

// CreateChatNotificationSubscription — то же самое, но на системные
// уведомления чата (подписки, рейды, объявления и т.п. — EventSub
// channel.chat.notification, см. eventsub/message.go decodeChatNotification).
// Тот же scope (user:read:chat), что и у обычных сообщений — заводить
// отдельно и просить пользователя войти заново не нужно.
func (c *Client) CreateChatNotificationSubscription(sessionID string, broadcaster domain.Channel, viewer domain.User) (string, error) {
	return c.createChatSubscription("channel.chat.notification", sessionID, broadcaster, viewer)
}

// CreateChatMessageDeleteSubscription — то же самое, но на удаление
// сообщений (модератор/бот стёр конкретное сообщение, см.
// eventsub/message.go decodeMessageDelete). Тот же scope
// (user:read:chat), что и у остальных двух — заводить отдельно и
// просить пользователя войти заново не нужно.
func (c *Client) CreateChatMessageDeleteSubscription(sessionID string, broadcaster domain.Channel, viewer domain.User) (string, error) {
	return c.createChatSubscription("channel.chat.message_delete", sessionID, broadcaster, viewer)
}

// CreateChatSettingsSubscription — то же самое, но на изменения
// режимов чата (только смайлики, только подписчики, медленный режим и
// т.п. — EventSub channel.chat_settings.update, см.
// eventsub/message.go decodeChatSettingsUpdate). Тот же scope
// (user:read:chat), что и у остальных — заново входить не нужно.
func (c *Client) CreateChatSettingsSubscription(sessionID string, broadcaster domain.Channel, viewer domain.User) (string, error) {
	return c.createChatSubscription("channel.chat_settings.update", sessionID, broadcaster, viewer)
}

// chatSettingsResponse — тело ответа Get Chat Settings. Только поля,
// доступные без прав модератора (non_moderator_chat_delay* требуют
// moderator:read:chat_settings и нам не нужны). Длительности — nil,
// когда соответствующий режим выключен.
type chatSettingsResponse struct {
	Data []struct {
		EmoteMode            bool `json:"emote_mode"`
		FollowerMode         bool `json:"follower_mode"`
		FollowerModeDuration *int `json:"follower_mode_duration"` // минуты
		SlowMode             bool `json:"slow_mode"`
		SlowModeWaitTime     *int `json:"slow_mode_wait_time"` // секунды
		SubscriberMode       bool `json:"subscriber_mode"`
		UniqueChatMode       bool `json:"unique_chat_mode"`
	} `json:"data"`
}

// GetChatModes возвращает текущие режимы чата канала — начальное
// состояние для плашек; дальнейшие изменения приходят по подписке
// (CreateChatSettingsSubscription). Права модератора не нужны:
// без moderator_id Twitch отдаёт всё, кроме модераторских полей.
func (c *Client) GetChatModes(broadcaster domain.Channel) (domain.ChatModes, error) {
	resp, err := c.do("GET", "/chat/settings", url.Values{"broadcaster_id": {broadcaster.ID}}, nil)
	if err != nil {
		return domain.ChatModes{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return domain.ChatModes{}, decodeAPIError(resp)
	}

	var out chatSettingsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return domain.ChatModes{}, err
	}
	if len(out.Data) == 0 {
		return domain.ChatModes{}, fmt.Errorf("chat settings: пустой ответ")
	}

	d := out.Data[0]
	modes := domain.ChatModes{
		EmoteOnly:       d.EmoteMode,
		SubscribersOnly: d.SubscriberMode,
		FollowersOnly:   d.FollowerMode,
		SlowMode:        d.SlowMode,
		UniqueOnly:      d.UniqueChatMode,
	}
	if d.FollowerModeDuration != nil {
		modes.FollowersMinutes = *d.FollowerModeDuration
	}
	if d.SlowModeWaitTime != nil {
		modes.SlowSeconds = *d.SlowModeWaitTime
	}
	return modes, nil
}

func (c *Client) createChatSubscription(subType, sessionID string, broadcaster domain.Channel, viewer domain.User) (string, error) {
	body := createSubscriptionRequest{
		Type:    subType,
		Version: "1",
		Condition: map[string]string{
			"broadcaster_user_id": broadcaster.ID,
			"user_id":             viewer.ID,
		},
		Transport: map[string]string{
			"method":     "websocket",
			"session_id": sessionID,
		},
	}

	data, err := json.Marshal(body)
	if err != nil {
		return "", err
	}

	resp, err := c.do("POST", "/eventsub/subscriptions", nil, data)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		return "", decodeAPIError(resp)
	}

	var out createSubscriptionResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if len(out.Data) == 0 {
		return "", fmt.Errorf("helix: subscription created but no id returned")
	}

	return out.Data[0].ID, nil
}

// DeleteSubscription удаляет ранее созданную EventSub-подписку.
func (c *Client) DeleteSubscription(subscriptionID string) error {
	resp, err := c.do("DELETE", "/eventsub/subscriptions", url.Values{"id": {subscriptionID}}, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		return decodeAPIError(resp)
	}

	return nil
}

type badgesResponse struct {
	Data []struct {
		SetID    string `json:"set_id"`
		Versions []struct {
			ID         string `json:"id"`
			ImageURL1x string `json:"image_url_1x"`
		} `json:"versions"`
	} `json:"data"`
}

// GlobalBadges возвращает глобальный каталог бейджей — те, что
// одинаковы на всех каналах (модератор, Prime, турбо и т.п.). Вызывать
// один раз за сессию (см. chatPane.setGlobalBadgeCatalog) — этот набор
// не меняется от канала к каналу, в отличие от ChannelBadges.
func (c *Client) GlobalBadges() (map[domain.Badge]string, error) {
	return c.fetchBadges("/chat/badges/global", nil)
}

// ChannelBadges возвращает каталог бейджей САМОГО канала — кастомные
// уровни подписки, кастомные бейджи за биты и т.п., которых нет в
// GlobalBadges. Ключ — domain.Badge{Name: set_id, Version: id}, то же
// самое, что приходит в domain.ChatMessage.Badges из EventSub (см.
// eventsub/message.go, decodeChatMessage) — можно сравнивать напрямую.
//
// Специально не объединяет результат с GlobalBadges: у каждого из двух
// каталогов свой жизненный цикл (глобальный — один на сессию, канальный
// — по одному на каждый открытый канал), сливать их в один кэш на
// стороне UI дешевле и даёт бейджам из уже готового глобального
// каталога появиться раньше, чем догрузится каталог конкретного канала
// (см. chatPane.resolveBadge/badgeImageURL).
func (c *Client) ChannelBadges(channel domain.Channel) (map[domain.Badge]string, error) {
	return c.fetchBadges("/chat/badges", url.Values{"broadcaster_id": {channel.ID}})
}

func (c *Client) fetchBadges(path string, query url.Values) (map[domain.Badge]string, error) {
	resp, err := c.do("GET", path, query, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, decodeAPIError(resp)
	}

	var out badgesResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}

	badges := make(map[domain.Badge]string)
	for _, set := range out.Data {
		for _, v := range set.Versions {
			badges[domain.Badge{Name: set.SetID, Version: v.ID}] = v.ImageURL1x
		}
	}
	return badges, nil
}

// Send реализует app.ChatSender через Helix "Send Chat Message".
// replyToMessageID — ID сообщения, на которое отвечаем (Twitch
// "reply"), либо "" для обычного сообщения.
//
// Важный нюанс этого эндпоинта: успешный HTTP-статус (200 OK) НЕ
// гарантирует, что сообщение реально появилось в чате. Решения о
// модерации (бан, таймаут, режим "только для подписчиков", медленный
// режим, сработавший AutoMod и т.п.) принимаются уже после приёма
// запроса и сообщаются тем же 200 OK через поля is_sent/drop_reason в
// теле ответа — см. https://discuss.dev.twitch.com/t/59079. Слишком
// длинное сообщение (>500 символов) — исключение, оно отклоняется
// обычной HTTP-ошибкой (400) ещё до этой проверки.
func (c *Client) Send(channel domain.Channel, text string, replyToMessageID string) error {
	if c.SenderID == "" {
		return fmt.Errorf("helix: SenderID is not set — call GetAuthenticatedUser first")
	}

	data, err := json.Marshal(sendMessageRequest{
		BroadcasterID:        channel.ID,
		SenderID:             c.SenderID,
		Message:              text,
		ReplyParentMessageID: replyToMessageID,
	})
	if err != nil {
		return err
	}

	resp, err := c.do("POST", "/chat/messages", nil, data)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return decodeAPIError(resp)
	}

	// 200 OK тут ещё не значит "сообщение реально появилось в чате" —
	// см. комментарий у sendMessageResponse. Отдельно от обычных
	// HTTP-ошибок (decodeAPIError выше): это не сбой запроса, а
	// содержательный ответ "запрос принят, но отправить отказались".
	var out sendMessageResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return fmt.Errorf("decode send response: %v", err)
	}
	if len(out.Data) == 0 {
		return fmt.Errorf("helix: пустой ответ на отправку сообщения")
	}

	if result := out.Data[0]; !result.IsSent {
		reason := "отклонено Twitch"
		switch {
		case result.DropReason != nil && result.DropReason.Message != "":
			reason = result.DropReason.Message
		case result.DropReason != nil && result.DropReason.Code != "":
			reason = result.DropReason.Code
		}
		return fmt.Errorf("сообщение не отправлено: %s", reason)
	}

	return nil
}

// do выполняет один HTTP-запрос к Helix с текущим токеном и, если
// Twitch ответил живым 401, один раз (не более) принудительно
// обновляет токен через InvalidateToken и повторяет запрос заново.
// body передаётся как готовый []byte (не io.Reader), потому что при
// повторе тело нужно прочитать второй раз — nil для запросов без
// тела (GET/DELETE).
//
// Ровно один повтор и только на 401 — намеренно: это не универсальный
// retry/backoff на сетевые сбои или 429/5xx, только конкретный случай
// "токен на вид рабочий, а сервер его не принял".
func (c *Client) do(method, path string, query url.Values, body []byte) (*http.Response, error) {
	tok, err := c.Token()
	if err != nil {
		return nil, fmt.Errorf("get token: %v", err)
	}

	resp, err := c.doOnce(method, path, query, tok, body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusUnauthorized || c.InvalidateToken == nil {
		return resp, nil
	}

	resp.Body.Close()
	c.InvalidateToken(tok)

	tok, err = c.Token()
	if err != nil {
		return nil, fmt.Errorf("get token after 401: %v", err)
	}

	return c.doOnce(method, path, query, tok, body)
}

func (c *Client) doOnce(method, path string, query url.Values, tok domain.Token, body []byte) (*http.Response, error) {
	u := c.BaseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequest(method, u, reader)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	req.Header.Set("Client-Id", c.ClientID)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return c.HTTP.Do(req)
}

func decodeAPIError(resp *http.Response) error {
	var apiErr apiError
	json.NewDecoder(resp.Body).Decode(&apiErr)
	if apiErr.Message != "" {
		return fmt.Errorf("helix: status %d: %s", resp.StatusCode, apiErr.Message)
	}
	return fmt.Errorf("helix: unexpected status %d", resp.StatusCode)
}

// Компиляционная проверка: Client действительно реализует app.ChatSender.
var _ app.ChatSender = (*Client)(nil)
