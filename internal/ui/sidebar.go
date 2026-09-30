//go:build windows
// +build windows

package ui

import (
	"fmt"
	"image"
	"log"
	"unsafe"

	"github.com/lxn/walk"
	"github.com/lxn/win"

	"twixp/internal/app"
	"twixp/internal/domain"
)

// avatarSize — сторона иконки в сайдбаре, в пикселях. 16×16 — это то,
// что просили: минимально осмысленный размер для списка чатов, не
// расталкивающий текст.
const avatarSize = 16

// sidebar — список открытых чатов слева: TableView с аватарками и
// поле добавления нового канала по логину. Сознательно не знает
// ничего про историю сообщений или подписки на них — об этом заботится
// chatPane, извещаемый через onChannelOpened/onChannelActivated.
type sidebar struct {
	view          *walk.TableView
	model         *channelListModel
	addChannelBtn *walk.PushButton

	window      *walk.MainWindow // для Synchronize из горутин; выставляется в New() после build()
	fetchAvatar ImageFetcher
	status      statusReporter

	// onChannelOpened вызывается для КАЖДОГО канала при reload — сигнал
	// "убедись, что канал слушается в фоне", даже если пользователь
	// сейчас смотрит на другой чат.
	onChannelOpened func(domain.Channel)
	// onChannelActivated вызывается, когда пользователь выбрал канал в
	// списке (кликом или программно сразу после добавления) — сигнал
	// "покажи историю этого канала".
	onChannelActivated func(domain.Channel)
	// onChannelRemoved вызывается сразу после того, как канал удалён
	// из workspace через контекстное меню — сигнал "забудь про него"
	// для chatPane (история, отображаемый сейчас текст).
	onChannelRemoved func(channelID string)

	resolve   ChannelResolver
	workspace *app.ChatWorkspace // nil, пока не выполнен успешный вход

	// channelOrder — та же последовательность каналов, что сейчас
	// выставлена в model. TableView адресует элементы по индексу, а не
	// по ID, поэтому по индексу из CurrentIndexChanged нужно самим
	// найти, какому domain.Channel он соответствует.
	channelOrder []domain.Channel

	// avatarsInFlight — какие каналы (по ID) уже в процессе загрузки
	// аватарки прямо сейчас. Без этого повторный reload (пока первая
	// загрузка ещё не завершилась) запустил бы вторую параллельную
	// загрузку той же самой картинки. См. pendingSet/fetchOnce в
	// asyncfetch.go — общий приём, тот же и для бейджей в chatpane.go.
	avatarsInFlight pendingSet
}

// setShowAvatars переключает показ аватарок в списке каналов (см.
// domain.Settings.ShowChannelAvatars) — просто передаточное звено к
// модели, сама логика в channelListModel.setShowAvatars.
func (s *sidebar) setShowAvatars(show bool) {
	s.model.setShowAvatars(show)
}

func newSidebar(fetchAvatar ImageFetcher, status statusReporter, onChannelOpened, onChannelActivated func(domain.Channel), onChannelRemoved func(channelID string)) *sidebar {
	return &sidebar{
		model:              newChannelListModel(),
		fetchAvatar:        fetchAvatar,
		status:             status,
		onChannelOpened:    onChannelOpened,
		onChannelActivated: onChannelActivated,
		onChannelRemoved:   onChannelRemoved,
		avatarsInFlight:    make(pendingSet),
	}
}

// setWorkspace подключает sidebar к рабочей сессии сразу после
// успешного входа. До этого момента sidebar существует (виджеты уже
// построены), но пуст.
func (s *sidebar) setWorkspace(ws *app.ChatWorkspace, resolve ChannelResolver) {
	s.workspace = ws
	s.resolve = resolve
}

