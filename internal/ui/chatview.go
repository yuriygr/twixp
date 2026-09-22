//go:build windows
// +build windows

package ui

import (
	"fmt"
	"syscall"
	"time"
	"unsafe"

	"github.com/lxn/walk"
	"github.com/lxn/win"

	"twixp/internal/domain"
)

// chatLine — одна строка истории в чистом, не привязанном к domain
// виде: только то, что нужно для отрисовки и для действий контекстного
// меню (Ответить/Скопировать). chatView ничего не знает про
// domain.ChatMessage напрямую — цвет ника, текст времени и признак
// упоминания уже посчитаны на стороне chatPane (см. domain.NicknameColor,
// domain.IsMentioned). Badges — исключение: сами domain.Badge (Name/Version)
// долетают как есть, отрисовку конкретной иконки chatView делает через
// resolveBadge, а не готовым *walk.Bitmap в самой строке — так уже
// показанные строки подхватывают иконку, догрузившуюся позже (см.
// chatPane.resolveBadge/ensureBadgeIcon).
type chatLine struct {
	Time      time.Time
	Author    string
	MessageID string // ID сообщения в Twitch — нужен для reply_parent_message_id
	Color     walk.Color
	Text      string
	// Badges — бейджи автора на момент отправки сообщения (модератор,
	// подписчик, VIP и т.п.), в том порядке, в каком их прислал Twitch.
	Badges []domain.Badge
	// ReplyTo — если сообщение является ответом, здесь автор и текст
	// родительского сообщения (см. domain.ReplyTo). Text уже без
	// ведущего "@Автор" — Twitch сам добавляет его в начало текста
	// любого ответа, а chatPane обрезает при построении chatLine (см.
	// stripReplyMentionPrefix), чтобы не дублировать то же самое имя
	// ещё и в отдельной серой строке сверху (см. layoutLine/drawLine).
	ReplyTo *domain.ReplyTo
	// Mentioned — упоминает ли сообщение текущего пользователя
	// ("@логин"/"@ОтображаемоеИмя" в тексте, см. isMentioned в
	// chatpane.go). Красится едва красноватым фоном (см. mentionBgColor)
	// с чуть более красной рамкой слева (см. mentionBorderColor)
	Mentioned bool
	// Highlighted — сообщение оплачено баллами канала для подсветки
	// ("Highlight My Message", см. domain.ChatMessage.Highlighted).
	// Рисуется фиолетовой полосой слева (см. highlightBorderColor) и
	// строкой "Redeemed Highlight My Message" сверху — в том же месте
	// раскладки, что и баннер ответа (см. lineLayout.reply), только
	// вместо него: сообщение не может одновременно показывать оба
	// баннера (см. bannerText в chatview.go).
	Highlighted bool
	// SystemMessage — если не пусто, вся строка — не обычное
	// сообщение, а системное уведомление о событии в чате (подписка,
	// рейд и т.п. — см. domain.ChatMessage.SystemMessage). В этом
	// случае Author/Badges/ReplyTo/Highlighted не используются: вся
	// строка — просто SystemMessage серым курсивом (см. drawLine).
	SystemMessage string
	// Deleted — сообщение удалено модератором/ботом (EventSub
	// channel.chat.message_delete, см. chatPane.deleteMessage). Текст
	// остаётся на месте (в отличие от веба Twitch, ничего не прячем),
	// просто красится серым — и ник, и само сообщение (см. drawLine).
	// Бейджи/рамки/подсветка при этом не убираются: раз удаление не
	// меняет то, чем сообщение было, отменять уже нарисованные
	// признаки (упоминание, выделение баллами) было бы лишней работой
	// ради сомнительной пользы.
	Deleted bool
}

var (
	timestampColor   = walk.RGB(128, 128, 128)
	defaultTextColor = walk.RGB(0, 0, 0)
	// mentionBgColor — едва красноватый фон строки с упоминанием
	// текущего пользователя (см. chatLine.Mentioned). Специально бледный
	// — это фон под ЧЁРНЫМ текстом (defaultTextColor выше), а не акцент
	// поверх него, различимость текста важнее яркости подсветки.
	mentionBgColor = walk.RGB(255, 232, 232)
	// mentionBorderColor - чуть более заметный красный для полосы слева.
	// Постарался подобрать сочетание цветов как в вебе.
	mentionBorderColor = walk.RGB(255, 125, 125)
	// highlightBorderColor — цвет полосы слева у оплаченных баллами
	// сообщений (см. chatLine.Highlighted). Тот же лиловый оттенок,
	// каким Twitch выделяет "Highlight My Message" в вебе.
	highlightBorderColor = walk.RGB(145, 71, 255)
	// systemMessageColor — цвет текста системных уведомлений (см.
	// chatLine.SystemMessage). Тот же серый, что и у времени — то же
	// самое "это не реплика собеседника" ощущение, что и у timestampColor,
	// только тут в курсиве и в полный размер шрифта (см. drawLine).
	systemMessageColor = timestampColor
	// unreadIndicatorColor/unreadIndicatorTextColor — плашка "↓ N новых
	// сообщений" (см. chatView.unreadCount). Синий акцент — просто
	// уведомление, не пересекается по смыслу ни с упоминанием
	// (красноватый mentionBgColor/mentionBorderColor), ни с выделением
	// баллами (лиловый highlightBorderColor).
	unreadIndicatorColor     = walk.RGB(60, 120, 220)
	unreadIndicatorTextColor = walk.RGB(255, 255, 255)
)

// chatPad* — внутренние отступы. chatLineGap — вертикальный зазор
// между сообщениями. approxLineHeight — грубая (не обязанная быть
// точной для пиксель-в-пиксель скролла) оценка высоты одной строки
// текста, используется и колесом мыши, и стрелками нативного
// скроллбара (см. onMouseWheel/onVScroll) — единственное место, где
// это число задано, чтобы оба способа скроллить "на одно и то же"
// ощущались одинаково.
const (
	chatPadX         = 4
	chatPadY         = 2
	chatLineGap      = 2
	approxLineHeight = 16
	// badgeIconSize — сторона квадрата под иконку бейджа. Helix отдаёт
	// в image_url_1x картинку 18×18 (см. helix.Client.GlobalBadges,
	// ChannelBadges) — рисуем её МЕНЬШЕ этого номинала (DrawImageStretched
	// сжимает), подогнав под типичную высоту строки чата: 18px в паре с
	// обычным 8-10pt шрифтом выглядел непропорционально крупным и тянул
	// на себя всю строку (см. mainHeight ниже), а не наоборот.
	badgeIconSize = 14
	// badgeGap — зазор между соседними бейджами одного сообщения.
	badgeGap = 2
	// replyLineGap — вертикальный зазор между серой строкой "Ответ
	// Автору: ..." и самим сообщением под ней (см. chatLine.ReplyTo).
	replyLineGap = 1
	// borderWidth — ширина полосы слева у сообщений,
	// оплаченных баллами канала или при упоминании.
	borderWidth = 2
)

