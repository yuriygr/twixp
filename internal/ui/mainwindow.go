//go:build windows
// +build windows

// Package ui — GUI-слой на lxn/walk для Windows XP. Зависит только от
// internal/app и internal/domain (UI → App → Domain, инфру не видит
// вовсе), собирается отдельно от остального ядра: Go 1.10.8 + GOPATH
// (GO111MODULE=off) + запиненные коммиты lxn/walk/lxn/win/
// gopkg.in/Knetic/govaluate.v3 до 24.08.2018 — см. Makefile.
//
// Окно — единственный интерфейс пользователя с приложением, поэтому
// оно обязано открыться в любом случае, даже если авторизация,
// хранилище или сеть на старте не в порядке. Все такие ошибки не
// прерывают запуск — они уходят в лог (стандартный log.Println,
// composition root настраивает его вывод в файл — GUI-подсистема
// (-H windowsgui) не имеет консоли, писать в stdout некому) и в
// нативный статус-бар внизу окна.
//
// MainWindow отвечает только за жизненный цикл окна целиком: сборку
// самого окна (единственный постоянный элемент — контейнер под
// страницы, см. pagehost.go) и путь сообщения об ошибках. Экран входа
// (signinpage.go) и экран чата (chatpage.go, sidebar.go, chatpane.go)
// — отдельные "страницы", переключаемые через pageHost; MainWindow
// только решает, какая страница должна быть показана сейчас
// (EnsureSignedIn/applySignIn), не занимаясь их внутренней логикой.
package ui

import (
	"fmt"
	"image"
	_ "image/jpeg" // декодер для аватарок Twitch — регистрируется через image.Decode
	_ "image/png"  // Twitch отдаёт и то, и другое в зависимости от аватарки
	"log"

	"github.com/lxn/walk"
	"github.com/lxn/walk/declarative"
	"github.com/lxn/win"

	"twixp/internal/app"
	"twixp/internal/domain"
)

// ChannelResolver находит канал по логину (то же самое, что делает
// helix.Client.GetChannelByLogin). UI-слою не нужно знать про
// infra/helix напрямую, поэтому это функция, а не тип из infra.
type ChannelResolver func(login string) (domain.Channel, error)

// ImageFetcher скачивает и декодирует картинку по URL — обычную
// публичную HTTPS-ссылку на CDN, токен не нужен. Используется для двух
// разных вещей, которым обеим нужно ровно одно и то же ("скачать +
// декодировать"): аватарки каналов в сайдбаре (см. AvatarURL) и иконки
// бейджей в истории чата (см. BadgeCatalog, chatPane.resolveBadge).
// UI-слою не нужно знать про net/http или infra/nettls напрямую —
// composition root даёт готовую реализацию. Ресайз до нужного размера —
// уже забота конкретного потребителя (см. toSidebarIcon в sidebar.go),
// а не этой функции: это чисто вопрос того, как картинка будет
// отрисована, к загрузке отношения не имеет.
type ImageFetcher func(url string) (image.Image, error)

// BadgeCatalog возвращает каталог бейджей САМОГО канала — какой URL
// картинки соответствует каждой паре (set_id, id) бейджа, приходящей в
// domain.ChatMessage.Badges (см. eventsub/message.go). То же самое, что
// делает helix.Client.ChannelBadges — UI-слою не нужно знать про
// infra/helix напрямую.
type BadgeCatalog func(channel domain.Channel) (map[domain.Badge]string, error)

// GlobalBadgeCatalog — то же самое, но без привязки к каналу: набор
// бейджей, одинаковых везде (модератор, Prime, турбо и т.п.). Отдельный
// тип, а не BadgeCatalog с игнорируемым аргументом — сигнатура сама
// документирует, что канал тут не нужен, а не полагается на комментарий
// рядом с вызовом. См. helix.Client.GlobalBadges.
type GlobalBadgeCatalog func() (map[domain.Badge]string, error)

// SettingsLoader читает сохранённые настройки (то же самое, что делает
// store.Store.LoadSettings). Без error в сигнатуре — "настроек ещё
// нет" не ошибка, а нормальный случай первого запуска (см.
// domain.DefaultSettings).
type SettingsLoader func() domain.Settings

