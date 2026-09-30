package store

import (
	"encoding/json"
	"errors"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"sync"

	"twixp/internal/app"
	"twixp/internal/domain"
)

// Store — единственное окно для всего, что приложение сохраняет между
// запусками: настройки, OAuth-токен, список открытых чатов, а в
// будущем — участники каждого чата, история сообщений и что угодно
// ещё. Весь остальной код обращается только к методам Store и никогда
// не трогает файлы на диске напрямую — это единственный пакет, который
// знает про физическую разметку директории данных.
//
// Разметка директории (см. appdir.Dir — %APPDATA%\TwiXP):
//
//	state.json — настройки и список каналов. Не секрет, поэтому
//	             обычный читаемый JSON: его можно и посмотреть, и
//	             поправить руками.
//	token.bin  — OAuth-токен (access + refresh), зашифрованный
//	             Windows DPAPI под текущего пользователя. Единственное
//	             по-настоящему секретное, что мы храним: refresh-токен
//	             даёт доступ к аккаунту надолго, поэтому в открытом
//	             виде на диске он не лежит.
//
// Более тяжёлые данные (история сообщений по каждому каналу,
// потенциально большая и растущая) НЕ должны попадать в state.json —
// когда до них дойдёт очередь, им место в отдельных файлах внутри
// этой же директории, подгружаемых по требованию. Именно поэтому Store
// работает с директорией (Dir), а не с одним файлом.
type Store struct {
	dir string

	mu    sync.Mutex
	state state
	token *domain.Token

	// plaintextLeft — после загрузки в state.json всё ещё лежит токен в
	// открытом виде (не удалось зашифровать или перезаписать файл).
	// Пока флаг стоит, старый источник миграции не удаляется.
	plaintextLeft bool
}

// state — то, что реально лежит в state.json.
type state struct {
	// Token — ТОЛЬКО для чтения старых файлов: до появления token.bin
	// токен хранился здесь открытым текстом. Новые версии его сюда
	// никогда не пишут; найденный при загрузке токен переезжает в
	// token.bin, а поле обнуляется (см. adoptLegacyTokenLocked).
	Token    *domain.Token    `json:"token,omitempty"`
	Channels []domain.Channel `json:"channels,omitempty"`
	Settings *domain.Settings `json:"settings,omitempty"`
}

// tokenMagic — первые байты token.bin: версия формата. Даёт возможность
// однажды сменить схему шифрования и отличить старые файлы от новых.
var tokenMagic = []byte("TXP1")

// tokenEntropy — дополнительная "соль" для DPAPI, привязывающая блоб к
// нашему приложению: другая программа того же пользователя не
// расшифрует его простым вызовом CryptUnprotectData без знания этой
// константы. Это не криптографический секрет (она есть в бинарнике), а
// разделение по приложениям.
var tokenEntropy = []byte("TwiXP/token/v1")

// New создаёт Store поверх директории dir (создаётся при первом
// сохранении, если ещё не существует) и сразу читает существующие
// данные. Отсутствие файлов — не ошибка, нормальный случай для первого
// запуска.
//
// legacyDirs — каталоги, где старые версии хранили данные (data рядом с
// exe). Если в dir ещё нет ни state.json, ни token.bin, а в одном из
// legacyDirs есть старый state.json — выполняется одноразовая
// миграция: файл копируется в dir, токен из него уходит в
// зашифрованный token.bin, а старый файл с открытым токеном
// удаляется. Миграция никогда не перезаписывает существующие данные
// в dir.
func New(dir string, legacyDirs ...string) (*Store, error) {
	s := &Store{dir: dir}

	migratedFrom := s.importLegacy(legacyDirs)

	if err := s.load(); err != nil {
		return nil, err
	}

	if migratedFrom != "" {
		if s.plaintextLeft {
			log.Printf("миграция: токен в %s остался в открытом виде, старый файл не удалён", migratedFrom)
		} else if err := os.Remove(migratedFrom); err != nil {
			log.Printf("миграция: удалить старый %s: %v", migratedFrom, err)
		} else {
			log.Printf("миграция: данные перенесены из %s в %s", migratedFrom, dir)
			// Пустой каталог убираем за собой; если в нём ещё что-то
			// лежит (например, старый лог) — Remove просто откажет.
			os.Remove(filepath.Dir(migratedFrom))
		}
	}

	return s, nil
}