// chatView — кастомный (не из lxn/walk) виджет чата поверх
// walk.CustomWidget: обычный win32 EDIT/RichEdit тут не годится —
// первый вообще не умеет разноцветный текст, второго в этой версии
// walk нет и его пришлось бы заворачивать самим. Рисуем сами: время
// серым, ник — его цветом, текст — обычным, с переносом по словам
// через штатный DrawTextEx(DT_WORDBREAK) — свой алгоритм переноса не
// пишем, GDI справляется сам.
//
// Скролл — колесо мыши плюс нативный вертикальный скроллбар (WS_VSCROLL,
// см. Style у declarative.CustomWidget в mainwindow.go). Обычный win32
// EDIT/RichEdit давал бы то и другое бесплатно, но версия walk без
// RichEdit не даёт готового способа встроить прокручиваемый контент
// переменной высоты в нативный скроллбар декларативно — сам walk
// ничего не знает про WM_VSCROLL для CustomWidget. Получаем его
// win32-сабклассингом оконной процедуры (см. subclassWndProc) — тем же
// приёмом, которым сам lxn/walk сабклассит внутренние SysListView32 у
// TableView (см. vendor lxn/walk, tableview.go). Разметка и paint не
// меняются вообще: scrollTop как был единственным источником правды о
// прокрутке, так и остался — просто у него теперь два способа
// поменяться, а не один. Ещё сознательное ограничение: нет
// выделения/копирования текста (то, что бесплатно давал бы TextEdit) —
// если окажется важно на практике, отдельная задача.
//
// onMouseWheel ниже сам по себе срабатывает только пока фокус ввода на
// самом chatView — вживую это почти никогда не так (обычно печатают в
// поле сообщения), а Windows шлёт WM_MOUSEWHEEL именно окну с фокусом,
// а не тому, что под курсором (см. MSDN, WM_MOUSEWHEEL). Настоящая
// точка входа для колеса мыши — MainWindow.onMouseWheel (mainwindow.go):
// туда сообщение попадает, поднявшись по цепочке родителей через
// DefWindowProc, и оттуда явно перевызывается chatView.onMouseWheel,
// если курсор сейчас над ним.
type chatView struct {
	widget *walk.CustomWidget

	// origWndProc — оконная процедура, которую вернул
	// walk/CreateWindowEx до сабклассинга (см. attach). Нужна, чтобы
	// отдавать ей все сообщения, кроме WM_VSCROLL, через CallWindowProc
	// — иначе виджет молча потерял бы всё остальное поведение
	// walk.CustomWidget (отрисовку, мышь и т.д.).
	origWndProc uintptr

	// bgBrush — кисть фона, кэшированная один раз в attach() и
	// переиспользуемая в paint() (там же и заливается — см. начало
	// paint()) — не создаём GDI-объект заново на каждую перерисовку,
	// это на слабом железе (Celeron ULV ~630МГц) не бесплатно, особенно
	// на каждое новое сообщение в активном чате.
	bgBrush walk.Brush

	// mentionBrush — кисть для подсветки строк с упоминанием
	// (chatLine.Mentioned), кэшированная там же и по той же причине, что
	// и bgBrush. nil, если создать кисть не удалось (см. attach) — paint
	// в этом случае просто не подсвечивает такие строки, не роняя
	// отрисовку остального.
	mentionBrush walk.Brush

	// highlightBorderBrush — кисть бледно-красной полосы слева у сообщений,
	// в которых упоминается пользователь.
	mentionBorderBrush walk.Brush

	// highlightBorderBrush — кисть лиловой полосы слева у сообщений,
	// оплаченных баллами канала (см. chatLine.Highlighted).
	highlightBorderBrush walk.Brush

	// unreadBrush — кисть фона плашки "↓ N новых сообщений" (см.
	// unreadCount), тот же приём кэширования, что и у остальных кистей.
	unreadBrush walk.Brush
	// unreadCount — сколько новых сообщений пришло, пока пользователь
	// был прокручен вверх (см. appendLine) — 0 значит "плашка не
	// нужна". Проверка "не пора ли сбросить" живёт в paint(), а не в
	// каждом отдельном месте, где меняется scrollTop (колесо,
	// скроллбар, клик по самой плашке) — в этом виджете КАЖДОЕ такое
	// место и так уже безусловно вызывает widget.Invalidate() (полный
	// перерисовщик, не частичный — тут нигде не используется
	// ScrollWindow/партиальный блиттинг), так что updateBounds в
	// paint() на практике всегда покрывает весь клиентский
	// прямоугольник, и сброс именно там ничего не теряет.
	unreadCount int
	// unreadIndicatorRect — прямоугольник плашки в локальных
	// координатах ВИДЖЕТА (не прокручиваемого содержимого — плашка
	// висит поверх видимой области, а не двигается вместе с scrollTop).
	// Обновляется в drawUnreadIndicator при каждой отрисовке, читается
	// в onMouseDown для хит-теста по клику. Нулевой прямоугольник,
	// если плашка сейчас не показана.
	unreadIndicatorRect walk.Rectangle

	lines       []chatLine
	layouts     []lineLayout // геометрия каждого сообщения при текущей ширине — считается один раз (recomputeLayout/appendLine), paint только читает
	totalHeight int

	// scrollTop — сколько пикселей контента прокручено выше видимой
	// области. 0 — самый верх истории.
	scrollTop int

	// contextLine/hasContext — какое сообщение было под курсором в
	// момент последнего правого клика (см. onMouseDown). Читается
	// действиями контекстного меню (см. attach) в момент клика по
	// пункту меню — момент показа меню и момент выбора пункта в нём
	// разнесены во времени, поэтому это состояние, а не параметр.
	contextLine chatLine
	hasContext  bool

	// onReply вызывается по клику "Ответить" в контекстном меню
	// сообщения. Сам chatView не знает, что значит "ответить" — это
	// решает chatPane (переключает состояние поля ввода).
	onReply func(chatLine)

	// resolveBadge отдаёт иконку бейджа для отрисовки, если она уже
	// скачана (см. chatPane.resolveBadge) — сама загрузка, если нужна,
	// вне ответственности chatView, только эта отдача готового
	// результата, безопасная для вызова прямо из paint. nil-результат —
	// нормальный случай (иконка ещё грузится или бейджа нет в каталоге
	// канала), просто пропускаем это место в строке (см. drawLine).
	resolveBadge func(domain.Badge) *walk.Bitmap

	// replyFont — уменьшенный (на 1pt меньше основного) вариант шрифта
	// виджета для серой строки "Ответ Автору: ..." над сообщением-ответом
	// (см. chatLine.ReplyTo). Создаётся один раз в attach(), когда уже
	// известен основной шрифт виджета — тем же приёмом кэширования, что
	// и bgBrush/mentionBrush. nil, если создать не удалось (например,
	// основной шрифт совсем мелкий и pointSize-1 не проходит) — тогда
	// drawLine просто не показывает эту строку, не роняя остальное.
	replyFont *walk.Font

	// systemFont — тот же размер, что основной шрифт виджета, но
	// курсивом (walk.FontItalic) — для строк системных уведомлений
	// (см. chatLine.SystemMessage). В отличие от replyFont размер не
	// уменьшается: просили "тем же размером шрифта, что и сообщение".
	// Пересоздаётся вместе с replyFont — и в attach() (первое
	// создание), и в setFontSize (при смене размера в настройках), см.
	// updateReplyFont/updateSystemFont.
	systemFont *walk.Font

	// nickFont — тот же размер, что основной шрифт виджета, но
	// полужирным (walk.FontBold) — для ника автора сообщения (см.
	// drawLine). Раскладка (layoutLine) меряет ширину ника ЭТИМ ЖЕ
	// шрифтом, а не основным: полужирный текст обычно чуть шире
	// обычного, и если мерить одним шрифтом, а рисовать другим, текст
	// сообщения мог бы слегка наехать на ник. Пересоздаётся вместе с
	// replyFont/systemFont — см. updateNickFont.
	nickFont *walk.Font

	// showTimestamps/showBadges/highlightMentions — настраиваемые
	// переключатели видимости (см. domain.Settings, settingsdialog.go).
	// Значения по умолчанию (newChatView) — true, то же самое, что и
	// domain.DefaultSettings(): до применения настроек (до входа, пока
	// applySettings ещё не вызван из mainwindow.go) чат выглядит ровно
	// так же, как выглядел всегда, а не с каких-то произвольных
	// нулевых значений.
	showTimestamps    bool
	showBadges        bool
	highlightMentions bool

	// fontSize — размер шрифта чата в pt из настроек (см.
	// domain.Settings.FontSize), 0 — системный по умолчанию. Поле, а
	// не только параметр setFontSize: applySettings может вызвать
	// setFontSize ДО того, как появится сам widget (см. MainWindow.New,
	// до входа) — тогда сохраняем значение здесь и применяем позже, в
	// attach(), когда widget уже есть (см. applyFontSize).
	fontSize int
}