// SettingsSaver сохраняет настройки целиком (то же самое, что делает
// store.Store.SaveSettings) — целиком, а не по одному изменённому
// полю: settingsDialog и так уже держит актуальный domain.Settings
// целиком и передаёт его при каждом изменении любого переключателя.
type SettingsSaver func(domain.Settings) error

// SignInResult — то, что нужно UI-слою после успешного входа: рабочий
// ChatWorkspace, способ находить канал по логину и данные самого
// вошедшего пользователя. Собирает их composition root (там же, где
// инфра — Helix-клиент, EventSub-хаб), UI сами infra-типы не видит.
type SignInResult struct {
	Workspace *app.ChatWorkspace
	Resolve   ChannelResolver
	// Viewer — авторизованный пользователь (тот же аккаунт, что
	// отправляет сообщения). chatPane использует его логин/отображаемое
	// имя, чтобы подсвечивать сообщения с упоминанием (см.
	// chatPane.setViewer).
	Viewer domain.User
	// Badges — каталог бейджей конкретного канала (см. BadgeCatalog).
	Badges BadgeCatalog
	// GlobalBadges — общий для всех каналов каталог (см.
	// GlobalBadgeCatalog) — chatPane запускает его загрузку сразу же,
	// один раз за сессию, не дожидаясь первого сообщения с бейджем (см.
	// chatPane.setGlobalBadgeCatalog).
	GlobalBadges GlobalBadgeCatalog
}

// SignIn выполняет вход целиком: авторизацию и разворачивание рабочей
// сессии (Helix-клиент, EventSub-хаб, ChatWorkspace, восстановление
// сохранённых чатов).
//
// onPrompt == nil — сигнал "попробуй только тихо, без интерактива"
// (аналог app.AuthService.TryReuse): используется автоматически при
// старте окна, чтобы вернувшемуся пользователю не пришлось лишний раз
// жать "Войти". onPrompt != nil — сигнал "можно и интерактивно", он же
// и есть способ показать пользователю код устройства; используется
// только в ответ на клик по кнопке "Войти".
type SignIn func(onPrompt func(userCode, verificationURI string)) (SignInResult, error)

// statusReporter — общий канал сообщений об ошибках/статусе для
// MainWindow и его дочерних контроллеров (sidebar, chatPane). Оба
// реализованы через MainWindow, чтобы ошибка из любого места всегда
// попадала в лог и статус-бар одним и тем же путём — независимо от
// того, где именно она произошла.
type statusReporter interface {
	logAndShowError(err error)
	setStatus(text string)
}

// MainWindow — окно приложения целиком. Единственный постоянный
// элемент содержимого — контейнер под страницы (pageHost, см.
// pagehost.go): сам он никогда не скрывается и не пересоздаётся,
// меняется только то, какая страница внутри него сейчас построена
// (страница входа или страница чата). Это заодно и единственный
// надёжный способ избежать целого класса багов старой lxn/walk с
// невидимыми при создании виджетами — см. подробности в pagehost.go.
type MainWindow struct {
	window     *walk.MainWindow
	statusItem *walk.StatusBarItem
	// connectionStatusItem — статус соединения EventSub ("Подключено"/
	// "Переподключение...", см. SetConnectionStatus). Пусто до первого
	// подключения (до входа Hub ещё не существует).
	connectionStatusItem *walk.StatusBarItem
	pages                *pageHost

	sidebar  *sidebar
	chatPane *chatPane

	signIn SignIn

	// settings — текущие настройки (см. domain.Settings). Загружаются
	// один раз в New(), до входа — часть из них (AlwaysOnTop) имеет
	// смысл применить сразу, не дожидаясь чата. settingsDialog читает и
	// мутирует этот же экземпляр через MainWindow, а не держит свою
	// копию — единственный источник истины на всё время работы
	// приложения.
	settings domain.Settings
	// saveSettings — куда сохранять settings после каждого изменения в
	// settingsDialog (см. applySettings).
	saveSettings SettingsSaver

	// wasMinimized — было ли окно в предыдущий SizeChanged свёрнутым.
	// Нужно только для onMainWindowSizeChanged/redrawClientEdges — см.
	// комментарий там.
	wasMinimized bool

	// trayIcon — значок в системном трее (см. setupTrayIcon), виден всё
	// время работы приложения независимо от того, показано окно или
	// спрятано. nil, если создать не удалось (см. setupTrayIcon) — тогда
	// закрытие крестиком просто закрывает приложение как обычно, без
	// сворачивания.
	trayIcon *walk.NotifyIcon
	// exiting — true, когда закрытие запрошено по-настоящему (пункт
	// "Выход" в меню трея), а не крестиком окна — onWindowClosing
	// смотрит на этот флаг, чтобы отличить "спрятать в трей" от
	// "действительно выйти", и во втором случае не перехватывать закрытие.
	exiting bool
	// trayHintShown — показывали ли уже баллон "приложение продолжает
	// работать в трее" — один раз за сессию, при первом сворачивании, а
	// не при каждом.
	trayHintShown bool
}

