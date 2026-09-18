//go:build windows
// +build windows

package ui

import (
	"syscall"
	"unsafe"

	"github.com/lxn/win"

	"twitchclient/internal/domain"
)

// mentionPopup — всплывающий список подсказок для "@упоминания" в поле
// ввода. Обычный системный LISTBOX (класс "LISTBOX" предопределён
// Windows, регистрировать не нужно), но в виде отдельного WS_POPUP-окна,
// а не walk.ListBox — walk не даёт способа сделать плавающее окно без
// декларативного родителя-контейнера (то же ограничение, из-за
// которого CustomWidget и TableView правятся напрямую через win32, см.
// nativeClientEdge в sidebar.go). Раз это настоящий системный список, а
// не что-то нарисованное вручную — внешний вид нативный для XP сам по
// себе, без единой строчки собственной отрисовки.
//
// Клавиатурный фокус всегда остаётся на LineEdit (см.
// chatPane.onInputTextChanged/onInputKeyDown) — сам попап никогда не
// активируется (ShowWindow с SW_SHOWNOACTIVATE), иначе набор текста
// прервался бы переключением фокуса. Навигация ↑/↓/Enter/Esc
// перехватывается в chatPane.onInputKeyDown и лишь ОТРАЖАЕТСЯ в списке
// через LB_SETCURSEL — самому попапу фокус для этого не нужен. Мышь же
// работает независимо от фокуса (обычное поведение Windows, не наша
// заслуга) — обрабатывается сабклассом WM_LBUTTONUP, тем же приёмом,
// что и WM_VSCROLL/WM_ERASEBKGND в chatview.go: у "осиротевшего"
// WS_POPUP-окна без обычного родителя нет гарантии, что
// WM_COMMAND/LBN_SELCHANGE штатно долетит туда же, откуда его удобно
// читать, — надёжнее забрать клик на себя напрямую.
type mentionPopup struct {
	hwnd     win.HWND
	origProc uintptr

	// items — те же самые варианты, что сейчас показаны в LISTBOX, в
	// том же порядке (индекс LISTBOX == индекс здесь). Нужны, чтобы по
	// индексу выбранной строки (LB_GETCURSEL/LB_ITEMFROMPOINT) отдать
	// вызывающему коду не текст, а domain.User целиком — включая Login,
	// который и подставляется в текст сообщения.
	items []domain.User
	// onCommit вызывается с выбранным пользователем — что делать с
	// выбором (вставить "@Логин " в текст, закрыть попап) решает
	// chatPane, сам попап значения имени не придаёт.
	onCommit func(domain.User)
}

// newMentionPopup создаёт (но не показывает) попап, привязанный к
// owner — окну, при сворачивании/закрытии которого попап должен вести
// себя так же (стандартное поведение WS_POPUP-окна с указанным
// hWndParent — это делает Windows сама, без нашего участия). Вызывать
// нужно один раз, когда уже есть настоящий hwnd главного окна (см.
// chatPane.attachMentionPopup/chatpage.go) — раньше этого момента
// owner ещё не существует.
func newMentionPopup(owner win.HWND) *mentionPopup {
	className, err := syscall.UTF16PtrFromString("LISTBOX")
	if err != nil {
		return &mentionPopup{}
	}

	hwnd := win.CreateWindowEx(
		win.WS_EX_TOOLWINDOW, // не отображается в панели задач/Alt+Tab — это подсказка, а не отдельное окно
		className,
		nil,
		win.WS_POPUP|win.WS_BORDER|win.WS_VSCROLL|win.LBS_NOTIFY|win.LBS_NOINTEGRALHEIGHT,
		0, 0, 0, 0,
		owner,
		0,
		win.GetModuleHandle(nil),
		nil,
	)

	p := &mentionPopup{hwnd: hwnd}
	if hwnd != 0 {
		p.origProc = win.SetWindowLongPtr(hwnd, win.GWLP_WNDPROC, syscall.NewCallback(p.wndProc))
	}
	return p
}