func newChatView(onReply func(chatLine), resolveBadge func(domain.Badge) *walk.Bitmap) *chatView {
	return &chatView{
		onReply:      onReply,
		resolveBadge: resolveBadge,
		// Значения по умолчанию — как будто настроек ещё не подвезли
		// (см. комментарий у полей): applySettings перезапишет их
		// актуальными сразу после входа, но до этого момента (первая
		// отрисовка страницы чата) чат не должен выглядеть куце.
		showTimestamps:    true,
		showBadges:        true,
		highlightMentions: true,
	}
}

// attach довключает поведение chatView уже после того, как
// declarative-дерево в build() создало и присвоило v.widget через
// AssignTo (тот же порядок, что у sidebar.window/chatPane.window) —
// раньше построения самого окна вешать обработчики и контекстное меню
// не на что.
func (v *chatView) attach() {
	widget := v.widget

	if bg, err := walk.NewSystemColorBrush(walk.SysColorWindow); err == nil {
		widget.SetBackground(bg)
		v.bgBrush = bg
	}

	if brush, err := walk.NewSolidColorBrush(mentionBgColor); err == nil {
		v.mentionBrush = brush
	}

	if brush, err := walk.NewSolidColorBrush(mentionBorderColor); err == nil {
		v.mentionBorderBrush = brush
	}

	if brush, err := walk.NewSolidColorBrush(highlightBorderColor); err == nil {
		v.highlightBorderBrush = brush
	}

	if brush, err := walk.NewSolidColorBrush(unreadIndicatorColor); err == nil {
		v.unreadBrush = brush
	}

	// pointSize()-1 — минимум 6pt, чтобы на совсем мелких основных
	// шрифтах строка "Ответ Автору: ..." не пыталась стать нечитаемой
	// или отрицательного размера (NewFont с pointSize <= 0 просто
	// вернёт ошибку — но лучше не полагаться на это, а не отправлять
	// туда заведомо абсурдное значение).
	//
	// Если fontSize уже был выставлен настройками ДО этого момента (см.
	// setFontSize — applySettings вызывает его ещё в MainWindow.New, до
	// входа, когда widget'а ещё не было), применяем его сейчас, и он
	// же по цепочке пересоздаст replyFont/systemFont под новый базовый
	// шрифт — отдельный вызов updateReplyFont/updateSystemFont в этом
	// случае не нужен.
	if v.fontSize > 0 {
		v.applyFontSize()
	} else {
		v.updateReplyFont(widget.Font())
		v.updateSystemFont(widget.Font())
		v.updateNickFont(widget.Font())
	}

	// Нативная утопленная 3D-окантовка (WS_EX_CLIENTEDGE) — та же, что
	// у LineEdit/TreeView/TextEdit по умолчанию (см. её же для
	// TableView — nativeClientEdge в sidebar.go, там же подробное
	// объяснение зачем). declarative.CustomWidget/walk.NewCustomWidget
	// не дают способа задать расширенный стиль при создании (см.
	// customwidget.go в vendor lxn/walk — там жёстко 0), поэтому это
	// доступно только постфактум через win32.
	//
	// SWP_FRAMECHANGED пересчитывает клиентскую область под новую
	// рамку (она "съедает" пару пикселей по краю) — recomputeLayout
	// ниже пересчитывает раскладку строк под эту новую (чуть меньшую)
	// ширину сразу, не дожидаясь случайного WM_SIZE.
	exStyle := win.GetWindowLong(widget.Handle(), win.GWL_EXSTYLE)
	exStyle |= win.WS_EX_CLIENTEDGE
	win.SetWindowLong(widget.Handle(), win.GWL_EXSTYLE, exStyle)
	win.SetWindowPos(widget.Handle(), 0, 0, 0, 0, 0,
		win.SWP_NOMOVE|win.SWP_NOSIZE|win.SWP_NOZORDER|win.SWP_FRAMECHANGED)
	v.recomputeLayout()

	widget.SizeChanged().Attach(v.onResized)
	widget.MouseWheel().Attach(v.onMouseWheel)
	widget.MouseDown().Attach(v.onMouseDown)

	// Сабклассинг ради WM_VSCROLL — см. комментарий у subclassWndProc.
	// SetWindowLongPtr(GWLP_WNDPROC, ...) возвращает ПРЕДЫДУЩУЮ
	// процедуру напрямую (обычная семантика win32 для этого вызова, ей
	// же пользуется сам lxn/walk в tableview.go) — отдельный
	// GetWindowLongPtr до этого не нужен.
	v.origWndProc = win.SetWindowLongPtr(widget.Handle(), win.GWLP_WNDPROC,
		syscall.NewCallback(v.subclassWndProc))
	v.updateScrollBar()

	if menu, err := walk.NewMenu(); err == nil {
		reply := walk.NewAction()
		_ = reply.SetText("Ответить")
		reply.Triggered().Attach(func() {
			// SystemMessage != "" — это не реплика собеседника, а системное
			// уведомление (подписка, рейд и т.п., см. chatLine.SystemMessage)
			// — отвечать на него семантически бессмысленно, да и
			// MessageID уведомления Twitch как reply_parent_message_id
			// скорее всего просто отклонит.
			if v.hasContext && v.onReply != nil && v.contextLine.SystemMessage == "" {
				v.onReply(v.contextLine)
			}
		})
		_ = menu.Actions().Add(reply)

		copyMsg := walk.NewAction()
		_ = copyMsg.SetText("Скопировать сообщение")
		copyMsg.Triggered().Attach(func() {
			if !v.hasContext {
				return
			}
			// У системных уведомлений Text пуст (весь текст — в
			// SystemMessage, см. layoutSystemMessage/drawLine) — без
			// этого фолбэка "Скопировать" на такой строке копировало бы
			// пустую строку.
			text := v.contextLine.Text
			if text == "" {
				text = v.contextLine.SystemMessage
			}
			_ = walk.Clipboard().SetText(text)
		})
		_ = menu.Actions().Add(copyMsg)

		widget.SetContextMenu(menu)
	}
}

// onMouseDown разбирает клик по кнопке:
//   - левая — если попали в плашку "↓ N новых сообщений" (см.
//     unreadCount/drawUnreadIndicator), прокрутить в конец; иначе ничего;
//   - правая — запомнить, какое сообщение сейчас под курсором, ДО того
//     как Windows покажет контекстное меню (оно показывается по
//     отдельному, более позднему WM_CONTEXTMENU, который сам chatView
//     не обрабатывает — см. общий обработчик в WindowBase). Без этого
//     пункты меню всегда действовали бы на что-то одно и то же
//     (например, последнее добавленное сообщение), а не на то, по чему
//     реально кликнули.
func (v *chatView) onMouseDown(x, y int, button walk.MouseButton) {
	if button == walk.LeftButton {
		r := v.unreadIndicatorRect
		if v.unreadCount > 0 && x >= r.X && x < r.X+r.Width && y >= r.Y && y < r.Y+r.Height {
			v.scrollTop = v.maxScrollTop()
			v.updateScrollBar()
			v.widget.Invalidate() // безусловный полный — иначе сброс unreadCount может не попасть в updateBounds (см. комментарий у поля)
		}
		return
	}

	if button != walk.RightButton {
		return
	}

	line, ok := v.lineAt(y)
	v.contextLine = line
	v.hasContext = ok
}