// New строит окно (но не запускает событийный цикл — см. Run) вместе
// с постоянным контейнером под страницы — сам контейнер ни разу не
// бывает невидимым, см. MainWindow. Композиционный корень должен сразу
// после New запустить EnsureSignedIn в отдельной горутине — сама New
// это не делает специально, чтобы не гнаться за собственным ещё не
// присвоенным указателем (см. EnsureSignedIn).
func New(signIn SignIn, fetchImage ImageFetcher, loadSettings SettingsLoader, saveSettings SettingsSaver) (*MainWindow, error) {
	m := &MainWindow{signIn: signIn, settings: loadSettings(), saveSettings: saveSettings}

	// chatPane создаётся первым, чтобы sidebar мог захватить его
	// методы (ensureWatching/showChannel) как коллбэки — на момент
	// вызова newSidebar нужен уже существующий (пусть ещё без виджетов)
	// указатель на chatPane, а не наоборот. Сами виджеты обоих
	// появятся позже, при первом показе страницы чата — см.
	// applySignIn/chatpage.go.
	m.chatPane = newChatPane(m, fetchImage)
	m.sidebar = newSidebar(fetchImage, m, m.chatPane.ensureWatching, func(channel domain.Channel) {
		m.chatPane.showChannel(channel.ID)
		m.chatPane.ensureWatching(channel)
	}, m.chatPane.forget)

	if err := m.build(); err != nil {
		return nil, err
	}

	// build() — единственное место, где в итоге появляется настоящий
	// *walk.MainWindow (через AssignTo в declarative-дереве). sidebar/
	// chatPane используют его только для Synchronize из фоновых
	// горутин — это не зависит от того, построены ли уже их
	// собственные виджеты (см. выше), поэтому эту ссылку можно отдать
	// сразу, не дожидаясь входа.
	m.sidebar.window = m.window
	m.chatPane.window = m.window

	// Применяем настройки сразу — тумблеры chatView/sidebar безопасны
	// для вызова и до входа (см. их nil-проверки на widget): просто
	// запоминают значение, а настоящая перерисовка случится позже,
	// когда виджеты появятся (см. chatView.attach). AlwaysOnTop же
	// требует m.window, который к этому моменту уже есть.
	m.applySettings(m.settings)

	return m, nil
}

// Run запускает событийный цикл. Блокируется, пока пользователь не
// закроет окно.
func (m *MainWindow) Run() {
	m.window.Run()
}