// show заполняет список пунктами items и раскрывает попап от верхнего
// края inputHwnd ВВЕРХ — в этом чате поле ввода стоит у самого низа
// окна, снизу под ним показывать некуда.
//
// items — уже отфильтрованный и отсортированный список (см.
// chatPane.matchChatters), сам попап никакой фильтрации не делает.
func (p *mentionPopup) show(inputHwnd win.HWND, items []domain.User, onCommit func(domain.User)) {
	if p.hwnd == 0 || len(items) == 0 {
		return
	}

	p.items = items
	p.onCommit = onCommit

	win.SendMessage(p.hwnd, win.LB_RESETCONTENT, 0, 0)
	for _, u := range items {
		text := u.DisplayName
		if text == "" {
			text = u.Login
		}
		ptr, err := syscall.UTF16PtrFromString(text)
		if err != nil {
			continue
		}
		win.SendMessage(p.hwnd, win.LB_ADDSTRING, 0, uintptr(unsafe.Pointer(ptr)))
	}
	// Первый пункт выделен сразу — так Enter без единого ↓ уже
	// вставляет верхний (обычно самый релевантный, см. matchChatters)
	// вариант, ровно как в вебе Twitch.
	win.SendMessage(p.hwnd, win.LB_SETCURSEL, 0, 0)

	var inputRect win.RECT
	win.GetWindowRect(inputHwnd, &inputRect)

	rowHeight := int32(win.SendMessage(p.hwnd, win.LB_GETITEMHEIGHT, 0, 0))
	if rowHeight <= 0 {
		rowHeight = 18 // разумное значение по умолчанию, если LB_GETITEMHEIGHT почему-то не сработал
	}

	const maxVisibleRows = 8
	visible := len(items)
	if visible > maxVisibleRows {
		visible = maxVisibleRows
	}

	const borderAllowance = 4 // рамка WS_BORDER сверху и снизу — без запаса нижняя строка обрезалась бы
	height := int32(visible)*rowHeight + borderAllowance
	width := inputRect.Right - inputRect.Left

	x := inputRect.Left
	y := inputRect.Top - height

	win.SetWindowPos(p.hwnd, win.HWND_TOPMOST, x, y, width, height, win.SWP_NOACTIVATE)
	win.ShowWindow(p.hwnd, win.SW_SHOWNOACTIVATE)
}

// hide прячет попап. Держать его созданным между показами дешевле, чем
// пересоздавать заново на каждое "@" (то же соображение, что и у
// закэшированных кистей/шрифтов в chatview.go) — окно живёт всё время
// работы приложения, отдельного Dispose для него, как и для прочих
// таких кэшей в проекте, не предусмотрено.
func (p *mentionPopup) hide() {
	if p.hwnd == 0 {
		return
	}
	win.ShowWindow(p.hwnd, win.SW_HIDE)
	p.items = nil
	p.onCommit = nil
}

func (p *mentionPopup) visible() bool {
	return p.hwnd != 0 && win.IsWindowVisible(p.hwnd)
}

// move двигает выделение в списке на delta пунктов (±1 для ↑/↓), с
// переносом с последнего пункта на первый и обратно. Само нажатие
// клавиши ловит chatPane.onInputKeyDown — фокус тут ни при чём (см.
// комментарий у mentionPopup), это только отражение в LISTBOX уже
// принятого решения.
func (p *mentionPopup) move(delta int) {
	if p.hwnd == 0 || len(p.items) == 0 {
		return
	}

	cur := int(int32(win.SendMessage(p.hwnd, win.LB_GETCURSEL, 0, 0)))
	next := (cur + delta + len(p.items)) % len(p.items)
	win.SendMessage(p.hwnd, win.LB_SETCURSEL, uintptr(next), 0)
}

// commitSelected вставляет текущий выделенный пункт — вызывается по
// Enter из chatPane.onInputKeyDown, пока попап открыт.
func (p *mentionPopup) commitSelected() {
	if p.onCommit == nil {
		return
	}

	cur := int(int32(win.SendMessage(p.hwnd, win.LB_GETCURSEL, 0, 0)))
	if cur < 0 || cur >= len(p.items) {
		return
	}

	p.onCommit(p.items[cur])
}

// wndProc — сабкласс попапа ради клика мышью по строке (см. комментарий
// у mentionPopup про то, почему не полагаемся на штатный
// WM_COMMAND/LBN_SELCHANGE). Всё остальное просто уходит в исходную
// оконную процедуру без изменений.
func (p *mentionPopup) wndProc(hwnd win.HWND, msg uint32, wParam, lParam uintptr) uintptr {
	const wmLButtonUp = 0x0202

	if msg == wmLButtonUp {
		// lParam WM_LBUTTONUP уже упакован ровно так же, как ожидает
		// LB_ITEMFROMPOINT (x в младшем слове, y в старшем, обе — координаты
		// в клиентской области ЭТОГО ЖЕ окна) — можно передать как есть, без
		// перепаковки.
		raw := uint32(win.SendMessage(hwnd, win.LB_ITEMFROMPOINT, 0, lParam))
		idx := int(raw & 0xffff)
		// Старший бит верхнего слова результата — "мимо всех строк"
		// (клик вне списка), см. документацию LB_ITEMFROMPOINT на MSDN.
		outside := raw>>16 != 0

		if !outside && idx >= 0 && idx < len(p.items) {
			win.SendMessage(hwnd, win.LB_SETCURSEL, uintptr(idx), 0)
			p.commitSelected()
		}
	}

	return win.CallWindowProc(p.origProc, hwnd, msg, wParam, lParam)
}
