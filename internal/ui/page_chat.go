//go:build windows
// +build windows

// Страница чата: сайдбар слева, область чата справа. Строится ровно
// один раз за сессию, сразу после успешного входа (см. applySignIn в
// mainwindow.go) — до входа этому дереву нечего показывать (ни списка
// каналов, ни истории), а строить его заранее ради последующего
// Visible-переключения — тот самый способ навсегда получить виджеты
// нулевого размера, см. подробное объяснение в pagehost.go.
package ui

import (
	"github.com/lxn/walk"
	"github.com/lxn/walk/declarative"
	"github.com/lxn/win"
)

// buildChatPage — pageFactory для pageHost.show (см. pagehost.go).
// m нужен целиком (а не отдельными callback-полями, как в
// buildSignInPage) — sidebar и chatPane уже сами получают свои
// зависимости через конструкторы в New(), сюда нужны только их
// AssignTo-указатели для declarative-дерева.
func buildChatPage(m *MainWindow) pageFactory {
	// Думаю так будет удобнее
	defaultMargins := declarative.Margins{Left: 2, Top: 2, Right: 2, Bottom: 2}
	return func(parent walk.Container) (*walk.Composite, error) {
		var root *walk.Composite

		err := (declarative.Composite{
			AssignTo: &root,
			Layout:   declarative.HBox{MarginsZero: true, SpacingZero: true},
			Children: []declarative.Widget{
				declarative.Composite{
					MinSize: declarative.Size{Width: 130},
					MaxSize: declarative.Size{Width: 130},
					Layout:  declarative.VBox{MarginsZero: true, SpacingZero: true},
					Children: []declarative.Widget{
						declarative.Composite{
							Layout: declarative.HBox{Margins: defaultMargins},
							Children: []declarative.Widget{
								declarative.PushButton{
									AssignTo:  &m.sidebar.addChannelBtn,
									Text:      "Добавить канал",
									OnClicked: m.sidebar.onAddChannelClicked,
								},
							},
						},
						declarative.TableView{
							AssignTo:              &m.sidebar.view,
							Model:                 m.sidebar.model,
							HeaderHidden:          true,
							LastColumnStretched:   true,
							Columns:               []declarative.TableViewColumn{{}},
							OnCurrentIndexChanged: m.sidebar.onSidebarSelectionChanged,
							OnMouseDown:           m.sidebar.onSidebarMouseDown,
							ContextMenuItems: []declarative.MenuItem{
								declarative.Action{
									Text:        "Информация о канале",
									OnTriggered: m.sidebar.onChannelInfoClicked,
								},
								declarative.Separator{},
								declarative.Action{
									Text:        "Удалить чат",
									OnTriggered: m.sidebar.onDeleteChannelClicked,
								},
							},
						},
						declarative.Composite{
							Layout: declarative.HBox{Margins: defaultMargins},
							Children: []declarative.Widget{
								declarative.PushButton{
									Text: "Настройки",
									// m.onSettingsClicked, а не через sidebar — сама
									// кнопка расположена в колонке сайдбара только
									// географически, к списку каналов как таковому
									// не относится, поэтому и не заведена как метод
									// sidebar (в отличие от onAddChannelClicked).
									OnClicked: m.onSettingsClicked,
								},
							},
						},
					},
				},
				declarative.Composite{
					Layout: declarative.VBox{MarginsZero: true, SpacingZero: true},
					Children: []declarative.Widget{
						declarative.CustomWidget{
							AssignTo: &m.chatPane.view.widget,
							Paint:    m.chatPane.view.paint,
							// PaintBuffered — paint() рисует не прямо в
							// экранный HDC, а в offscreen-битмап, и walk
							// сам одним BitBlt переносит готовый кадр на
							// экран (см. bufferedPaint в vendor lxn/walk,
							// customwidget.go). Без этого на каждое новое
							// сообщение/скролл экран успевал показать
							// кадр с уже стёртым, но ещё не дорисованным
							// текстом — источник мерцания истории,
							// отдельный от WM_ERASEBKGND (тот подавлен
							// ниже, в subclassWndProc, и решает другую
							// половину той же проблемы).
							PaintMode: declarative.PaintBuffered,
							// WS_VSCROLL — declarative.CustomWidget не
							// даёт декларативного способа добавить
							// скроллбар; chatView сам сабклассит
							// WM_VSCROLL, см. chatview.go.
							Style: win.WS_VSCROLL,
						},
						declarative.Composite{
							AssignTo: &m.chatPane.replyBanner,
							Visible:  false,
							Layout:   declarative.HBox{Margins: defaultMargins},
							Children: []declarative.Widget{
								declarative.Label{
									AssignTo: &m.chatPane.replyLabel,
								},
								declarative.PushButton{
									Text:      "×",
									MaxSize:   declarative.Size{Width: 24},
									OnClicked: m.chatPane.onCancelReplyClicked,
								},
							},
						},
						declarative.Composite{
							Layout: declarative.HBox{Margins: defaultMargins},
							Children: []declarative.Widget{
								declarative.LineEdit{
									AssignTo:      &m.chatPane.input,
									OnKeyDown:     m.chatPane.onInputKeyDown,
									OnTextChanged: m.chatPane.onInputTextChanged,
								},
								declarative.PushButton{
									Text:      "Отправить",
									OnClicked: m.chatPane.onSendClicked,
								},
							},
						},
					},
				},
			},
		}).Create(declarative.NewBuilder(parent))
		if err != nil {
			return nil, err
		}

		// Список каналов слева обрамлён чёрной WS_BORDER рамкой, которую
		// declarative.TableView не даёт отключить декларативно, а вместо
		// неё нужна нативная утопленная 3D-окантовка — см.
		// nativeClientEdge в sidebar.go. Раньше вызывалось сразу после
		// Create() в build() — теперь на том же месте по смыслу: сразу
		// после того, как у TableView появился настоящий hwnd.
		nativeClientEdge(m.sidebar.view)

		// Сабклассинг ради WM_VSCROLL (см. chatview.go) — по той же
		// причине можно делать только теперь, а не в New(): раньше
		// CustomWidget существовал сразу при создании окна, теперь —
		// только с этого момента.
		m.chatPane.view.attach()

		// Попап автодополнения "@..." (см. mentionpopup.go) создаётся
		// как отдельное WS_POPUP-окно с owner'ом — главным окном; раньше
		// этого места m.window.Handle() ещё не был доступен изнутри
		// New() (см. mainwindow.go).
		m.chatPane.attachMentionPopup(m.window.Handle())

		return root, nil
	}
}
