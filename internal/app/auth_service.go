package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"

	"twixp/internal/domain"
)

// ErrNoReusableToken — TryReuse не смог воспользоваться сохранённым
// токеном (ни он сам не годен, ни тихий refresh не прошёл), и по
// определению TryReuse не идёт дальше в интерактивный AuthFlow.
// Сигнал вызывающему коду: показывать пользователю способ войти самому
// (кнопку, ссылку — что уместно в конкретном UI).
//
// Тот же самый сигнал отдаёт и EnsureAuthenticated(nil) — вызов с nil
// onPrompt семантически и есть "тихая попытка, без интерактива", то
// есть TryReuse под другим именем (см. EnsureAuthenticated).
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

	// cancelMu/cancelAuthorize — отдельный, короткий лок специально
	// под отмену текущего интерактивного AuthFlow (см. Logout). НЕ тот
	// же mu, что сериализует load→refresh→authorize→save: пока
	// EnsureAuthenticated стоит внутри Authorize, она держит mu на всё
	// это время (по замыслу — не более одной интерактивной попытки
	// сразу), и если бы Logout тоже приходилось ждать mu, отменить
	// зависшую попытку было бы нечем — тем же самым mu Logout и
	// заблокирован. cancelMu берётся на доли секунды (сохранить один
	// указатель на функцию) и никогда не удерживается на время
	// сетевого I/O, поэтому не мешает основной сериализации.
	cancelMu        sync.Mutex
	cancelAuthorize context.CancelFunc
}

// NewAuthService связывает AuthService с конкретной парой store/flow
// и опциональным refresher'ом (может быть nil — тогда обновление
// токена всегда идёт через полный AuthFlow).
//
// store и flow обязательны — паникуем сразу, а не откладываем до
// первого вызова: без них AuthService не может сделать буквально
// ничего полезного, и был бы разве что вводящим в заблуждение nil-panic
// где-то в глубине EnsureAuthenticated вместо понятного сообщения
// прямо в точке создания.
func NewAuthService(store TokenStore, flow AuthFlow, refresher TokenRefresher) *AuthService {
	if store == nil {
		panic("app.NewAuthService: store is nil")
	}
	if flow == nil {
		panic("app.NewAuthService: flow is nil")
	}
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
//
// onPrompt == nil означает "интерактив невозможен, показывать код
// некому" (см. AuthFlow.Authorize) — в этом случае шаг (3) не
// запускается вовсе: EnsureAuthenticated(nil) эквивалентен TryReuse()
// и возвращает ErrNoReusableToken, если шаги (1)/(2) не сработали, а
// не проваливается в интерактивный AuthFlow с некому-звонить onPrompt,
// который иначе завис бы там до таймаута device code — и на всё это
// время держал бы mu, блокируя TryReuse/MarkInvalid/Logout из других
// горутин. Именно так этот метод и используется как TokenProvider для
// helix.Client (см. main.go) — там нет UI-контекста для интерактивного
// входа, и его там не должно быть: не тот вызывающий код.
func (s *AuthService) EnsureAuthenticated(onPrompt func(userCode, verificationURI string)) (domain.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if token, ok := s.tryReuseLocked(); ok {
		return token, nil
	}

	if onPrompt == nil {
		return domain.Token{}, ErrNoReusableToken
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.cancelMu.Lock()
	s.cancelAuthorize = cancel
	s.cancelMu.Unlock()
	defer func() {
		s.cancelMu.Lock()
		s.cancelAuthorize = nil
		s.cancelMu.Unlock()
		cancel() // на случай успешного/неуспешного возврата без отмены — не течь context'ами
	}()

	token, err := s.flow.Authorize(ctx, onPrompt)
	if err != nil {
		return domain.Token{}, fmt.Errorf("authorize: %v", err)
	}

	if err := s.store.Save(token); err != nil {
		// Пользователь только что прошёл полный интерактивный вход в
		// браузере, Twitch выдал рабочий токен — терять его из-за того,
		// что диск не принял запись, нельзя ни в коем случае: без этого
		// пользователь и поработать не смог бы (токен потерян), и
		// заново логиниться пришлось бы на пустом месте. Персистентность
		// — это только про переживание перезапуска, а не про то, можно
		// ли пользоваться токеном прямо сейчас; теряем её одну, а не всё
		// сразу.
		log.Println("сохранить токен:", err)
	}

	s.invalidAccessToken = ""
	return token, nil
}

// TryReuse — то же самое, что первые два шага EnsureAuthenticated
// (сохранённый токен / тихий refresh), но НИКОГДА не идёт дальше в
// интерактивный AuthFlow. Нужен composition root'у: на старте
// приложения можно попробовать тихо войти по тому, что уже сохранено
// с прошлого раза, и только если это не получилось — показать
// пользователю способ войти самому.
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
//
// Если прямо сейчас где-то идёт интерактивный AuthFlow (пользователь
// ещё не ввёл код, EnsureAuthenticated стоит внутри Authorize и держит
// s.mu) — сначала отменяем его через ctx (см. cancelAuthorize), и
// только потом идём за s.mu: иначе Logout сам встал бы в очередь за
// тем же локом и ждал бы, пока не истечёт таймаут device code, — то
// самое "отменить вход невозможно в принципе", которого эта отмена и
// призвана избежать. cancelMu, а не s.mu — намеренно, см. комментарий
// у поля cancelAuthorize.
func (s *AuthService) Logout() error {
	s.cancelMu.Lock()
	if s.cancelAuthorize != nil {
		s.cancelAuthorize()
	}
	s.cancelMu.Unlock()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.invalidAccessToken = ""
	return s.store.Clear()
}
