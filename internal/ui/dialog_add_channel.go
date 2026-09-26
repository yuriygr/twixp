//go:build windows
// +build windows

// Диалог добавления канала по логину — модальный, открывается кликом
// по кнопке в сайдбаре (см. sidebar.go, onAddChannelClicked).
//
// В отличие от входа (см. signinpage.go, там от диалога отказались)
// модальность здесь — то, что нужно, без всяких оговорок: это разовое,
// инициированное самим пользователем действие уже ПОСЛЕ того, как
// основное окно точно видно и с ним уже взаимодействовали (кнопку же
// как-то нажали) — риск "окно съехало за экран, а диалог блокирует его
// подвинуть", из-за которого страница заменила диалог на входе, тут
// попросту неприменим: до этой кнопки ещё надо было дотянуться.
package ui

import (
	"strings"

	"github.com/lxn/walk"
	"github.com/lxn/walk/declarative"
)

// showAddChannelDialog показывает модальный диалог добавления канала
// и блокируется, пока он не закроется. Возвращает введённый логин и
// true, если пользователь нажал "Добавить" с непустым текстом; иначе
// ("", false) — отмена (крестик, Escape, кнопка "Отмена").
func showAddChannelDialog(owner walk.Form) (string, bool) {
	var dlg *walk.Dialog
	var loginEdit *walk.LineEdit
	var addBtn, cancelBtn *walk.PushButton

	login := ""
	ok := false

	// accept — общий путь и для кнопки "Добавить", и для Enter
	// (DefaultButton ниже маршрутизирует Enter на addBtn.Clicked сам,
	// это штатный win32-механизм диалогов через IsDialogMessage — двух
	// разных обработчиков для одного действия не нужно).
	accept := func() {
		login = strings.TrimSpace(loginEdit.Text())
		if login == "" {
			return
		}
		ok = true
		dlg.Accept()
	}

	icon := appIcon()

	err := (declarative.Dialog{
		AssignTo:      &dlg,
		Icon:          icon,
		Title:         "Добавить канал",
		FixedSize:     true,
		Size:          declarative.Size{Width: 280, Height: 110},
		Layout:        declarative.VBox{},
		DefaultButton: &addBtn,
		CancelButton:  &cancelBtn,
		Children: []declarative.Widget{
			declarative.Composite{
				Layout: declarative.HBox{Margins: declarative.Margins{Left: 8, Top: 8, Right: 8, Bottom: 4}},
				Children: []declarative.Widget{
					declarative.LineEdit{
						AssignTo:  &loginEdit,
						CueBanner: "Логин канала",
					},
				},
			},
			declarative.Composite{
				Layout: declarative.HBox{Margins: declarative.Margins{Left: 8, Right: 8, Bottom: 8}},
				Children: []declarative.Widget{
					declarative.HSpacer{},
					declarative.PushButton{
						AssignTo:  &addBtn,
						Text:      "Добавить",
						OnClicked: accept,
					},
					declarative.PushButton{
						AssignTo: &cancelBtn,
						Text:     "Отмена",
						OnClicked: func() {
							dlg.Cancel()
						},
					},
				},
			},
		},
	}).Create(owner)
	if err != nil {
		return "", false
	}

	dlg.Run()

	return login, ok
}