func (m *MainWindow) build() error {
	icon, _ := walk.NewIconFromResourceId(2)

	var pageContainer *walk.Composite

	err := (declarative.MainWindow{
		AssignTo: &m.window,
		Icon:     icon,
		Title:    "TwiXP",
		// 800x480 — экран Eee PC 701, конечной цели этого клиента.
		Size:    declarative.Size{Width: 800, Height: 480},
		MinSize: declarative.Size{Width: 480, Height: 320},
		Layout:  declarative.VBox{MarginsZero: true, SpacingZero: true},
		StatusBarItems: []declarative.StatusBarItem{
			// Порядок важен: части статус-бара у walk не умеют сами
			// растягиваться под окно, когда их больше одной (это работает
			// только для единственной части) — первым двум даём разумную
			// фиксированную ширину, последней — заведомо избыточную
			// (10000), чтобы визуально забрать всё оставшееся место:
			// отрисовка всё равно обрежется по реальной ширине бара,
			// реального переполнения не бывает.
			{AssignTo: &m.connectionStatusItem, Text: "Подключение...", Width: 160},
			{AssignTo: &m.statusItem, Text: "Запуск...", Width: 10000},
		},
		Children: []declarative.Widget{
			declarative.Composite{
				AssignTo: &pageContainer,
				Layout:   declarative.HBox{MarginsZero: true, SpacingZero: true},
			},
		},
	}).Create()
	if err != nil {
		return fmt.Errorf("создать окно: %v", err)
	}

	m.pages = newPageHost(pageContainer)

	// На сворачивание/восстановление окна — см. onMainWindowSizeChanged.
	m.window.SizeChanged().Attach(m.onMainWindowSizeChanged)

	// См. onMouseWheel — единственное место, где вообще нужно знать про
	// колесо мыши на уровне всего окна, а не конкретного виджета.
	m.window.MouseWheel().Attach(m.onMouseWheel)

	if err := m.setupTrayIcon(icon); err != nil {
		// Не фатально — просто не будет сворачивания в трей, крестик
		// закроет приложение как обычно, безо всякого трея.
		log.Println("трей:", err)
	} else {
		m.window.Closing().Attach(m.onWindowClosing)
	}

	return nil
}

// setupTrayIcon создаёт значок в системном трее с меню "Открыть"/
// "Выход" и делает его видимым сразу — значок живёт всё время работы
// приложения, а не появляется только при сворачивании (иначе правый
// клик "а как выйти, если окно спрятано" был бы неоткуда сделать).
func (m *MainWindow) setupTrayIcon(icon *walk.Icon) error {
	ni, err := walk.NewNotifyIcon()
	if err != nil {
		return fmt.Errorf("создать значок трея: %v", err)
	}

	if icon != nil {
		if err := ni.SetIcon(icon); err != nil {
			return fmt.Errorf("иконка трея: %v", err)
		}
	}
	if err := ni.SetToolTip("TwiXP"); err != nil {
		return fmt.Errorf("подсказка трея: %v", err)
	}

	openAction := walk.NewAction()
	_ = openAction.SetText("Открыть")
	openAction.Triggered().Attach(m.restoreFromTray)
	if err := ni.ContextMenu().Actions().Add(openAction); err != nil {
		return fmt.Errorf("пункт меню трея: %v", err)
	}

	closeAction := walk.NewAction()
	_ = closeAction.SetText("Закрыть")
	closeAction.Triggered().Attach(func() {
		// exiting — сигнал onWindowClosing не перехватывать это закрытие
		// (см. комментарий у поля): без него Close() ниже просто спрятал
		// бы окно ещё раз, и выйти из трея было бы вообще никак нельзя.
		m.exiting = true
		ni.Dispose() // иначе значок "призраком" повисит в трее до наведения мышью — мелкая, но известная болячка Windows-приложений, которые об этом забывают
		m.window.Close()
	})
	if err := ni.ContextMenu().Actions().Add(closeAction); err != nil {
		return fmt.Errorf("пункт меню трея: %v", err)
	}

	// Левый одиночный клик тоже разворачивает окно, а не только двойной
	// — так вели себя трей-иконки большинства мессенджеров эпохи, под
	// которую сделан весь остальной интерфейс, не заставлять же тут
	// целиться именно в двойной клик.
	ni.MouseDown().Attach(func(x, y int, button walk.MouseButton) {
		if button == walk.LeftButton {
			m.restoreFromTray()
		}
	})

	if err := ni.SetVisible(true); err != nil {
		return fmt.Errorf("показать значок трея: %v", err)
	}

	m.trayIcon = ni
	return nil
}

// onWindowClosing перехватывает закрытие крестиком: вместо выхода
// прячет окно и оставляет приложение работать в трее — EventSub
// продолжает получать сообщения в фоне, и вся накопленная за сессию
// история/список чаттеров для автодополнения/кэш бейджей никуда не
// пропадают (сейчас всё это только в памяти процесса — см. известные
// ограничения в README). exiting (пункт "Выход" в меню трея) — обходит
// этот перехват, давая закрытию пройти по-настоящему.
func (m *MainWindow) onWindowClosing(canceled *bool, reason walk.CloseReason) {
	if m.exiting {
		return
	}

	*canceled = true
	m.window.SetVisible(false)

	if !m.trayHintShown && m.trayIcon != nil {
		m.trayHintShown = true
		_ = m.trayIcon.ShowInfo("TwiXP", `Приложение продолжает работать в трее. Чтобы закрыть его полностью, кликните правой кнопкой по значку и выберите "Выход".`)
	}
}