// visualRow — видимая "строка" (блок одного сообщения) в текущих
// координатах прокрутки: индекс в v.lines/v.layouts и вертикальный
// диапазон [y, y+h) в локальных координатах виджета.
type visualRow struct {
	index int
	y, h  int
}

// forEachRow — единственное место, где живёт арифметика "накопленное
// Y от chatPadY-scrollTop по v.layouts". paint (отрисовка) и lineAt
// (хит-тест под правым кликом) идут через один и тот же проход —
// раньше это была одна и та же формула, скопированная в двух местах
// руками, и она могла разойтись при любой правке одного места без
// другого (типичный источник "меню не по той строке, что нарисована").
// fn возвращает false, чтобы прервать проход раньше конца.
func (v *chatView) forEachRow(fn func(row visualRow) bool) {
	y := chatPadY - v.scrollTop
	for i, layout := range v.layouts {
		h := layout.rowHeight()
		if !fn(visualRow{index: i, y: y, h: h}) {
			return
		}
		y += h
	}
}

// lineAt находит сообщение, чей отрисованный блок содержит точку y
// (в локальных координатах виджета).
func (v *chatView) lineAt(y int) (chatLine, bool) {
	var found chatLine
	ok := false

	v.forEachRow(func(row visualRow) bool {
		if y >= row.y && y < row.y+row.h {
			found = v.lines[row.index]
			ok = true
			return false
		}
		return true
	})

	return found, ok
}

// setLines заменяет содержимое целиком (переключение канала, обрезка
// истории при переполнении, полная очистка при forget) и сразу
// прокручивает к низу — так же, как раньше это делал showChannel для
// TextEdit.
// setLines заменяет всю историю целиком — используется и при обрезке
// истории (historyTrimBatch), и при обновлении уже показанных строк
// после того, как что-то долгрузилось (каталог бейджей, отдельная
// иконка), и при переключении на другой канал/его закрытии (см.
// showChannel/forget в chatpane.go) — у этих случаев разные ожидания
// от скролла, отсюда и stickToBottom:
//
//   - true — всегда прокрутить в конец после замены. Нужно там, где
//     новая история никак не связана с той позицией, что видел
//     пользователь до этого (переключение на другой канал — прежний
//     scrollTop остался от СОВСЕМ ДРУГОГО содержимого, сохранять его
//     было бы бессмысленно, а не просто неверно).
//   - false — "прилипание ко низу", тот же принцип, что и в
//     appendLine: если видимая область и так упиралась в конец истории
//     ДО замены, остаётся упираться и после; если пользователь
//     прокрутил вверх читать историю — позиция сохраняется. Раньше
//     функция ВСЕГДА прыгала в низ безусловно — при обрезке истории
//     это было терпимо (редкое событие), но после того, как появилась
//     догрузка бейджей/каталогов теми же путями, стало откровенно
//     раздражающим: чтение истории то и дело прерывалось прыжком вниз
//     на каждую догрузившуюся иконку в активном чате.
func (v *chatView) setLines(lines []chatLine, stickToBottom bool) {
	atBottom := stickToBottom || v.scrollTop >= v.maxScrollTop()

	v.lines = lines
	v.recomputeLayout()

	if atBottom {
		v.scrollTop = v.maxScrollTop()
	}
	v.clampScrollAndRedraw()
}

// appendLine добавляет одно сообщение без пересчёта всей истории —
// обычный путь на каждое новое сообщение. "Прилипание ко низу": если
// видимая область и так упиралась в конец истории, остаётся упираться
// и после добавления (пользователь видит новое сообщение); если
// пользователь прокрутил вверх читать историю — позиция не трогается.
func (v *chatView) appendLine(line chatLine) {
	atBottom := v.scrollTop >= v.maxScrollTop()

	var layout lineLayout
	if canvas, err := v.widget.CreateCanvas(); err == nil {
		layout, _ = v.layoutLine(canvas, v.widget.Font(), line, v.widget.ClientBounds().Width)
		canvas.Dispose()
	}

	v.lines = append(v.lines, line)
	v.layouts = append(v.layouts, layout)
	v.totalHeight += layout.rowHeight()

	if atBottom {
		v.scrollTop = v.maxScrollTop()
	} else {
		// Пользователь читает историю выше — не дёргаем его вниз (это
		// уже решено раньше, см. atBottom), но и не даём новому
		// сообщению пройти незамеченным: плашка "↓ N новых" (см.
		// unreadCount) сама сбросится, как только он долистает до конца
		// каким угодно способом (см. paint()).
		v.unreadCount++
	}

	v.updateScrollBar()
	v.widget.Invalidate()
}

func (v *chatView) maxScrollTop() int {
	max := v.totalHeight - v.widget.ClientBounds().Height
	if max < 0 {
		return 0
	}
	return max
}

// recomputeLayout пересчитывает геометрию КАЖДОЙ строки заново —
// нужно целиком при смене ширины виджета (перенос текста зависит от
// неё) и при полной замене содержимого (setLines). На появление
// одного нового сообщения (обычный случай) не нужен — см. appendLine.
func (v *chatView) recomputeLayout() {
	v.layouts = make([]lineLayout, len(v.lines))
	v.totalHeight = 0

	canvas, err := v.widget.CreateCanvas()
	if err != nil {
		return // редкость (виджет ещё не готов) — переживёт следующий Invalidate
	}
	defer canvas.Dispose()

	font := v.widget.Font()
	width := v.widget.ClientBounds().Width

	for i, line := range v.lines {
		layout, err := v.layoutLine(canvas, font, line, width)
		if err != nil {
			continue
		}
		v.layouts[i] = layout
		v.totalHeight += layout.rowHeight()
	}
}

func (v *chatView) onResized() {
	v.recomputeLayout()
	v.clampScrollAndRedraw()
}

// clampScrollAndRedraw — общий хвост после любого изменения, которое
// могло сдвинуть totalHeight (пересчёт раскладки при ресайзе, смене
// шрифта, включении/выключении времени или бейджей): подрезать
// scrollTop, если он вдруг вылез за новый maxScrollTop, обновить
// скроллбар и перерисовать. Раньше это было только в onResized —
// вынесено, чтобы setFontSize/setShowTimestamps/setShowBadges не
// дублировали те же четыре строки.
func (v *chatView) clampScrollAndRedraw() {
	if v.scrollTop > v.maxScrollTop() {
		v.scrollTop = v.maxScrollTop()
	}
	v.updateScrollBar()
	v.widget.Invalidate()
}

// updateReplyFont (пере)создаёт replyFont — уменьшенный (на 1pt меньше
// base) шрифт для серой строки "Ответ Автору: ..." (см. chatLine.ReplyTo).
// Вызывается и из attach() (первое создание), и из setFontSize (при
// смене размера шрифта в настройках — replyFont должен меняться вместе
// с основным, а не оставаться привязанным к тому размеру, что был при
// запуске).
func (v *chatView) updateReplyFont(base *walk.Font) {
	replySize := base.PointSize() - 1
	if replySize < 6 {
		replySize = 6
	}
	if font, err := walk.NewFont(base.Family(), replySize, base.Style()); err == nil {
		v.replyFont = font
	}
}

// updateSystemFont (пере)создаёт systemFont — курсивный вариант base
// ТОГО ЖЕ размера (в отличие от updateReplyFont, размер тут не
// уменьшается) — для строк системных уведомлений (см.
// chatLine.SystemMessage). Вызывается там же, где и updateReplyFont:
// из attach() (первое создание) и из applyFontSize (при смене размера
// шрифта в настройках).
func (v *chatView) updateSystemFont(base *walk.Font) {
	if font, err := walk.NewFont(base.Family(), base.PointSize(), base.Style()|walk.FontItalic); err == nil {
		v.systemFont = font
	}
}