// reload перечитывает список открытых чатов из workspace и
// перестраивает и модель, и channelOrder. Вызывать после любого
// изменения состава каналов (добавление, закрытие — не важно, по
// инициативе пользователя через контекстное меню или извне, см.
// MainWindow.NotifyChannelClosed).
//
// Сам вычисляет, какие каналы из предыдущего s.channelOrder пропали, и
// зовёт onChannelRemoved для каждого — единственное место, которое это
// делает. Раньше это было обязанностью каждого вызывающего кода
// отдельно (onDeleteChannelClicked звал onChannelRemoved явно, ДО
// reload) — и путь закрытия канала извне (NotifyChannelClosed) про
// это просто не знал, так что chatPane.forget для него никогда не
// вызывался: история/бейджи/chatters оставались в памяти, хотя канал
// пользователю показывался как закрытый. Теперь оба пути ведут себя
// одинаково, потому что это одна и та же функция, а не два похожих
// куска кода в разных местах.
func (s *sidebar) reload() {
	if s.workspace == nil {
		return // вход ещё не выполнен — показывать нечего
	}
	channels := s.workspace.List()

	live := make(map[string]bool, len(channels))
	for _, ch := range channels {
		live[ch.ID] = true
	}
	for _, ch := range s.channelOrder {
		if !live[ch.ID] {
			s.onChannelRemoved(ch.ID)
		}
	}

	s.channelOrder = channels
	s.model.setChannels(channels)

	for _, ch := range channels {
		s.ensureAvatar(ch)
		s.onChannelOpened(ch)
	}
}

// ensureAvatar запускает загрузку аватарки канала, если у модели ещё
// нет аватарки именно по этой ссылке и она прямо сейчас не грузится.
// Сравнение по ССЫЛКЕ, а не просто "есть ли какая-то аватарка":
// стример мог сменить аватарку, и refreshChannels принесёт новый
// AvatarURL — тогда нужно загрузить новую, а не считать вопрос
// закрытым. Пока грузится новая, старая остаётся на экране.
//
// Сеть — в отдельной горутине (fetchAvatar — блокирующий HTTP-запрос
// либо чтение дискового кэша), применение к модели — через
// Synchronize (см. fetchOnce в asyncfetch.go).
func (s *sidebar) ensureAvatar(channel domain.Channel) {
	if channel.AvatarURL == "" || s.fetchAvatar == nil || s.model.avatarURL(channel.ID) == channel.AvatarURL {
		return
	}

	url := channel.AvatarURL

	// Ключ pending включает ссылку: загрузка старой ссылки не должна
	// мешать запуску загрузки новой.
	fetchOnce(s.window, s.avatarsInFlight, channel.ID+"|"+url,
		fmt.Sprintf("аватар канала %s:", channel.Name),
		func() (apply func(), err error) {
			img, err := s.fetchAvatar(url)
			if err != nil {
				return nil, err
			}

			// toSidebarIcon — это в итоге walk.NewBitmapFromImage, GDI-
			// вызов: оставляем его внутри apply (UI-поток, вызывается
			// уже после Synchronize в fetchOnce), а не здесь, в
			// фоновой горутине.
			return func() {
				// Пока грузилось, канал могли закрыть или его ссылка
				// сменилась ещё раз — не затираем актуальное старым.
				if s.currentAvatarURL(channel.ID) != url {
					return
				}
				icon, err := toSidebarIcon(img)
				if err != nil {
					log.Printf("аватар канала %s: %v", channel.Name, err)
					return
				}
				s.model.setAvatar(channel.ID, url, icon)
			}, nil
		})
}

// currentAvatarURL — актуальная ссылка на аватарку канала по ID из
// текущего списка (пустая строка, если канала в списке уже нет).
func (s *sidebar) currentAvatarURL(channelID string) string {
	for _, ch := range s.channelOrder {
		if ch.ID == channelID {
			return ch.AvatarURL
		}
	}
	return ""
}