func (s *Store) statePath() string { return filepath.Join(s.dir, "state.json") }
func (s *Store) tokenPath() string { return filepath.Join(s.dir, "token.bin") }

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// importLegacy копирует старый state.json в новый каталог, если
// миграция нужна (см. New). Возвращает путь к скопированному
// источнику либо "" — если мигрировать нечего или не получилось
// (тогда просто стартуем с пустого состояния, ошибка уходит в лог).
func (s *Store) importLegacy(legacyDirs []string) string {
	if exists(s.statePath()) || exists(s.tokenPath()) {
		return ""
	}

	for _, ld := range legacyDirs {
		if sameDir(ld, s.dir) {
			continue
		}

		src := filepath.Join(ld, "state.json")
		data, err := ioutil.ReadFile(src)
		if err != nil {
			if !os.IsNotExist(err) {
				log.Printf("миграция: прочитать %s: %v", src, err)
			}
			continue
		}

		if err := os.MkdirAll(s.dir, 0700); err != nil {
			log.Printf("миграция: создать %s: %v", s.dir, err)
			return ""
		}
		if err := writeFileAtomic(s.statePath(), data, 0600); err != nil {
			log.Printf("миграция: записать %s: %v", s.statePath(), err)
			return ""
		}
		return src
	}
	return ""
}

func sameDir(a, b string) bool {
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	return errA == nil && errB == nil && absA == absB
}

func (s *Store) load() error {
	data, err := ioutil.ReadFile(s.statePath())
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &s.state); err != nil {
			return err
		}
	case !os.IsNotExist(err):
		return err
	}

	s.token = s.readTokenFile()

	if s.state.Token != nil {
		s.mu.Lock()
		if err := s.adoptLegacyTokenLocked(); err != nil {
			log.Println("перенести токен из state.json в token.bin:", err)
			s.plaintextLeft = true
		}
		s.mu.Unlock()
	}

	return nil
}

// readTokenFile читает и расшифровывает token.bin. Любой сбой (нет
// файла, повреждён, расшифровать нельзя — например, сменили пароль
// учётной записи админом или файл скопирован с другой машины) означает
// просто "токена нет": приложение попросит войти заново, что и есть
// правильная реакция. Ошибка, кроме "нет файла", уходит в лог.
func (s *Store) readTokenFile() *domain.Token {
	raw, err := ioutil.ReadFile(s.tokenPath())
	if err != nil {
		if !os.IsNotExist(err) {
			log.Println("прочитать token.bin:", err)
		}
		return nil
	}

	if len(raw) < len(tokenMagic) || string(raw[:len(tokenMagic)]) != string(tokenMagic) {
		log.Println("token.bin: неизвестный формат, токен проигнорирован")
		return nil
	}

	plain, err := unprotect(raw[len(tokenMagic):], tokenEntropy)
	if err != nil {
		log.Println("token.bin: не удалось расшифровать, потребуется новый вход:", err)
		return nil
	}

	var token domain.Token
	if err := json.Unmarshal(plain, &token); err != nil {
		log.Println("token.bin: повреждённое содержимое:", err)
		return nil
	}
	return &token
}

// adoptLegacyTokenLocked переносит токен, найденный в state.json в
// открытом виде (старый формат), в token.bin и перезаписывает
// state.json уже без него. Порядок важен: сначала гарантированно
// сохраняем зашифрованную копию, и только потом стираем открытую —
// иначе сбой посередине потерял бы токен.
func (s *Store) adoptLegacyTokenLocked() error {
	legacy := s.state.Token

	if s.token == nil {
		s.token = legacy
		if err := s.saveTokenLocked(); err != nil {
			// Шифрование не удалось — токен остаётся в памяти (сеанс
			// работает), а файл со старым содержимым не трогаем.
			s.state.Token = legacy
			return err
		}
	}

	s.state.Token = nil
	if err := s.saveStateLocked(); err != nil {
		s.state.Token = legacy
		return err
	}
	return nil
}

// writeFileAtomic пишет во временный файл рядом и переименовывает его
// поверх целевого: обрыв питания или падение посередине записи не
// оставит наполовину записанный (а значит нечитаемый) state.json или
// token.bin — останется либо старая версия, либо новая целиком.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"

	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}

	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// saveStateLocked пишет state.json целиком. Вызывающий код должен уже
// держать s.mu — сам лок не берёт.
func (s *Store) saveStateLocked() error {
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return err
	}

	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}

	return writeFileAtomic(s.statePath(), data, 0600)
}