// updateNickFont (пере)создаёт nickFont — полужирный вариант base
// того же размера (см. комментарий у поля nickFont). Та же логика
// вызова, что и у updateReplyFont/updateSystemFont: attach() (первое
// создание) и applyFontSize (при смене размера в настройках).
func (v *chatView) updateNickFont(base *walk.Font) {
	if font, err := walk.NewFont(base.Family(), base.PointSize(), base.Style()|walk.FontBold); err == nil {
		v.nickFont = font
	}
}

// setFontSize меняет размер шрифта чата (см. domain.Settings.FontSize).
// Безопасен для вызова ДО того, как появился сам widget (см.
// MainWindow.applySettings, вызывается из New() ещё до входа): в этом
// случае только запоминает значение в fontSize, а сама подгонка шрифта
// происходит позже, в attach() (см. applyFontSize).
func (v *chatView) setFontSize(pt int) {
	v.fontSize = pt
	if v.widget == nil {
		return
	}

	v.applyFontSize()
	v.recomputeLayout()
	v.clampScrollAndRedraw()
}

// applyFontSize — собственно смена шрифта widget'а и пересоздание
// replyFont/systemFont под него, по текущему значению v.fontSize. Общая
// часть setFontSize (когда меняют "на лету", уже после входа) и
// attach() (когда применяют то, что было настроено ДО того, как widget
// появился, — см. комментарий у fontSize). fontSize <= 0 — системный
// шрифт по умолчанию (тот же MS Shell Dlg 2, 8pt на большинстве систем
// — см. defaultFont в vendor lxn/walk).
func (v *chatView) applyFontSize() {
	pt := v.fontSize
	if pt <= 0 {
		pt = 8
	}

	base := v.widget.Font()
	font, err := walk.NewFont(base.Family(), pt, base.Style())
	if err != nil {
		return
	}

	v.widget.SetFont(font)
	v.updateReplyFont(font)
	v.updateSystemFont(font)
	v.updateNickFont(font)
}

// setShowTimestamps/setShowBadges/setHighlightMentions — переключатели
// из настроек (см. domain.Settings). Первые два меняют горизонтальную
// раскладку каждой строки (время и бейджи — часть layoutLine, а не
// что-то поверх неё), поэтому требуют recomputeLayout, а не только
// Invalidate; highlightMentions — только цвет заливки в paint, полную
// раскладку можно не трогать.
func (v *chatView) setShowTimestamps(show bool) {
	if v.showTimestamps == show {
		return
	}
	v.showTimestamps = show
	if v.widget == nil {
		return // виджета ещё нет (настройки применяются раньше входа) — перерисовывать нечего, поле уже верное
	}
	v.recomputeLayout()
	v.clampScrollAndRedraw()
}

func (v *chatView) setShowBadges(show bool) {
	if v.showBadges == show {
		return
	}
	v.showBadges = show
	if v.widget == nil {
		return
	}
	v.recomputeLayout()
	v.clampScrollAndRedraw()
}

func (v *chatView) setHighlightMentions(highlight bool) {
	if v.highlightMentions == highlight {
		return
	}
	v.highlightMentions = highlight
	if v.widget != nil {
		v.widget.Invalidate()
	}
}

// updateScrollBar синхронизирует нативный скроллбар (диапазон, размер
// страницы, позицию ползунка) с текущим состоянием chatView. Вызывается
// после ЛЮБОГО изменения, которое могло изменить totalHeight,
// ClientBounds().Height или scrollTop — иначе скроллбар начнёт
// показывать неправду (не тот размер ползунка/не ту позицию).
//
// SIF_DISABLENOSCROLL сознательно не выставлен: без него Windows сама
// прячет скроллбар, когда вся история помещается в видимую область
// (nPage перекрывает весь диапазон) — то поведение, которое нужно, без
// отдельного ShowScrollBar.
func (v *chatView) updateScrollBar() {
	var si win.SCROLLINFO
	si.CbSize = uint32(unsafe.Sizeof(si))
	si.FMask = win.SIF_RANGE | win.SIF_PAGE | win.SIF_POS

	if v.totalHeight > 0 {
		si.NMax = int32(v.totalHeight - 1)
	}
	if height := v.widget.ClientBounds().Height; height > 0 {
		si.NPage = uint32(height)
	}
	si.NPos = int32(v.scrollTop)

	win.SetScrollInfo(v.widget.Handle(), win.SB_VERT, &si, true)
}

// subclassWndProc перехватывает WM_VSCROLL нативного скроллбара —
// клики по стрелкам/треку и перетаскивание ползунка. walk.CustomWidget
// в этой версии ничего не делает с WM_VSCROLL и не публикует под него
// события, поэтому единственный способ его получить — классический
// win32-сабклассинг: подменить оконную процедуру именно этого hwnd на
// свою, всё остальное отдавая оригиналу через CallWindowProc. Тот же
// приём, которым сам lxn/walk сабклассит внутренние SysListView32 у
// TableView (см. vendor lxn/walk, tableview.go,
// tableViewNormalLVWndProc) — известный, а не придуманный с нуля
// способ.
//
// WM_ERASEBKGND тут больше не перехватывается отдельно — раньше это
// было нужно, чтобы решить проблему мерцания истории на каждое новое
// сообщение (Invalidate() всегда просит перерисовать весь клиентский
// прямоугольник целиком, и Windows перед WM_PAINT сама заливала бы его
// фоном отдельным более ранним сообщением, оставляя пустой кадр видимым
// до paint()). Теперь и это, и вторую половину той же проблемы (сам
// paint() рисовал прямо в экранный HDC без буфера — на медленном
// железе разрыв между стиранием фона и отрисовкой текста было видно
// даже без WM_ERASEBKGND) закрывает PaintMode: declarative.PaintBuffered
// у CustomWidget (см. page_chat.go) — offscreen-отрисовка с одним
// BitBlt в конце, и WM_ERASEBKGND в этом режиме гасит сам walk (см.
// customwidget.go: `if cw.paintMode != PaintNormal { return 1 }`).
// Раньше выставленный здесь `return 1` был бы просто более ранним
// дублем того же самого.
//
// Восстанавливать оригинальную процедуру на WM_NCDESTROY не нужно:
// chatView не пересоздаётся и не меняет hwnd в течение жизни
// приложения (в отличие от внутренних списков TableView, которые
// framework может пересобирать) — сабкласс живёт ровно до закрытия
// окна, вместе с самим процессом.
func (v *chatView) subclassWndProc(hwnd win.HWND, msg uint32, wParam, lParam uintptr) uintptr {
	if msg == win.WM_VSCROLL {
		v.onVScroll(wParam)
		return 0
	}

	return win.CallWindowProc(v.origWndProc, hwnd, msg, wParam, lParam)
}

