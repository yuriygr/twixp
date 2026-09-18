package store

import (
	"encoding/json"
	"io/ioutil"
	"os"
	"path/filepath"
	"sync"

	"twitchclient/internal/app"
	"twitchclient/internal/domain"
)

// Store — единственное окно для всего, что приложение сохраняет между
// запусками: настройки, OAuth-токен, список открытых чатов, а в
// будущем — участники каждого чата, история сообщений и что угодно
// ещё. Весь остальной код обращается только к методам Store и никогда
// не трогает файлы на диске напрямую — это единственный пакет, который
// знает про физическую разметку директории данных.
//
// Небольшие, часто читаемые данные (настройки, токен, список каналов)
// живут в одном файле state.json — при их размере дёшево читать и
// перезаписывать целиком на каждое изменение.
//
// Более тяжёлые данные (история сообщений по каждому каналу,
// потенциально большая и растущая) НЕ должны попадать в тот же
// state.json — когда до них дойдёт очередь, им место в отдельных
// файлах внутри этой же директории (например, history/<channel_id>.json),
// подгружаемых по требованию, а не все разом при каждом старте.
// Именно поэтому Store работает с директорией (Dir), а не с одним
// файлом — организационная граница уже заложена.
type Store struct {
	dir string

	mu    sync.Mutex
	state state
}

// state — то, что реально лежит в state.json.
type state struct {
	Token    *domain.Token    `json:"token,omitempty"`
	Channels []domain.Channel `json:"channels,omitempty"`
	Settings *domain.Settings `json:"settings,omitempty"`
}

// New создаёт Store поверх директории dir (создаётся при первом
// сохранении, если ещё не существует) и сразу читает существующий
// state.json, если он есть. Отсутствие файла — не ошибка, нормальный
// случай для первого запуска.
func New(dir string) (*Store, error) {
	s := &Store{dir: dir}

	if err := s.load(); err != nil {
		return nil, err
	}

	return s, nil
}

func (s *Store) statePath() string {
	return filepath.Join(s.dir, "state.json")
}

func (s *Store) load() error {
	data, err := ioutil.ReadFile(s.statePath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	return json.Unmarshal(data, &s.state)
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

	return ioutil.WriteFile(s.statePath(), data, 0600)
}

// ---------------------------------------------------------------
// Токен — под капотом для app.TokenStore (см. адаптер ниже)
// ---------------------------------------------------------------

// LoadToken возвращает сохранённый токен либо app.ErrNoToken, если
// токена ещё нет.
func (s *Store) LoadToken() (domain.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state.Token == nil {
		return domain.Token{}, app.ErrNoToken
	}
	return *s.state.Token, nil
}

// SaveToken сохраняет токен.
func (s *Store) SaveToken(token domain.Token) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.state.Token = &token
	return s.saveStateLocked()
}

// ClearToken удаляет сохранённый токен.
func (s *Store) ClearToken() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.state.Token = nil
	return s.saveStateLocked()
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