// restoreFromTray возвращает окно из трея — по клику на "Открыть" в
// меню значка или по левому клику на сам значок (см. setupTrayIcon).
func (m *MainWindow) restoreFromTray() {
	m.window.SetVisible(true)
	win.SetForegroundWindow(m.window.Handle())
}

// applySettings применяет settings ко всем виджетам, которые от них
// зависят, и запоминает как текущие (m.settings) — источник истины для
// settingsDialog. Безопасен для вызова в любой момент, в том числе до
// входа (см. New()) — тумблеры chatView/sidebar сами разбираются, есть
// ли уже виджет, которому есть что перерисовывать (см. их nil-проверки
// на widget).
func (m *MainWindow) applySettings(s domain.Settings) {
	m.settings = s

	m.chatPane.view.setShowTimestamps(s.ShowTimestamps)
	m.chatPane.view.setShowBadges(s.ShowBadges)
	m.chatPane.view.setHighlightMentions(s.HighlightMentions)
	m.chatPane.view.setFontSize(s.FontSize)
	m.chatPane.setMentionAutocomplete(s.MentionAutocomplete)
	m.sidebar.setShowAvatars(s.ShowChannelAvatars)

	topmost := win.HWND_NOTOPMOST
	if s.AlwaysOnTop {
		topmost = win.HWND_TOPMOST
	}
	win.SetWindowPos(m.window.Handle(), topmost, 0, 0, 0, 0, win.SWP_NOMOVE|win.SWP_NOSIZE)
}

// onSettingsClicked открывает диалог настроек (см. settingsdialog.go) —
// кнопка "Настройки" под списком каналов (см. chatpage.go).
func (m *MainWindow) onSettingsClicked() {
	showSettingsDialog(m.window, m.settings, m.saveSettings, m.applySettings)
}

// onMainWindowSizeChanged перерисовывает утопленную 3D-окантовку
// (WS_EX_CLIENTEDGE) у виджетов, у которых она есть (LineEdit — по
// умолчанию, TableView и chatView — вручную через nativeClientEdge /
// SetWindowPos в sidebar.go/chatview.go), когда окно только что
// развернули из свёрнутого состояния.
//
// Причина та же, что уже была найдена в этом проекте для другого бага
// (см. п.6 старой истории — виджет, невидимый в момент разметки,
// навсегда остаётся нулевого размера): BoxLayout пропускает реальный
// SetWindowPos у ребёнка, если заново посчитанный размер совпадает с
// уже закэшированным. После разворачивания размер как раз возвращается
// к тому же, что был ДО сворачивания, — то есть настоящего WM_SIZE у
// LineEdit/TableView/chatView может и не случиться, а рамка, стёртая
// при сворачивании, сама себя перерисовать не может.
//
// GetClientRect у свёрнутого окна всегда возвращает 0×0 — этим и
// пользуемся, чтобы отличить "просто изменили размер" от "было
// свёрнуто, а теперь развёрнуто". SizeChanged — обычное высокоуровневое
// событие walk (см. WindowBase.SizeChanged в vendor lxn/walk), оно
// публикуется на любой WM_SIZE без различения SIZE_MINIMIZED/
// SIZE_RESTORED — сырой wParam нам и не нужен, разворачивание видно и
// так, по самому факту "было 0×0, стало не 0×0".
func (m *MainWindow) onMainWindowSizeChanged() {
	bounds := m.window.ClientBounds()
	minimized := bounds.Width == 0 && bounds.Height == 0

	if !minimized && m.wasMinimized {
		m.redrawClientEdges()
	}
	m.wasMinimized = minimized
}