// saveTokenLocked шифрует s.token через DPAPI и пишет token.bin.
// Вызывающий код должен уже держать s.mu.
func (s *Store) saveTokenLocked() error {
	if s.token == nil {
		return errors.New("нечего сохранять: токена нет")
	}

	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return err
	}

	plain, err := json.Marshal(s.token)
	if err != nil {
		return err
	}

	blob, err := protect(plain, tokenEntropy)
	if err != nil {
		return err
	}

	return writeFileAtomic(s.tokenPath(), append(append([]byte{}, tokenMagic...), blob...), 0600)
}

// ---------------------------------------------------------------
// Токен — под капотом для app.TokenStore (см. адаптер ниже)
// ---------------------------------------------------------------

// LoadToken возвращает сохранённый токен либо app.ErrNoToken, если
// токена ещё нет.
func (s *Store) LoadToken() (domain.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.token == nil {
		return domain.Token{}, app.ErrNoToken
	}
	return *s.token, nil
}

// SaveToken сохраняет токен в зашифрованном виде. Если шифрование или
// запись не удались, токен всё равно остаётся в памяти на время сеанса
// (см. комментарий в AuthService.EnsureAuthenticated), а ошибка
// возвращается вызывающему.
func (s *Store) SaveToken(token domain.Token) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.token = &token
	return s.saveTokenLocked()
}

// ClearToken удаляет сохранённый токен — и из памяти, и с диска.
func (s *Store) ClearToken() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.token = nil
	if err := os.Remove(s.tokenPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ---------------------------------------------------------------
// Список чатов — под капотом для app.ChannelStore (см. адаптер ниже)
// ---------------------------------------------------------------

// LoadChannels возвращает сохранённый список каналов.
func (s *Store) LoadChannels() ([]domain.Channel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	channels := make([]domain.Channel, len(s.state.Channels))
	copy(channels, s.state.Channels)
	return channels, nil
}

// SaveChannels сохраняет список каналов целиком.
func (s *Store) SaveChannels(channels []domain.Channel) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.state.Channels = channels
	return s.saveStateLocked()
}

// ---------------------------------------------------------------
// Настройки
// ---------------------------------------------------------------

// LoadSettings возвращает сохранённые настройки, а если их ещё нет
// (первый запуск, или state.json от версии до появления страницы
// настроек) — domain.DefaultSettings(). В отличие от LoadToken это не
// ошибка ни в каком смысле, поэтому и сигнатура без error.
func (s *Store) LoadSettings() domain.Settings {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state.Settings == nil {
		return domain.DefaultSettings()
	}
	return *s.state.Settings
}

// SaveSettings сохраняет настройки целиком (не по одному полю —
// вызывающий код, см. ui.settingsDialog, уже держит актуальный
// domain.Settings и передаёт его целиком при каждом изменении любого
// переключателя).
func (s *Store) SaveSettings(settings domain.Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.state.Settings = &settings
	return s.saveStateLocked()
}

// ---------------------------------------------------------------
// Адаптеры под узкие интерфейсы app
// ---------------------------------------------------------------
//
// app.TokenStore и app.ChannelStore оба называют свои методы
// Load/Save — один тип в Go не может иметь два метода с одинаковым
// именем и разной сигнатурой. Поэтому Store сам называет методы
// уникально (LoadToken/LoadChannels и т.д.), а под каждый интерфейс
// app отдаётся тонкий адаптер-обёртка. AuthService и ChatWorkspace
// продолжают зависеть от узких интерфейсов и знать не знают, что за
// ними стоит один и тот же файл — это деталь infra.

// AsTokenStore отдаёт app.TokenStore поверх этого Store.
func (s *Store) AsTokenStore() app.TokenStore {
	return tokenAdapter{s}
}

// AsChannelStore отдаёт app.ChannelStore поверх этого же Store.
func (s *Store) AsChannelStore() app.ChannelStore {
	return channelAdapter{s}
}

type tokenAdapter struct{ store *Store }

func (a tokenAdapter) Load() (domain.Token, error) { return a.store.LoadToken() }
func (a tokenAdapter) Save(t domain.Token) error   { return a.store.SaveToken(t) }
func (a tokenAdapter) Clear() error                { return a.store.ClearToken() }

type channelAdapter struct{ store *Store }

func (a channelAdapter) Load() ([]domain.Channel, error) { return a.store.LoadChannels() }
func (a channelAdapter) Save(channels []domain.Channel) error {
	return a.store.SaveChannels(channels)
}

// Компиляционные проверки: адаптеры действительно реализуют то, что
// от них ожидает app.
var _ app.TokenStore = tokenAdapter{}
var _ app.ChannelStore = channelAdapter{}
