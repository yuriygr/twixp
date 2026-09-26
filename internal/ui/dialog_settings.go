//go:build windows
// +build windows

package ui

import (
	"log"

	"github.com/lxn/walk"
	"github.com/lxn/walk/declarative"

	"twixp/internal/domain"
)

// showSettingsDialog показывает модальный диалог настроек — отдельное
// окно (walk.Dialog), а не страница в pagehost: третий вид страницы
// ради одной формы с чекбоксами усложнил бы pagehost без особой нужды,
// а обычный модальный Dialog для настроек — стандартное поведение для
// Windows-приложений этой эпохи в принципе.
//
// current — то, что сейчас действует (см. MainWindow.settings), save —
// куда персистить (см. store.Store.SaveSettings), apply — что сделать
// с уже изменённым значением немедленно (см. MainWindow.applySettings).
// apply вызывается после КАЖДОГО изменения любого переключателя, без
// отдельной кнопки "Применить" и без Cancel, откатывающего изменения:
// для диалога такого размера live-применение проще и нагляднее, чем
// городить отдельное состояние "несохранённые изменения" — пользователь
// сразу видит результат, не гадая, что случится после закрытия.
func showSettingsDialog(owner walk.Form, current domain.Settings, save SettingsSaver, apply func(domain.Settings)) {
	settings := current // рабочая копия — мутируем её по ходу диалога, а не current вызывающего кода

	// commit — общий хвост после изменения ЛЮБОГО поля: применить
	// немедленно к виджетам и сохранить на диск. Не проверяет,
	// действительно ли значение поменялось — вызовов немного, не в
	// цикле, а сеттеры внутри apply (chatView.setShowTimestamps и т.п.)
	// и так уже сами ничего не делают при повторном том же значении.
	commit := func() {
		apply(settings)
		if save == nil {
			return
		}
		if err := save(settings); err != nil {
			log.Println("сохранить настройки:", err)
		}
	}

	var dlg *walk.Dialog
	var closeBtn *walk.PushButton

	var showTimestamps, showBadges, highlightMentions *walk.CheckBox
	var showAvatars, mentionAutocomplete, alwaysOnTop *walk.CheckBox
	var fontSize *walk.NumberEdit

	icon := appIcon()

	err := (declarative.Dialog{
		AssignTo:      &dlg,
		Icon:          icon,
		Title:         "Настройки",
		MinSize:       declarative.Size{Width: 300},
		Layout:        declarative.VBox{},
		DefaultButton: &closeBtn,
		CancelButton:  &closeBtn,
		Children: []declarative.Widget{
			declarative.CheckBox{
				AssignTo: &showTimestamps,
				Text:     "Показывать время сообщений",
				Checked:  settings.ShowTimestamps,
				OnCheckedChanged: func() {
					settings.ShowTimestamps = showTimestamps.Checked()
					commit()
				},
			},
			declarative.CheckBox{
				AssignTo: &showBadges,
				Text:     "Показывать бейджики (модератор, VIP, подписка и т.п.)",
				Checked:  settings.ShowBadges,
				OnCheckedChanged: func() {
					settings.ShowBadges = showBadges.Checked()
					commit()
				},
			},
			declarative.CheckBox{
				AssignTo: &highlightMentions,
				Text:     "Подсвечивать упоминания меня",
				Checked:  settings.HighlightMentions,
				OnCheckedChanged: func() {
					settings.HighlightMentions = highlightMentions.Checked()
					commit()
				},
			},
			declarative.CheckBox{
				AssignTo: &showAvatars,
				Text:     "Показывать аватарки в списке каналов",
				Checked:  settings.ShowChannelAvatars,
				OnCheckedChanged: func() {
					settings.ShowChannelAvatars = showAvatars.Checked()
					commit()
				},
			},
			declarative.CheckBox{
				AssignTo: &mentionAutocomplete,
				Text:     "Автодополнение \"@\" при наборе сообщения",
				Checked:  settings.MentionAutocomplete,
				OnCheckedChanged: func() {
					settings.MentionAutocomplete = mentionAutocomplete.Checked()
					commit()
				},
			},
			declarative.CheckBox{
				AssignTo: &alwaysOnTop,
				Text:     "Поверх остальных окон",
				Checked:  settings.AlwaysOnTop,
				OnCheckedChanged: func() {
					settings.AlwaysOnTop = alwaysOnTop.Checked()
					commit()
				},
			},
			declarative.Composite{
				Layout: declarative.HBox{MarginsZero: true},
				Children: []declarative.Widget{
					declarative.Label{Text: "Размер шрифта чата (0 — по умолчанию):"},
					declarative.NumberEdit{
						AssignTo:  &fontSize,
						MinValue:  0,
						MaxValue:  24,
						Decimals:  0,
						Increment: 1,
						Value:     float64(settings.FontSize),
						// Только запоминаем значение — commit() (а
						// значит и дорогой setFontSize →
						// recomputeLayout, 500 MeasureText на большой
						// истории) не зовём на каждое изменение: у
						// NumberEdit в этой версии walk ValueChanged
						// стреляет на КАЖДУЮ цифру набора (печатаете "1",
						// потом "12" — событие два раза, с разными
						// значениями), а не после того, как пользователь
						// закончил. commit() зовём один раз чуть ниже,
						// когда поле теряет фокус (см. FocusedChanged
						// после Create) — и ещё раз, страховкой, прямо
						// перед закрытием диалога (см. closeBtn).
						OnValueChanged: func() {
							settings.FontSize = int(fontSize.Value())
						},
					},
				},
			},
			declarative.PushButton{
				AssignTo: &closeBtn,
				Text:     "Закрыть",
				OnClicked: func() {
					// commit(), а не просто dlg.Accept(): если
					// пользователь дошёл до Enter прямо из NumberEdit,
					// не переводя фокус (IsDialogMessage может отдать
					// Enter дефолтной кнопке напрямую, без промежуточного
					// WM_KILLFOCUS на поле) — FocusedChanged ниже не
					// успеет сработать, и последнее набранное значение
					// применится/сохранится только тут. Повторный вызов
					// commit() с уже применённым значением ничего не
					// стоит — все сеттеры внутри apply идемпотентны (см.
					// комментарий выше).
					commit()
					dlg.Accept()
				},
			},
		},
	}).Create(owner)
	if err != nil {
		log.Println("открыть диалог настроек:", err)
		return
	}

	// FocusedChanged — не декларативное поле NumberEdit, вешаем уже
	// после Create. commit() здесь — тот самый момент "пользователь
	// закончил печатать", которого NumberEdit в этой версии walk не
	// даёт напрямую (нет OnEditingFinished).
	fontSize.FocusedChanged().Attach(func() {
		if !fontSize.Focused() {
			commit()
		}
	})

	dlg.Run()
}