// onMouseWheel — единственная причина этого метода: Windows шлёт
// WM_MOUSEWHEEL окну с ФОКУСОМ ВВОДА, а не тому, что под курсором (см.
// MSDN, раздел Remarks у WM_MOUSEWHEEL — так исторически сложилось ещё
// с 16-битных Windows). В этом приложении фокус почти всё время держит
// поле ввода сообщения, а не chatView, — так что колесо мыши над
// историей чата у chatView.onMouseWheel (см. chatview.go) само по себе
// ни разу не сработает: событие достаётся полю ввода, которое его не
// обрабатывает.
//
// Дальше начинает работать другая часть того же механизма: любое окно,
// не обработавшее WM_MOUSEWHEEL, само передаёт его своему родителю
// через DefWindowProc — так оно поднимается по цепочке родителей (поле
// ввода → Composite-контейнеры → MainWindow), пока не найдётся то, что
// его заберёт. MainWindow — вершина этой цепочки в нашем окне, сюда
// сообщение долетает нетронутым, и здесь остаётся вручную решить,
// относится ли оно к chatView (курсор сейчас над ним) — единственный
// скроллящийся колесом виджет во всём приложении, второго такого места
// пока нет и заводить общий "роутер" под это преждевременно.
func (m *MainWindow) onMouseWheel(x, y int, button walk.MouseButton) {
	view := m.chatPane.view
	if view.widget == nil {
		return // страница чата ещё не построена — сворачивать нечего
	}

	// x/y у WM_MOUSEWHEEL — координаты ЭКРАНА, а не клиентской области,
	// в отличие от остальных мышиных сообщений (та же причина: раз
	// сообщение в принципе может уйти в другое окно, координаты не
	// имеют смысла быть относительными к какому-то одному из них) — see
	// MSDN. Поэтому сравниваем прямо с GetWindowRect, без
	// ScreenToClient.
	var rect win.RECT
	if !win.GetWindowRect(view.widget.Handle(), &rect) {
		return
	}
	if x < int(rect.Left) || x >= int(rect.Right) || y < int(rect.Top) || y >= int(rect.Bottom) {
		return // курсор не над chatView — это не наше колесо
	}

	view.onMouseWheel(x, y, button)
}

// redrawClientEdges — тот же самый вызов SetWindowPos с
// SWP_FRAMECHANGED, что уже используют nativeClientEdge (sidebar.go) и
// attach() у chatView (chatview.go) для ПЕРВОНАЧАЛЬНОГО появления
// рамки: без реального изменения геометрии (SWP_NOMOVE|SWP_NOSIZE)
// заставляет Windows пересчитать и перерисовать именно нерабочую
// область конкретного hwnd (WM_NCCALCSIZE/WM_NCPAINT), не трогая
// обычную клиентскую отрисовку (WM_PAINT) — та и так работает
// исправно, пропадает только рамка.
//
// Каждый виджет проверяется на nil отдельно: до входа (или пока не
// показана страница чата) часть из них ещё не создана — см.
// applySignIn/chatpage.go.
func (m *MainWindow) redrawClientEdges() {
	const swpFrameOnly = win.SWP_NOMOVE | win.SWP_NOSIZE | win.SWP_NOZORDER | win.SWP_FRAMECHANGED

	if m.chatPane.input != nil {
		win.SetWindowPos(m.chatPane.input.Handle(), 0, 0, 0, 0, 0, swpFrameOnly)
	}
	if m.sidebar.view != nil {
		win.SetWindowPos(m.sidebar.view.Handle(), 0, 0, 0, 0, 0, swpFrameOnly)
	}
	if m.chatPane.view.widget != nil {
		win.SetWindowPos(m.chatPane.view.widget.Handle(), 0, 0, 0, 0, 0, swpFrameOnly)
	}
}