// refreshChannels применяет обновлённые данные каналов (аватарка,
// имя — см. app.ChatWorkspace.RefreshProfiles), не трогая состав
// списка. Если состав или порядок не изменился — обновляем модель на
// месте, БЕЗ полного сброса строк (setChannels → PublishRowsReset
// мог бы сбросить выделение, которое пользователь успел сделать), и
// запускаем догрузку аватарок с новыми ссылками. Если состав всё же
// поменялся (канал закрыли параллельно) — обычный reload.
func (s *sidebar) refreshChannels() {
	if s.workspace == nil {
		return
	}

	channels := s.workspace.List()
	if !sameChannelIDs(channels, s.channelOrder) {
		s.reload()
		return
	}

	s.channelOrder = channels
	s.model.updateChannels(channels)

	for _, ch := range channels {
		s.ensureAvatar(ch)
	}
}

func sameChannelIDs(a, b []domain.Channel) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID {
			return false
		}
	}
	return true
}

// selectChannel выделяет канал в списке по ID и делает его активным.
// Не полагаемся только на то, что SetCurrentIndex сам опубликует
// CurrentIndexChanged (см. onSidebarSelectionChanged) — если новый
// индекс совпадёт со старым (например, после удаления канала на его
// место в списке встаёт другой), событие не придёт вообще, и канал
// останется невыбранным в workspace, хотя визуально подсвечен. Прямой
// вызов activate() ниже безопасен и в паре с последующим событием
// (если оно всё-таки придёт) — SetActive/showChannel идемпотентны.
func (s *sidebar) selectChannel(channelID string) {
	for i, ch := range s.channelOrder {
		if ch.ID == channelID {
			_ = s.view.SetCurrentIndex(i)
			s.activate(ch)
			return
		}
	}
}

// activate — общий хвост для трёх путей выбора канала: клика
// пользователя (onSidebarSelectionChanged), программного выбора после
// добавления или удаления (selectChannel). Помечает канал активным в
// workspace и просит chatPane показать его историю.
func (s *sidebar) activate(channel domain.Channel) {
	if err := s.workspace.SetActive(channel.ID); err != nil {
		s.status.logAndShowError(err)
		return
	}
	s.onChannelActivated(channel)
}

func (s *sidebar) onSidebarSelectionChanged() {
	idx := s.view.CurrentIndex()
	if idx < 0 || idx >= len(s.channelOrder) {
		return
	}
	s.activate(s.channelOrder[idx])
}

// onSidebarMouseDown выделяет строку под курсором ПЕРЕД показом
// контекстного меню. walk сам этого не делает (WM_CONTEXTMENU в
// lxn/walk — общий, безо всякой привязки к листвью: см. WM_CONTEXTMENU
// в window.go) — правый клик на невыделенной строке иначе оставил бы
// CurrentIndex указывать на то, что было выделено раньше, и пункты
// меню сработали бы не на том канале. LVM_HITTEST — низкоуровневый
// win32-вызов напрямую в TableView, потому что публичного метода
// "какая строка под точкой x,y" в этой версии walk нет (тот же приём,
// что и GetScrollInfo в chatpane.go, — не в первый раз лезем в win32
// напрямую там, где walk чего-то не даёт).
func (s *sidebar) onSidebarMouseDown(x, y int, button walk.MouseButton) {
	if button != walk.RightButton {
		return
	}

	hti := win.LVHITTESTINFO{Pt: win.POINT{X: int32(x), Y: int32(y)}}
	win.SendMessage(s.view.Handle(), win.LVM_HITTEST, 0, uintptr(unsafe.Pointer(&hti)))

	if hti.Flags == win.LVHT_NOWHERE || int(hti.IItem) < 0 {
		return
	}

	_ = s.view.SetCurrentIndex(int(hti.IItem))
}

// onChannelInfoClicked — пункт контекстного меню "Информация о
// канале". Показывает то немногое, что у нас реально есть о канале
// (Helix GetChannelByLogin даёт только это — см. domain.Channel), без
// придумывания несуществующих полей.
func (s *sidebar) onChannelInfoClicked() {
	channel, ok := s.currentChannel()
	if !ok {
		return
	}

	walk.MsgBox(s.window, "Информация о канале",
		fmt.Sprintf("%s\r\nЛогин: %s\r\nID: %s", channel.DisplayName, channel.Name, channel.ID),
		walk.MsgBoxIconInformation)
}

