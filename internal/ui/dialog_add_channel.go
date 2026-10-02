//go:build windows
// +build windows

// Диалог добавления канала — модальный, открывается кликом по кнопке в
// сайдбаре (см. sidebar.go, onAddChannelClicked).
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
	"fmt"
	"log"
	"strings"

	"github.com/lxn/walk"
	"github.com/lxn/walk/declarative"

	"twixp/internal/app"
	"twixp/internal/domain"
)

// addChannelDialogData — то, что диалогу нужно от вызывающего кода.
type addChannelDialogData struct {
	// twitch и viewerID — откуда и чьи подписки загружать (блокирующий
	// сетевой вызов, диалог зовёт его из фоновой горутины). Нет того или
	// другого — списка подписок нет вовсе, диалог работает как простое
	// поле для логина.
	twitch   app.TwitchAPI
	viewerID string
	// open — ID каналов, уже открытых в сайдбаре: в списке подписок их
	// не показываем.
	open map[string]bool
}

// showAddChannelDialog показывает модальный диалог добавления канала и
// блокируется, пока он не закроется. Возвращает логины выбранных
// каналов и true, если пользователь нажал "Добавить"; иначе (nil, false)
// — отмена (крестик, Escape, кнопка "Отмена").
//
// Что именно вернётся: выделенные в списке подписок каналы (можно
// несколько), а если в списке ничего не выделено — логин, введённый в
// поле. То же поле работает и как фильтр списка по подстроке, поэтому
// набранное не обязано быть логином существующей подписки.
func showAddChannelDialog(owner *walk.MainWindow, d addChannelDialogData) ([]string, bool) {
	var (
		dlg         *walk.Dialog
		loginEdit   *walk.LineEdit
		statusLabel *walk.Label
		listBox     *walk.ListBox
		addBtn      *walk.PushButton
		cancelBtn   *walk.PushButton // нужен только как AssignTo для CancelButton ниже — маршрутизирует Esc на его Clicked тем же win32-механизмом, что и DefaultButton для Enter; свой OnClicked ему не нужен

		all     []domain.FollowedChannel // подписки как пришли (отсортированы)
		visible []domain.FollowedChannel // после фильтра — строки listBox по порядку

		result []string
		ok     bool
		// closed ставится в UI-потоке после закрытия окна; фоновая
		// загрузка проверяет его, прежде чем трогать виджеты.
		closed bool
	)

	// refresh пересобирает список под текущий текст в поле.
	refresh := func() {
		if listBox == nil || loginEdit == nil {
			return // событие пришло до того, как дерево виджетов собрано
		}

		visible = domain.FilterFollowed(all, loginEdit.Text(), d.open)

		labels := make([]string, len(visible))
		for i, f := range visible {
			labels[i] = f.ListLabel()
		}
		if err := listBox.SetModel(labels); err != nil {
			log.Println("список подписок:", err)
		}
	}

	// accept — общий путь и для кнопки "Добавить", и для Enter
	// (DefaultButton ниже маршрутизирует Enter на addBtn.Clicked сам, это
	// штатный win32-механизм диалогов через IsDialogMessage), и для
	// двойного клика по строке списка.
	accept := func() {
		var picked []string
		for _, i := range listBox.SelectedIndexes() {
			if i >= 0 && i < len(visible) {
				picked = append(picked, visible[i].Channel.Name)
			}
		}

		if len(picked) == 0 {
			typed := strings.TrimSpace(loginEdit.Text())
			if typed == "" {
				return
			}
			picked = []string{typed}
		}

		result = picked
		ok = true
		dlg.Accept()
	}

	hasFollowed := d.twitch != nil && d.viewerID != ""

	status := "Загрузка подписок…"
	if !hasFollowed {
		status = ""
	}

	err := (declarative.Dialog{
		AssignTo:      &dlg,
		Icon:          appIcon(),
		Title:         "Добавить канал",
		MinSize:       declarative.Size{Width: 320, Height: 300},
		Size:          declarative.Size{Width: 340, Height: 420},
		Layout:        declarative.VBox{Margins: declarative.Margins{Left: 8, Top: 8, Right: 8, Bottom: 8}},
		DefaultButton: &addBtn,
		CancelButton:  &cancelBtn,
		Children: []declarative.Widget{
			declarative.LineEdit{
				AssignTo:      &loginEdit,
				CueBanner:     "Логин канала или поиск по подпискам",
				OnTextChanged: refresh,
			},
			declarative.Label{
				AssignTo: &statusLabel,
				Text:     status,
				Visible:  hasFollowed,
			},
			declarative.ListBox{
				AssignTo:        &listBox,
				MultiSelection:  true,
				StretchFactor:   1,
				Visible:         hasFollowed,
				OnItemActivated: accept,
			},
			declarative.Composite{
				Layout: declarative.HBox{MarginsZero: true},
				Children: []declarative.Widget{
					declarative.HSpacer{},
					declarative.PushButton{
						AssignTo:  &addBtn,
						Text:      "Добавить",
						OnClicked: accept,
					},
					declarative.PushButton{
						AssignTo:  &cancelBtn,
						Text:      "Отмена",
						OnClicked: func() { dlg.Cancel() },
					},
				},
			},
		},
	}).Create(owner)
	if err != nil {
		log.Println("диалог добавления канала:", err)
		return nil, false
	}

	// Подписки — в фоне: диалог уже на экране и им можно пользоваться
	// (ввести логин руками), пока список грузится.
	if hasFollowed {
		go func() {
			list, err := d.twitch.GetFollowedChannels(d.viewerID)

			owner.Synchronize(func() {
				if closed {
					return
				}

				if err != nil {
					// Самая вероятная причина — токен выдан до того, как
					// приложению понадобилось право читать подписки: тогда
					// помогает повторный вход.
					log.Println("подписки:", err)
					statusLabel.SetText("Подписки недоступны (возможно, нужен повторный вход)")
					return
				}

				all = list
				refresh()

				switch n := len(domain.FilterFollowed(all, "", d.open)); {
				case len(all) == 0:
					statusLabel.SetText("Подписок нет")
				case n == 0:
					statusLabel.SetText("Все ваши подписки уже открыты")
				default:
					statusLabel.SetText(fmt.Sprintf("Ваши подписки: %d  (● — сейчас в эфире)", n))
				}
			})
		}()
	}

	dlg.Run()
	closed = true

	if !ok {
		return nil, false
	}
	return result, true
}