// onVScroll — реакция на клик по стрелке/треку или перетаскивание
// ползунка скроллбара. SB_LINEUP/DOWN и PAGEUP/DOWN двигают scrollTop
// на фиксированный шаг, SB_THUMBTRACK/POSITION — на позицию ползунка.
func (v *chatView) onVScroll(wParam uintptr) {
	switch win.LOWORD(uint32(wParam)) {
	case win.SB_LINEUP:
		v.scrollTop -= approxLineHeight
	case win.SB_LINEDOWN:
		v.scrollTop += approxLineHeight
	case win.SB_PAGEUP:
		v.scrollTop -= v.widget.ClientBounds().Height
	case win.SB_PAGEDOWN:
		v.scrollTop += v.widget.ClientBounds().Height
	case win.SB_THUMBTRACK, win.SB_THUMBPOSITION:
		// HIWORD(wParam) как позиция ползунка — 16-битный и переполнится
		// на достаточно длинной истории чата (totalHeight в пикселях
		// легко превышает 65535 за долгую сессию). Правильный способ
		// узнать реальную позицию при перетаскивании — отдельный
		// GetScrollInfo с SIF_TRACKPOS, как и советует MSDN в разделе
		// Remarks про WM_VSCROLL.
		var si win.SCROLLINFO
		si.CbSize = uint32(unsafe.Sizeof(si))
		si.FMask = win.SIF_TRACKPOS
		if win.GetScrollInfo(v.widget.Handle(), win.SB_VERT, &si) {
			v.scrollTop = int(si.NTrackPos)
		}
	case win.SB_TOP:
		v.scrollTop = 0
	case win.SB_BOTTOM:
		v.scrollTop = v.maxScrollTop()
	default:
		return // SB_ENDSCROLL и прочее — позиция не меняется
	}

	if v.scrollTop < 0 {
		v.scrollTop = 0
	}
	if max := v.maxScrollTop(); v.scrollTop > max {
		v.scrollTop = max
	}

	v.updateScrollBar()
	v.widget.Invalidate()
}

// onMouseWheel сдвигает scrollTop на примерное число пикселей на
// "щелчок" колеса — 3 строки, как совпадает с системной настройкой
// Windows по умолчанию (SPI_GETWHEELSCROLLLINES), помноженные на
// грубую (не обязанную быть точной для пиксель-в-пиксель скролла)
// оценку высоты строки текста.
func (v *chatView) onMouseWheel(x, y int, button walk.MouseButton) {
	delta := int16(uint32(button) >> 16)

	const linesPerNotch = 3

	v.scrollTop -= int(delta) / 120 * linesPerNotch * approxLineHeight

	if v.scrollTop < 0 {
		v.scrollTop = 0
	}
	if max := v.maxScrollTop(); v.scrollTop > max {
		v.scrollTop = max
	}

	v.updateScrollBar()
	v.widget.Invalidate()
}

// lineLayout — рассчитанная геометрия одного сообщения при данной
// ширине: X и размеры кусков (время/бейджи/ник/текст), без Y — Y
// прибавляется отдельно и при измерении (recomputeLayout/appendLine),
// и при отрисовке (paint), поэтому сам расчёт колонок не размножен по
// двум местам.
type lineLayout struct {
	ts, nick, text walk.Rectangle
	// badges — по одному прямоугольнику на элемент line.Badges, в том
	// же порядке. Место резервируется всегда, даже если иконка ещё не
	// скачалась (resolveBadge вернёт nil) — раскладка не должна ёрзать
	// вправо-влево в момент, когда иконки одна за другой догружаются.
	badges []walk.Rectangle
	// reply — область под серую строку "Ответ Автору: ..." НАД основной
	// строкой сообщения (см. chatLine.ReplyTo). Нулевое значение
	// (Height == 0), если сообщение не является ответом — тогда drawLine
	// её просто не рисует, а mainY у остальных полей равен 0.
	//
	// Y у всех прямоугольников (ts/badges/nick/text/reply) — смещение
	// ОТНОСИТЕЛЬНО верха всего блока строки, а не абсолютная координата:
	// reply.Y всегда 0 (первая), у остальных — mainY (0, если ответа
	// нет, иначе reply.Height+replyLineGap). Отрисовка (drawLine)
	// прибавляет к этому Y абсолютную позицию блока в чате, а не
	// заменяет его — в отличие от старой версии, где Y для всех полей
	// был один и тот же, тут разным полям законно нужен разный Y.
	reply walk.Rectangle
	// userMessage — только для строк системных уведомлений (см.
	// chatLine.SystemMessage): текст, который сам подписчик добавил к
	// событию (комментарий к ресабу и т.п., см. domain.ChatMessage.Text
	// у decodeChatNotification) — отдельным абзацем НИЖЕ системного
	// текста, обычным (не курсивным) шрифтом и обычным цветом: это
	// слова реального человека, а не сгенерированная Twitch фраза,
	// стоит визуально отличать. Нулевое значение (Height == 0), если
	// добавленного текста нет — тогда drawLine его просто не рисует.
	userMessage walk.Rectangle
	height      int
}

// rowHeight — сколько вертикального места блок реально занимает в
// списке, вместе с зазором до следующего сообщения. Единственное
// место, добавляющее chatLineGap к height, — используется и при
// накоплении totalHeight (recomputeLayout/appendLine), и при проходе
// по строкам для отрисовки/хит-теста (forEachRow).
func (l lineLayout) rowHeight() int {
	return l.height + chatLineGap
}