// onDeleteChannelClicked — пункт контекстного меню "Удалить чат".
// Закрывает TODO о том, что пользователь не может закрыть чат сам
// (раньше это делал только Hub при отзыве подписки). Подтверждение —
// потому что случайный правый клик + Enter/клик мимо не должен молча
// снести открытый чат.
func (s *sidebar) onDeleteChannelClicked() {
	channel, ok := s.currentChannel()
	if !ok {
		return
	}

	if walk.MsgBox(s.window, "Удалить чат",
		fmt.Sprintf("Закрыть чат %s?", channel.DisplayName),
		walk.MsgBoxYesNo|walk.MsgBoxIconQuestion) != walk.DlgCmdYes {
		return
	}

	if err := s.workspace.Remove(channel.ID); err != nil {
		s.status.logAndShowError(fmt.Errorf("удалить чат %s: %v", channel.Name, err))
	}

	// onChannelRemoved для этого канала вызовет сам reload() — он
	// сравнивает workspace.List() с тем, что было в channelOrder до
	// этого вызова, и сам находит разницу (см. reload). Раньше тут был
	// ещё и явный s.onChannelRemoved(channel.ID) — избыточный (и, если
	// Remove выше вернул ошибку и канал на самом деле остался открытым,
	// неверный: chatPane забыл бы историю канала, который на самом деле
	// никуда не делся).
	s.reload()

	// Если что-то ещё осталось открытым — делаем активным первый канал
	// в списке через тот же selectChannel, что и остальные пути выбора
	// (см. его комментарий про то, почему это не просто SetCurrentIndex).
	if len(s.channelOrder) > 0 {
		s.selectChannel(s.channelOrder[0].ID)
	}
}

// nativeClientEdge убирает чёрную WS_BORDER рамку у TableView и
// добавляет вместо неё нативную "утопленную" 3D-окантовку
// (WS_EX_CLIENTEDGE) — ту же самую, что по умолчанию имеют
// LineEdit/TreeView/TextEdit в этой версии walk (см. vendor lxn/walk,
// lineedit.go/treeview.go/textedit.go — везде WS_EX_CLIENTEDGE
// проставлен явно при создании), но TableView не проставляет никогда.
// Без какой-либо рамки белый список на сером фоне composite (серый —
// COLOR_BTNFACE, дефолтный фон класса у walk, см. window.go) выглядит
// как случайно потерянное оформление, а не как осознанный элемент —
// WS_EX_CLIENTEDGE это тот самый стандартный способ Windows обозначить
// "белый контентный колодец на сером chrome" (проводник, блокнот,
// список контактов — everywhere).
//
// lxn/walk жёстко прибивает WS_BORDER к внешнему hwnd самого TableView
// при создании (см. NewTableViewWithCfg в vendor lxn/walk, tableview.go)
// — ни declarative.TableView, ни сам walk.TableView не дают свойства,
// чтобы это поменять. Тот же приём, что и LVM_HITTEST в
// onSidebarMouseDown: лезем в win32 напрямую там, где walk ничего не
// предлагает.
//
// SWP_FRAMECHANGED обязателен для обеих правок: одного SetWindowLong
// недостаточно — Windows не перечитывает стиль/расш.стиль окна и не
// перерисовывает рамку, пока это явно не попросить (см. документацию
// SetWindowPos на MSDN, раздел про SWP_FRAMECHANGED).
//
// Внутренние SysListView32 (hwndFrozenLV/hwndNormalLV), которыми
// TableView рисует сами строки, WS_BORDER не получают — только их общий
// внешний контейнер, который и возвращает view.Handle(). Правки одного
// этого hwnd достаточно.
func nativeClientEdge(view *walk.TableView) {
	hwnd := view.Handle()

	style := win.GetWindowLong(hwnd, win.GWL_STYLE)
	style &^= win.WS_BORDER
	win.SetWindowLong(hwnd, win.GWL_STYLE, style)

	exStyle := win.GetWindowLong(hwnd, win.GWL_EXSTYLE)
	exStyle |= win.WS_EX_CLIENTEDGE
	win.SetWindowLong(hwnd, win.GWL_EXSTYLE, exStyle)

	win.SetWindowPos(hwnd, 0, 0, 0, 0, 0,
		win.SWP_NOMOVE|win.SWP_NOSIZE|win.SWP_NOZORDER|win.SWP_FRAMECHANGED)
}