// EnsureSignedIn пытается войти: сначала тихо по сохранённому с
// прошлого раза токену (чтобы вернувшемуся пользователю не пришлось
// лишний раз жать "Войти"), а если это не удалось — показывает
// страницу входа вместо страницы чата (см. pagehost.go). Вызывать
// ровно один раз, отдельной горутиной, и только ПОСЛЕ того как
// вызывающий код присвоил результат New() в свою переменную — сама New
// эту горутину не запускает, чтобы не читать ещё не записанный (в
// терминах видимости между горутинами) указатель на самих себя. Можно
// вызывать до Run(): m.window.Synchronize ставит колбэк в очередь
// сообщений окна и выполнит его сразу же, как только запустится цикл
// обработки.
//
// Без сессии работать нельзя — если построить ни страницу входа, ни
// страницу чата не удалось (само по себе крайне маловероятно —
// единственный реалистичный случай это баг в дереве виджетов, а не
// внешние обстоятельства), показывать пустое окно бессмысленно,
// приложение закрывается.
func (m *MainWindow) EnsureSignedIn() {
	result, err := m.signIn(nil)
	if err == nil {
		m.window.Synchronize(func() {
			m.applySignIn(result)
		})
		return
	}

	log.Println("тихий вход не удался:", err)

	m.window.Synchronize(func() {
		m.setStatus("Не авторизован")

		if err := m.pages.show(buildSignInPage(m.signIn, m.applySignIn)); err != nil {
			log.Println("построить страницу входа:", err)
			m.window.Close()
		}
	})
}

// applySignIn переключает на страницу чата и подключает к ней
// результат входа. Строит страницу чата заново при каждом вызове —
// на практике вызывается ровно один раз за сессию (либо сразу после
// тихого входа, либо один раз из signInPage), но это не предположение,
// на которое опирается код: pageHost.show просто идемпотентно заменяет
// текущую страницу, какая бы она ни была.
func (m *MainWindow) applySignIn(result SignInResult) {
	if err := m.pages.show(buildChatPage(m)); err != nil {
		log.Println("построить страницу чата:", err)
		m.window.Close()
		return
	}

	m.sidebar.setWorkspace(result.Workspace, result.Resolve)
	m.chatPane.setWorkspace(result.Workspace)
	m.chatPane.setViewer(result.Viewer)
	m.chatPane.setBadgeCatalog(result.Badges)
	m.chatPane.setGlobalBadgeCatalog(result.GlobalBadges)

	m.setStatus("")
	m.sidebar.reload()
}

// NotifyChannelClosed сообщает пользователю, что чат закрылся не по
// его инициативе — отзыв подписки Twitch'ом или провал пересоздания
// подписки после реконнекта (composition root подключает это к
// eventsub.Hub.OnRevoked/OnResubscribeFailed). Модальный диалог тут
// оправдан (в отличие от обычных ошибок через logAndShowError) — это
// не транзитная неполадка на фоне текущего действия пользователя, а
// событие, которое стоит явно ему показать, даже если он сейчас
// смотрит в другой чат. Безопасен для вызова из любой горутины.
func (m *MainWindow) NotifyChannelClosed(channel domain.Channel, reason string) {
	m.window.Synchronize(func() {
		log.Printf("канал %s закрыт: %s", channel.Name, reason)
		walk.MsgBox(m.window, "Чат закрыт",
			fmt.Sprintf("%s: %s\r\n\r\nОткрыть заново можно через поле слева.", channel.DisplayName, reason),
			walk.MsgBoxIconWarning)
		m.sidebar.reload()
	})
}

// logAndShowError — стандартный путь для рядовых ошибок выполнения
// (не хватило сети на отправку, канал не нашёлся и т.п.): пишем в лог
// (composition root настраивает его вывод в файл — окно
// GUI-подсистемы консоли не имеет) и коротко показываем в статус-баре.
// Не модальный — не должен прерывать то, чем занят пользователь, ради
// одной неудавшейся операции. Часть интерфейса statusReporter — этим
// же путём идут ошибки из sidebar и chatPane.
func (m *MainWindow) logAndShowError(err error) {
	log.Println(err)
	m.setStatus(err.Error())
}

func (m *MainWindow) setStatus(text string) {
	_ = m.statusItem.SetText(text)
}

// SetConnectionStatus обновляет статус соединения EventSub в
// статус-баре (см. Hub.OnConnected/OnDisconnected — вешаются в main.go,
// сам Hub про UI не знает). Вызывается из фоновых горутин Hub, поэтому
// Synchronize — тот же приём, что и везде при обращении к виджетам не
// из UI-потока.
func (m *MainWindow) SetConnectionStatus(connected bool) {
	m.window.Synchronize(func() {
		text := "Переподключение..."
		if connected {
			text = "Подключено"
		}
		_ = m.connectionStatusItem.SetText(text)
	})
}
