package auth

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"twixp/internal/app"
	"twixp/internal/domain"
	"twixp/internal/infra/nettls"
)

const (
	defaultDeviceCodeURL = "https://id.twitch.tv/oauth2/device"
	defaultTokenURL      = "https://id.twitch.tv/oauth2/token"
)

// DeviceFlow реализует app.AuthFlow через Device Code Grant Flow —
// без client secret, ровно то, что нужно публичному десктоп-клиенту.
// Механика провалидирована отдельным спайком до переноса сюда.
type DeviceFlow struct {
	ClientID string
	Scopes   []string
	HTTP     *http.Client

	// DeviceCodeURL и TokenURL по умолчанию указывают на реальный
	// Twitch, но подменяемы в тестах на httptest-сервер.
	DeviceCodeURL string
	TokenURL      string
}

// NewDeviceFlow создаёт DeviceFlow с реальными эндпоинтами Twitch
// и разумным таймаутом по умолчанию. TLS-конфигурация — общая для
// всего приложения, см. internal/infra/nettls: раньше у DeviceFlow и
// у helix.Client были независимо заведённые http.Client с
// одинаковыми на вид, но нигде не переиспользуемыми настройками — и
// именно поэтому чинить TLS для одного было недостаточно.
func NewDeviceFlow(clientID string, scopes []string) *DeviceFlow {
	return &DeviceFlow{
		ClientID:      clientID,
		Scopes:        scopes,
		HTTP:          nettls.NewHTTPClient(nettls.DefaultTimeout),
		DeviceCodeURL: defaultDeviceCodeURL,
		TokenURL:      defaultTokenURL,
	}
}

type deviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
}

type tokenResponse struct {
	AccessToken  string   `json:"access_token"`
	RefreshToken string   `json:"refresh_token"`
	ExpiresIn    int      `json:"expires_in"`
	Scope        []string `json:"scope"`
	TokenType    string   `json:"token_type"`
}

type apiError struct {
	Status  int    `json:"status"`
	Message string `json:"message"`
}

// decodeAPIError разбирает тело ответа Twitch OAuth с ошибкой. Ошибку
// самого декодирования тихо игнорируем — тогда apiErr просто остаётся
// нулевым, а resp.StatusCode всё равно есть, чтобы дать осмысленное
// сообщение (см. tokenErrorf).
func decodeAPIError(resp *http.Response) apiError {
	var apiErr apiError
	_ = json.NewDecoder(resp.Body).Decode(&apiErr)
	return apiErr
}

// tokenErrorf форматирует "status %d: %s" — общий хвост для
// requestDeviceCode и Refresh. pollForToken сюда не заходит: ему нужно
// само сообщение (apiErr.Message) для разбора pending/slow_down, а не
// сразу готовый error.
func tokenErrorf(resp *http.Response) error {
	apiErr := decodeAPIError(resp)
	return fmt.Errorf("status %d: %s", resp.StatusCode, apiErr.Message)
}

// toDomain переносит ответ Twitch в domain.Token, посчитав абсолютное
// время истечения из ExpiresIn (секунды от "сейчас"). Общий хвост для
// Authorize и Refresh — оба получают tokenResponse с одинаковой
// формой и должны привести её к domain.Token одинаково.
func (t tokenResponse) toDomain() domain.Token {
	return domain.Token{
		AccessToken:  t.AccessToken,
		RefreshToken: t.RefreshToken,
		Scopes:       t.Scope,
		ExpiresAt:    time.Now().Add(time.Duration(t.ExpiresIn) * time.Second),
	}
}

// Authorize реализует app.AuthFlow.
func (f *DeviceFlow) Authorize(onPrompt func(userCode, verificationURI string)) (domain.Token, error) {
	log.Println("device flow: запрашиваю device code у", f.DeviceCodeURL)
	dc, err := f.requestDeviceCode()
	if err != nil {
		log.Println("device flow: requestDeviceCode провалился:", err)
		return domain.Token{}, fmt.Errorf("request device code: %v", err)
	}
	log.Println("device flow: device code получен, user_code =", dc.UserCode)

	if onPrompt != nil {
		onPrompt(dc.UserCode, dc.VerificationURI)
	}

	tok, err := f.pollForToken(dc)
	if err != nil {
		log.Println("device flow: pollForToken провалился:", err)
		return domain.Token{}, fmt.Errorf("poll for token: %v", err)
	}
	log.Println("device flow: токен получен")

	return tok.toDomain(), nil
}

func (f *DeviceFlow) requestDeviceCode() (*deviceCodeResponse, error) {
	form := url.Values{}
	form.Set("client_id", f.ClientID)
	form.Set("scopes", strings.Join(f.Scopes, " "))

	resp, err := f.HTTP.PostForm(f.DeviceCodeURL, form)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, tokenErrorf(resp)
	}

	var dc deviceCodeResponse
	if err := json.NewDecoder(resp.Body).Decode(&dc); err != nil {
		return nil, err
	}
	return &dc, nil
}

func (f *DeviceFlow) pollForToken(dc *deviceCodeResponse) (*tokenResponse, error) {
	interval := time.Duration(dc.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	deadline := time.Now().Add(time.Duration(dc.ExpiresIn) * time.Second)

	for time.Now().Before(deadline) {
		time.Sleep(interval)

		form := url.Values{}
		form.Set("client_id", f.ClientID)
		form.Set("device_code", dc.DeviceCode)
		form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")

		resp, err := f.HTTP.PostForm(f.TokenURL, form)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode == http.StatusOK {
			var tok tokenResponse
			decErr := json.NewDecoder(resp.Body).Decode(&tok)
			resp.Body.Close()
			if decErr != nil {
				return nil, decErr
			}
			return &tok, nil
		}

		apiErr := decodeAPIError(resp)
		resp.Body.Close()

		msg := strings.ToLower(apiErr.Message)
		switch {
		case strings.Contains(msg, "pending"):
			// пользователь ещё не подтвердил — ждём дальше
		case strings.Contains(msg, "slow"):
			interval += 5 * time.Second
		default:
			return nil, fmt.Errorf("twitch returned: %q (status %d)", apiErr.Message, resp.StatusCode)
		}
	}

	return nil, fmt.Errorf("authorization timed out")
}

// Refresh реализует app.TokenRefresher — обновляет access token по
// refresh token без участия пользователя. Публичным клиентам (Device
// Code Flow) client_secret для этого не нужен.
//
// Важно: Twitch выдаёт refresh token одноразовым — использованный
// становится недействителен, а взамен приходит НОВЫЙ refresh token,
// который обязательно нужно сохранить вместо старого (это делает
// вызывающий код — AuthService — через Save после успешного Refresh).
func (f *DeviceFlow) Refresh(refreshToken string) (domain.Token, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", f.ClientID)

	resp, err := f.HTTP.PostForm(f.TokenURL, form)
	if err != nil {
		return domain.Token{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return domain.Token{}, tokenErrorf(resp)
	}

	var tok tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return domain.Token{}, err
	}

	return tok.toDomain(), nil
}

// Компиляционная проверка: DeviceFlow действительно реализует app.AuthFlow.
var _ app.AuthFlow = (*DeviceFlow)(nil)

// ...и app.TokenRefresher — один и тот же тип закрывает оба контракта,
// используя один и тот же client_id/HTTP-клиент/токен-эндпоинт.
var _ app.TokenRefresher = (*DeviceFlow)(nil)