// currentChannel — канал под текущим выделением сайдбара, если оно
// есть. Общий хвост для пунктов контекстного меню.
func (s *sidebar) currentChannel() (domain.Channel, bool) {
	idx := s.view.CurrentIndex()
	if idx < 0 || idx >= len(s.channelOrder) {
		return domain.Channel{}, false
	}
	return s.channelOrder[idx], true
}

// onAddChannelClicked открывает модальный диалог добавления канала
// (см. addchanneldialog.go) и, если пользователь ввёл логин и нажал
// "Добавить", резолвит его и открывает. Резолв логина и Add —
// блокирующий сетевой I/O, поэтому уходит в отдельную горутину:
// дёргать его прямо в обработчике клика значило бы заморозить окно на
// время сетевого запроса.
func (s *sidebar) onAddChannelClicked() {
	login, ok := showAddChannelDialog(s.window)
	if !ok {
		return
	}

	s.addChannelBtn.SetEnabled(false)
	go s.addChannel(login)
}

// addChannel — тело onAddChannelClicked, в отдельной горутине (см. её
// комментарий).
//
// s.resolve/s.workspace выставляются вместе в setWorkspace, а кнопка
// "Добавить канал" физически существует только на странице чата,
// которая строится уже после успешного входа (см. applySignIn) — то
// есть сейчас нажать её раньше, чем оба поля выставлены, невозможно.
// Проверка ниже — не на случай, что это когда-нибудь станет неверным
// незаметно: без неё nil-поле-функция обернулось бы совсем не
// показательной паникой где-то в глубине горутины, а не понятной
// ошибкой в статус-баре.
func (s *sidebar) addChannel(login string) {
	var channel domain.Channel
	var err error

	if s.resolve == nil || s.workspace == nil {
		err = fmt.Errorf("канал %s: sidebar ещё не подключён к рабочей сессии", login)
	} else {
		channel, err = s.resolve(login)
		if err == nil {
			_, err = s.workspace.Add(channel)
		}
	}

	if s.window == nil {
		// Дальше сообщить об ошибке уже некому и нечем — Synchronize
		// без window означало бы панику вместо тихого выхода.
		return
	}

	s.window.Synchronize(func() {
		s.addChannelBtn.SetEnabled(true)

		if err != nil {
			s.status.logAndShowError(fmt.Errorf("канал %s: %v", login, err))
			return
		}

		s.reload()
		s.selectChannel(channel.ID)
	})
}

// channelListModel — модель для TableView сайдбара: список каналов с
// аватарками. walk.TableModelBase даёт события (RowsReset и т.п.),
// RowCount/Value/Image — наши. walk сам определяет через приведение
// типов, что модель реализует ImageProvider — отдельно регистрировать
// это нигде не нужно.
type channelListModel struct {
	walk.TableModelBase

	channels []domain.Channel
	avatars  map[string]*walk.Bitmap // по Channel.ID; отсутствие в карте = ещё не загружена
	// avatarURLs — по какой ссылке загружена картинка из avatars (тот
	// же ключ). Нужна, чтобы заметить смену аватарки у стримера: если
	// AvatarURL канала стал другим, картинку надо загрузить заново.
	avatarURLs map[string]string

	// showAvatars — переключатель из настроек (см. domain.Settings).
	// true по умолчанию (newChannelListModel) — так модель ведёт себя
	// ровно как раньше, пока applySettings не подъехал с настоящим
	// значением после входа.
	showAvatars bool
}

