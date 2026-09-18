package app

import (
	"errors"
	"fmt"
	"sync"

	"twitchclient/internal/domain"
)

// ErrNoReusableToken — TryReuse не смог воспользоваться сохранённым
// токеном (ни он сам не годен, ни тихий refresh не прошёл), и по
// определению TryReuse не идёт дальше в интерактивный AuthFlow.
// Сигнал вызывающему коду: показывать пользователю способ войти самому
// (кнопку, ссылку — что уместно в конкретном UI).
var ErrNoReusableToken = errors.New("нет токена, который можно тихо переиспользовать")

// AuthService гарантирует, что у приложения есть рабочий OAuth-токен:
// переиспользует сохранённый, если он ещё действителен, пробует тихо
// обновить его по refresh token, и только если это не сработало —
// запускает AuthFlow заново.
//
// EnsureAuthenticated безопасен для конкурентных вызовов из разных
// горутин (например, основной поток при отправке сообщения и фоновый
// eventsub.Hub при пересоздании подписок после обрыва — оба дёргают
// один и тот же TokenProvider). Это не факультативная предосторожность:
// Twitch выдаёт refresh token одноразовым, и если две горутины увидят
// один и тот же протухший токен одновременно, обе попытаются
// обновиться по одному и тому же refresh token — выиграет только
// первая, вторая получит invalid_grant и провалится в полный
// AuthFlow с nil onPrompt, который молча зависнет до таймаута device
// code. Мьютекс сериализует всю последовательность
// load→refresh→authorize→save, так что вторая горутина, дождавшись
// своей очереди, увидит уже обновлённый (и сохранённый первой) токен
// на шаге Load и вернёт его, не трогая refresher вовсе.
type AuthService struct {
	store     TokenStore
	flow      AuthFlow
	refresher TokenRefresher

	mu sync.Mutex
	// invalidAccessToken — access token, о котором MarkInvalid сообщил,
	// что сервер его отверг живым 401. Сравнение по значению, а не
	// булев флаг: если между тем как вызывающий код получил токен и
	// тем как пожаловался на 401 токен успел смениться (например, его
	// обновил параллельный вызов), инвалидация не должна задеть уже
	// свежий токен — она просто ни с чем не совпадёт.
	invalidAccessToken string
}

// NewAuthService связывает AuthService с конкретной парой store/flow
// и опциональным refresher'ом (может быть nil — тогда обновление
// токена всегда идёт через полный AuthFlow).
func NewAuthService(store TokenStore, flow AuthFlow, refresher TokenRefresher) *AuthService {
	return &AuthService{store: store, flow: flow, refresher: refresher}
}

// EnsureAuthenticated возвращает рабочий токен.
//
// Порядок попыток: (1) сохранённый токен, если он ещё не истёк;
// (2) тихое обновление по refresh token, если он есть и задан
// refresher — пользователь ничего не видит; (3) полный AuthFlow —
// последнее средство, требующее похода в браузер.
//
// Любая ошибка загрузки сохранённого токена (в том числе ErrNoToken)
// трактуется одинаково — как повод пройти шаги (2)/(3). Если
// понадобится различать "токена нет" от "хранилище сломано" — можно
// уточнить позже, не меняя сигнатуру метода.
func (s *AuthService) EnsureAuthenticated(onPrompt func(userCode, verificationURI string)) (domain.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if token, ok := s.tryReuseLocked(); ok {
		return token, nil
	}

	token, err := s.flow.Authorize(onPrompt)
	if err != nil {
		return domain.Token{}, fmt.Errorf("authorize: %v", err)
	}

	if err := s.store.Save(token); err != nil {
		return domain.Token{}, fmt.Errorf("save token: %v", err)
	}

	s.invalidAccessToken = ""
	return token, nil
}

// TryReuse — то же самое, что первые два шага EnsureAuthenticated
// (сохранённый токен / тихий refresh), но НИКОГДА не идёт дальше в
// интерактивный AuthFlow. Нужен composition root'у: на старте
// приложения можно попробовать тихо войти по тому, что уже сохранено
// с прошлого раза, и только если это не получилось — показать
// пользователю способ войти самому. Вызывать EnsureAuthenticated(nil)
// для этого нельзя: при отсутствии годного токена он молча уйдёт в
// Device Code Flow без единого способа показать код пользователю и
// зависнет там до таймаута (см. комментарий у AuthService).
func (s *AuthService) TryReuse() (domain.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if token, ok := s.tryReuseLocked(); ok {
		return token, nil
	}

	return domain.Token{}, ErrNoReusableToken
}

// tryReuseLocked — общая часть EnsureAuthenticated и TryReuse. Вызывать
// только под s.mu: делит с EnsureAuthenticated единую атомарную
// последовательность load→refresh→save, ту самую, из-за отсутствия
// которой раньше была гонка за одноразовый refresh token (см. шапку
// файла).
func (s *AuthService) tryReuseLocked() (domain.Token, bool) {
	token, err := s.store.Load()
	if err == nil && !token.Expired() && token.AccessToken != s.invalidAccessToken {
		return token, true
	}

	if err == nil && token.RefreshToken != "" && s.refresher != nil {
		if refreshed, refreshErr := s.refresher.Refresh(token.RefreshToken); refreshErr == nil {
			if saveErr := s.store.Save(refreshed); saveErr == nil {
				s.invalidAccessToken = ""
				return refreshed, true
			}
			// Сохранить не удалось — не считаем это тихим успехом:
			// в следующий раз пришлось бы обновляться заново тем же
			// (уже использованным) refresh token'ом, а он одноразовый.
			// Дальше по цепочке решит сам вызывающий (EnsureAuthenticated
			// пойдёт в AuthFlow, TryReuse вернёт ErrNoReusableToken).
		}
	}

	return domain.Token{}, false
}

// MarkInvalid сообщает, что access token, который EnsureAuthenticated
// ранее выдал вызывающему коду, был отвергнут сервером живым 401 —
// несмотря на то, что по нашим собственным часам (Token.Expired) он
// ещё должен был быть рабочим. Причины бывают разные: токен отозвали
// вручную, рассинхронизировались часы, сервер укоротил его жизнь и
// т.п. — конкретная причина AuthService не важна, важно, что доверять
// больше не стоит.
//
// После вызова следующий EnsureAuthenticated не станет отдавать этот
// же токен по быстрому пути, даже если Expired() всё ещё говорит
// "нет" — пойдёт по цепочке refresh→AuthFlow ровно так же, как при
// обычном истечении.
func (s *AuthService) MarkInvalid(tok domain.Token) {
	if tok.AccessToken == "" {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.invalidAccessToken = tok.AccessToken
}

// Logout удаляет сохранённый токен, вынуждая пройти авторизацию заново
// при следующем вызове EnsureAuthenticated.
func (s *AuthService) Logout() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.store.Clear()
}