func (v *chatView) layoutLine(canvas *walk.Canvas, font *walk.Font, line chatLine, width int) (lineLayout, error) {
	if line.SystemMessage != "" {
		return v.layoutSystemMessage(canvas, font, line, width)
	}

	// mainY — на сколько ниже верха блока начинается обычная строка
	// (время/бейджи/ник/текст). 0, если баннера нет (не ответ и не
	// выделенное сообщение) или replyFont почему-то не создался (см.
	// attach) — тогда баннер просто не показывается, а не съедает
	// место впустую.
	var reply walk.Rectangle
	mainY := 0
	if bannerText, ok := bannerTextFor(line); ok && v.replyFont != nil {
		replyWide := walk.Rectangle{Width: 10000, Height: 10000}
		replyM, _, err := canvas.MeasureText(bannerText, v.replyFont, replyWide, walk.TextSingleLine|walk.TextCalcRect)
		if err != nil {
			return lineLayout{}, err
		}

		replyWidth := width - chatPadX*2
		if replyWidth < 40 {
			replyWidth = 40
		}
		reply = walk.Rectangle{X: chatPadX, Width: replyWidth, Height: replyM.Height}
		mainY = reply.Height + replyLineGap
	}

	x := chatPadX

	// Ширина под 10000 — не предполагаем реального переноса внутри
	// времени/ника (они короткие и переносить их незачем), только
	// измеряем однострочную ширину, чтобы узнать, где начать текст.
	// TextCalcRect обязателен: без него walk.Canvas.MeasureText не
	// пересчитывает Rectangle.Width под реальный текст (win32
	// DrawTextEx без DT_CALCRECT не трогает переданный rect вообще —
	// в самом MeasureText это сознательный компромисс ради
	// uiLengthDrawn, см. комментарий в его исходнике) — без этого флага
	// назад пришла бы просто эхом та же ширина 10000, а не реальная.
	wide := walk.Rectangle{Width: 10000, Height: 10000}
	const singleLineMeasure = walk.TextSingleLine | walk.TextCalcRect

	tsText := line.Time.Local().Format("15:04:05")
	var ts walk.Rectangle
	if v.showTimestamps {
		tsM, _, err := canvas.MeasureText(tsText, font, wide, singleLineMeasure)
		if err != nil {
			return lineLayout{}, err
		}
		ts = walk.Rectangle{X: x, Y: mainY, Width: tsM.Width, Height: tsM.Height}
		x += tsM.Width + chatPadX
	}

	// Бейджи — фиксированные квадраты badgeIconSize, без измерения
	// текста: их размер не зависит ни от шрифта, ни от того, догрузилась
	// иконка уже или ещё нет (см. комментарий у lineLayout.badges).
	// Пустой (nil) слайс, если бейджи выключены в настройках — drawLine
	// проходит по layout.badges, а не по line.Badges, так что при
	// выключенной настройке просто нечего рисовать, без отдельной
	// проверки в двух местах.
	var badges []walk.Rectangle
	if v.showBadges && len(line.Badges) > 0 {
		badges = make([]walk.Rectangle, len(line.Badges))
		for i := range line.Badges {
			badges[i] = walk.Rectangle{X: x, Y: mainY, Width: badgeIconSize, Height: badgeIconSize}
			x += badgeIconSize + badgeGap
		}
	}

	// nickFont, если он есть — та же логика, что и у systemFont чуть
	// выше: раскладка обязана мерить ТЕМ ЖЕ шрифтом, каким потом рисует
	// drawLine, иначе полужирный ник может оказаться шире посчитанного
	// места (см. комментарий у поля nickFont).
	nickFont := font
	if v.nickFont != nil {
		nickFont = v.nickFont
	}

	nickText := line.Author + ":"
	nickM, _, err := canvas.MeasureText(nickText, nickFont, wide, singleLineMeasure)
	if err != nil {
		return lineLayout{}, err
	}
	nick := walk.Rectangle{X: x, Y: mainY, Width: nickM.Width, Height: nickM.Height}
	x += nickM.Width + chatPadX

	textWidth := width - x - chatPadX
	if textWidth < 40 {
		textWidth = 40 // совсем узкое окно — не даём тексту схлопнуться в ничто
	}
	textM, _, err := canvas.MeasureText(line.Text, font, walk.Rectangle{Width: textWidth, Height: 1 << 20}, walk.TextWordbreak)
	if err != nil {
		return lineLayout{}, err
	}
	text := walk.Rectangle{X: x, Y: mainY, Width: textWidth, Height: textM.Height}

	mainHeight := text.Height
	if ts.Height > mainHeight {
		mainHeight = ts.Height
	}
	if len(badges) > 0 && badgeIconSize > mainHeight {
		// Иконка выше строки текста (например, у совсем короткого
		// сообщения мелким шрифтом) — тогда высота строки определяется
		// бейджем, а не текстом.
		mainHeight = badgeIconSize
	}

	// Центрируем бейджи по вертикали относительно ПЕРВОЙ строки текста
	// (её высота — ts.Height, время всегда однострочное, тем же
	// шрифтом, что и текст), а НЕ относительно mainHeight: у
	// перенесённого на несколько строк сообщения mainHeight — высота
	// всего блока целиком (text.Height от MeasureText с TextWordbreak
	// включает все строки), и центровка по нему уводила бы бейдж вниз,
	// в середину всего сообщения, а не туда, где на самом деле ник —
	// в первую строку.
	lineHeight := ts.Height
	if nick.Height > lineHeight {
		lineHeight = nick.Height
	}
	badgeY := mainY + (lineHeight-badgeIconSize)/2
	if badgeY < mainY {
		// Бейдж выше самой строки текста — не задвигаем его ЕЩЁ выше
		// mainY ради центровки, которой всё равно не получится:
		// прижимаем к тому же верху, что и раньше.
		badgeY = mainY
	}
	for i := range badges {
		badges[i].Y = badgeY
	}

	return lineLayout{ts: ts, badges: badges, nick: nick, text: text, reply: reply, height: mainY + mainHeight}, nil
}

// bannerTextFor — текст серой строки НАД основной строкой сообщения
// (см. lineLayout.reply): либо "Использовано: Выделить моё сообщение" (см.
// chatLine.Highlighted), либо "Ответ Автору: ..." (см. chatLine.ReplyTo).
// Приоритет — Highlighted: оплаченное баллами выделение — более редкое
// и более "заметное" (платное) действие, чем обычный ответ, так что
// если оба почему-то совпали на одном сообщении, показываем именно его.
// ok=false — баннера нет вообще, mainY в layoutLine остаётся 0.
func bannerTextFor(line chatLine) (text string, ok bool) {
	if line.Highlighted {
		return "Использовано: Выделить моё сообщение", true
	}
	if line.ReplyTo != nil {
		return domain.ReplyLineText(line.ReplyTo), true
	}
	return "", false
}

// layoutSystemMessage — упрощённая раскладка для системных уведомлений
// (см. chatLine.SystemMessage): только время (если оно включено в
// настройках) и сам текст курсивом, без бейджей/ника/баннера сверху —
// это не реплика собеседника, а системная строка о событии в чате
// (подписка, рейд и т.п.), настоящего автора-собеседника у неё нет.
func (v *chatView) layoutSystemMessage(canvas *walk.Canvas, font *walk.Font, line chatLine, width int) (lineLayout, error) {
	x := chatPadX

	wide := walk.Rectangle{Width: 10000, Height: 10000}
	const singleLineMeasure = walk.TextSingleLine | walk.TextCalcRect

	var ts walk.Rectangle
	if v.showTimestamps {
		tsText := line.Time.Local().Format("15:04:05")
		tsM, _, err := canvas.MeasureText(tsText, font, wide, singleLineMeasure)
		if err != nil {
			return lineLayout{}, err
		}
		ts = walk.Rectangle{X: x, Width: tsM.Width, Height: tsM.Height}
		x += tsM.Width + chatPadX
	}

	// systemFont может быть nil, если NewFont в updateSystemFont
	// почему-то не удался (см. её комментарий) — тогда просто рисуем
	// обычным шрифтом, без курсива, а не роняем раскладку целиком.
	systemFont := font
	if v.systemFont != nil {
		systemFont = v.systemFont
	}

	textWidth := width - x - chatPadX
	if textWidth < 40 {
		textWidth = 40
	}
	textM, _, err := canvas.MeasureText(line.SystemMessage, systemFont, walk.Rectangle{Width: textWidth, Height: 1 << 20}, walk.TextWordbreak)
	if err != nil {
		return lineLayout{}, err
	}
	text := walk.Rectangle{X: x, Width: textWidth, Height: textM.Height}

	height := text.Height
	if ts.Height > height {
		height = ts.Height
	}

	// userMessage — то, что сам подписчик добавил к событию (комментарий
	// к ресабу и т.п., см. lineLayout.userMessage) — отдельным абзацем
	// ниже системного текста, обычным шрифтом. Пусто почти всегда (не
	// каждое уведомление это поддерживает и не каждый его заполняет).
	var userMessage walk.Rectangle
	if line.Text != "" {
		userM, _, err := canvas.MeasureText(line.Text, font, walk.Rectangle{Width: textWidth, Height: 1 << 20}, walk.TextWordbreak)
		if err != nil {
			return lineLayout{}, err
		}
		userMessage = walk.Rectangle{X: x, Y: height + chatPadY, Width: textWidth, Height: userM.Height}
		height = userMessage.Y + userMessage.Height
	}

	return lineLayout{ts: ts, text: text, userMessage: userMessage, height: height}, nil
}