func newChannelListModel() *channelListModel {
	return &channelListModel{
		avatars:     make(map[string]*walk.Bitmap),
		avatarURLs:  make(map[string]string),
		showAvatars: true,
	}
}

func (m *channelListModel) RowCount() int {
	return len(m.channels)
}

func (m *channelListModel) Value(row, _ int) interface{} {
	return m.channels[row].DisplayName
}

// Image — часть интерфейса walk.ImageProvider. nil означает "иконки
// пока нет" — walk просто оставляет место под неё пустым, а не ломает
// вёрстку строки. Тем же nil пользуемся и для "аватарки выключены в
// настройках" — не нужно отдельно менять TableView, достаточно того,
// что уже умеет ImageProvider.
func (m *channelListModel) Image(row int) interface{} {
	if !m.showAvatars {
		return nil
	}
	if bmp, ok := m.avatars[m.channels[row].ID]; ok {
		return bmp
	}
	return nil
}

// setShowAvatars переключает показ аватарок и просит TableView
// перечитать все строки — сами скачанные картинки при этом никуда не
// деваются (avatars не трогаем), просто Image() либо отдаёт их, либо
// нет.
func (m *channelListModel) setShowAvatars(show bool) {
	if m.showAvatars == show {
		return
	}
	m.showAvatars = show
	m.PublishRowsReset()
}

// setChannels заменяет список каналов целиком (см. sidebar.reload) —
// зовётся при каждом добавлении/удалении канала, а не только на входе.
//
// avatars чистим здесь же: ключ там — channelID, а сам кэш раньше не
// подчищался НИКОГДА, ни при удалении канала, ни тут — на каждое
// открыть-закрыть-открыть-другой копился ещё один *walk.Bitmap
// (HBITMAP, GDI-объект с жёстким лимитом на процесс). Дешевле всего
// чистить сразу тут, а не заводить отдельный forget-путь только под
// это: набор каналов и так пересобирается целиком на каждое изменение.
func (m *channelListModel) setChannels(channels []domain.Channel) {
	m.channels = channels

	live := make(map[string]bool, len(channels))
	for _, ch := range channels {
		live[ch.ID] = true
	}
	for id := range m.avatars {
		if !live[id] {
			delete(m.avatars, id)
			delete(m.avatarURLs, id)
		}
	}

	m.PublishRowsReset()
}

// updateChannels подменяет данные каналов на месте — тот же набор и
// порядок, что уже в модели (это вызывающий код проверяет сам, см.
// sidebar.refreshChannels), — и просит перерисовать строки без
// PublishRowsReset, чтобы не сбросить выделение.
func (m *channelListModel) updateChannels(channels []domain.Channel) {
	m.channels = channels
	for row := range channels {
		m.PublishRowChanged(row)
	}
}

// avatarURL — ссылка, по которой загружена текущая аватарка канала
// (пустая строка, если аватарка ещё не загружена).
func (m *channelListModel) avatarURL(channelID string) string {
	return m.avatarURLs[channelID]
}

func (m *channelListModel) setAvatar(channelID, url string, bmp *walk.Bitmap) {
	m.avatars[channelID] = bmp
	m.avatarURLs[channelID] = url
	for row, ch := range m.channels {
		if ch.ID == channelID {
			m.PublishRowChanged(row)
			return
		}
	}
}

// toSidebarIcon уменьшает картинку до avatarSize×avatarSize и
// заворачивает в walk.Bitmap. Ресайз — свой, нарочно простой
// (nearest-neighbor): для иконки 16×16 качество некритично, а тащить
// внешнюю зависимость (golang.org/x/image, ещё один коммит для
// пиновки под Go 1.10.8) ради этого не стоит.
func toSidebarIcon(img image.Image) (*walk.Bitmap, error) {
	resized := domain.ResizeNearest(img, avatarSize)
	return walk.NewBitmapFromImage(resized)
}