// paint — единственный обработчик отрисовки. Геометрия строк уже
// посчитана заранее (recomputeLayout/appendLine) — здесь только
// читаем кэш и рисуем, не трогая GDI-измерение текста заново на
// каждый Invalidate (скролл колесом, новое сообщение и т.п.) — на
// слабом железе лишний проход MeasureText на каждую перерисовку того
// не стоит. updateBounds — уже ограниченная Windows-ом грязная
// область; строки полностью выше или полностью ниже неё не рисуем.
//
// FillRectangle в начале — заливка фона одним проходом вместе с самим
// текстом, а не отдельным более ранним WM_ERASEBKGND: PaintMode:
// declarative.PaintBuffered (см. page_chat.go) гасит WM_ERASEBKGND сам
// и рисует весь этот проход в offscreen-битмап, так что разрыв между
// стиранием и текстом в принципе не виден на экране — см. подробности
// у subclassWndProc.
func (v *chatView) paint(canvas *walk.Canvas, updateBounds walk.Rectangle) error {
	// Проверка "уже внизу?" — раньше отрисовки строк, а не после: не
	// важно, каким путём scrollTop туда попал (колесо, скроллбар, клик
	// по самой плашке, новое сообщение при уже нижней позиции) — как
	// только он там, плашка больше не нужна ни при каких обстоятельствах.
	if v.unreadCount > 0 && v.scrollTop >= v.maxScrollTop() {
		v.unreadCount = 0
	}

	if v.bgBrush != nil {
		if err := canvas.FillRectangle(v.bgBrush, updateBounds); err != nil {
			return err
		}
	}

	font := v.widget.Font()
	width := v.widget.ClientBounds().Width
	bottom := updateBounds.Y + updateBounds.Height

	var drawErr error
	v.forEachRow(func(row visualRow) bool {
		if row.y+row.h < updateBounds.Y {
			return true // выше грязной области — пропустить, но идти дальше
		}
		if row.y > bottom {
			return false // дальше только строки ниже видимой области
		}

		line := v.lines[row.index]
		layout := v.layouts[row.index]

		// Подсветка — во всю ширину строки, но только на высоту самого
		// сообщения (layout.height), не на весь row.h с chatLineGap: зазор
		// между сообщениями остаётся обычным фоном, иначе подсветка
		// соседних строк сливалась бы в один сплошной блок.
		if line.Mentioned && v.mentionBrush != nil && v.highlightMentions {
			bg := walk.Rectangle{X: 0, Y: row.y, Width: width, Height: layout.height}
			if err := canvas.FillRectangle(v.mentionBrush, bg); err != nil {
				drawErr = err
				return false
			}
		}

		if err := v.drawLine(canvas, font, line, layout, row.y); err != nil {
			drawErr = err
			return false
		}
		return true
	})

	if drawErr != nil {
		return drawErr
	}

	return v.drawUnreadIndicator(canvas, font)
}

// drawUnreadIndicator рисует плашку "↓ N новых сообщений" поверх
// видимой области, если она сейчас нужна (см. unreadCount), и
// запоминает её прямоугольник для хит-теста по клику (см. onMouseDown).
// Координаты — относительно viewport, а НЕ прокручиваемого содержимого
// (плашка не часть истории, а оверлей над ней) — считаются заново от
// ClientBounds на каждой отрисовке, а не от row.y/scrollTop.
func (v *chatView) drawUnreadIndicator(canvas *walk.Canvas, font *walk.Font) error {
	if v.unreadCount <= 0 || v.unreadBrush == nil {
		v.unreadIndicatorRect = walk.Rectangle{}
		return nil
	}

	text := fmt.Sprintf("↓ %d новых", v.unreadCount)

	wide := walk.Rectangle{Width: 10000, Height: 10000}
	textM, _, err := canvas.MeasureText(text, font, wide, walk.TextSingleLine|walk.TextCalcRect)
	if err != nil {
		return err
	}

	const paddingX = 10
	const paddingY = 5
	const bottomMargin = 10

	pillWidth := textM.Width + paddingX*2
	pillHeight := textM.Height + paddingY*2

	client := v.widget.ClientBounds()
	rect := walk.Rectangle{
		X:      (client.Width - pillWidth) / 2,
		Y:      client.Height - pillHeight - bottomMargin,
		Width:  pillWidth,
		Height: pillHeight,
	}
	v.unreadIndicatorRect = rect

	if err := canvas.FillRectangle(v.unreadBrush, rect); err != nil {
		return err
	}

	textRect := walk.Rectangle{X: rect.X + paddingX, Y: rect.Y + paddingY, Width: textM.Width, Height: textM.Height}
	return canvas.DrawText(text, font, unreadIndicatorTextColor, textRect, walk.TextSingleLine)
}

func (v *chatView) drawLine(canvas *walk.Canvas, font *walk.Font, line chatLine, layout lineLayout, y int) error {
	// r.Y += y, а не r.Y = y: Y в lineLayout — уже не абсолютная
	// координата, а смещение относительно верха блока (см. комментарий
	// у lineLayout.reply) — у reply он 0, у остального (ts/badges/
	// nick/text) — mainY, если у сообщения есть баннер сверху.
	at := func(r walk.Rectangle) walk.Rectangle {
		r.Y += y
		return r
	}

	if line.SystemMessage != "" {
		if v.showTimestamps {
			tsText := line.Time.Local().Format("15:04:05")
			if err := canvas.DrawText(tsText, font, timestampColor, at(layout.ts), walk.TextSingleLine); err != nil {
				return err
			}
		}

		systemFont := font
		if v.systemFont != nil {
			systemFont = v.systemFont
		}
		if err := canvas.DrawText(line.SystemMessage, systemFont, systemMessageColor, at(layout.text), walk.TextWordbreak); err != nil {
			return err
		}

		if line.Text == "" {
			return nil
		}
		// userMessage — то, что сам подписчик добавил к событию
		// (комментарий к ресабу и т.п.) — обычным шрифтом/цветом, в
		// отличие от курсивного серого системного текста выше (см.
		// lineLayout.userMessage).
		return canvas.DrawText(line.Text, font, defaultTextColor, at(layout.userMessage), walk.TextWordbreak)
	}

	if line.Mentioned && v.mentionBorderBrush != nil {
		border := walk.Rectangle{X: 0, Width: borderWidth, Height: layout.height}
		if err := canvas.FillRectangle(v.mentionBorderBrush, at(border)); err != nil {
			return err
		}
	}

	if line.Highlighted && v.highlightBorderBrush != nil {
		border := walk.Rectangle{X: 0, Width: borderWidth, Height: layout.height}
		if err := canvas.FillRectangle(v.highlightBorderBrush, at(border)); err != nil {
			return err
		}
	}

	if bannerText, ok := bannerTextFor(line); ok && v.replyFont != nil {
		if err := canvas.DrawText(bannerText, v.replyFont, timestampColor, at(layout.reply), walk.TextSingleLine); err != nil {
			return err
		}
	}

	if v.showTimestamps {
		tsText := line.Time.Local().Format("15:04:05")
		if err := canvas.DrawText(tsText, font, timestampColor, at(layout.ts), walk.TextSingleLine); err != nil {
			return err
		}
	}

	if v.resolveBadge != nil {
		// По layout.badges, а НЕ по line.Badges: при выключенных в
		// настройках бейджах (см. setShowBadges) layoutLine оставляет
		// layout.badges пустым независимо от того, сколько бейджей у
		// строки на самом деле — так что длины двух срезов совпадают,
		// только пока бейджи включены (см. комментарий у lineLayout.badges
		// и в layoutLine), и перебирать нужно именно ту сторону, что
		// заведомо не длиннее.
		for i := range layout.badges {
			badge := line.Badges[i]
			icon := v.resolveBadge(badge)
			if icon == nil {
				continue // ещё грузится или такого бейджа нет в каталоге — просто пропускаем место
			}
			if err := canvas.DrawImageStretched(icon, at(layout.badges[i])); err != nil {
				return err
			}
		}
	}

	nickText := line.Author + ":"
	nickColor := line.Color
	textColor := defaultTextColor
	if line.Deleted {
		// Удалено — красим и ник, и текст серым (см. chatLine.Deleted).
		// Тот же timestampColor, что и у времени/системных строк: то же
		// самое "неактуально, но пусть остаётся видно", а не выдумывать
		// третий оттенок серого специально под это.
		nickColor = timestampColor
		textColor = timestampColor
	}
	nickFont := font
	if v.nickFont != nil {
		nickFont = v.nickFont
	}
	if err := canvas.DrawText(nickText, nickFont, nickColor, at(layout.nick), walk.TextSingleLine); err != nil {
		return err
	}

	return canvas.DrawText(line.Text, font, textColor, at(layout.text), walk.TextWordbreak)
}
