//go:build windows
// +build windows

package ui

import (
	"log"
	"strings"
	"time"

	"github.com/lxn/walk"
	"github.com/lxn/walk/declarative"

	"twixp/internal/app"
	"twixp/internal/domain"
)

// Цвета статуса трансляции.
var (
	statusLiveColor    = walk.RGB(0, 128, 0)
	statusOfflineColor = walk.RGB(110, 110, 110)
)

// channelDialogData — всё, что нужно окну канала. channel — то, что о
// канале известно сразу, без сети (ID, логин, имя), чтобы окно
// открывалось мгновенно.
type channelDialogData struct {
	channel  domain.Channel
	viewerID string // кто смотрит; нужен для статуса подписки

	twitch     app.TwitchAPI
	fetchImage ImageFetcher
}

// showChannelDialog показывает модальное окно "Информация о канале": имя,
// аватарка, тип канала, статус трансляции (на момент открытия окна, не
// обновляется), статус подписки и описание канала.
//
// Сделано по образцу окна профиля пользователя (dialog_profile.go) и,
// как и оно, — walk.Dialog, а не MsgBox: см. предупреждение в
// dialog_common.go, MsgBox замораживает обновления чата.
//
// Данные из API грузятся в фоне тремя независимыми запросами, и каждый
// дорисовывается по готовности: быстрые не ждут медленных, а сбой одного
// не портит остальное.
func showChannelDialog(owner *walk.MainWindow, d channelDialogData) {
	var (
		dlg      *walk.Dialog
		closeBtn *walk.PushButton

		avatar      *walk.ImageView
		statusLabel *walk.Label
		typeLabel   *walk.Label
		streamEdit  *walk.TextEdit
		followLabel *walk.Label
		followBtn   *walk.PushButton
		descEdit    *walk.TextEdit

		avatarBmp      *walk.Bitmap
		placeholderBmp *walk.Bitmap
		// closed ставится в UI-потоке после закрытия окна; фоновые
		// загрузки проверяют его перед тем, как трогать виджеты.
		closed bool
	)

	channel := d.channel
	title := channel.DisplayName
	if title == "" {
		title = channel.Name
	}

	var avatarImage walk.Image
	if bmp := newAvatarPlaceholder(); bmp != nil {
		placeholderBmp = bmp
		avatarImage = bmp
	}

	// Свой канал: на себя не подписываются, статус подписки не запрашиваем.
	isOwn := d.viewerID != "" && d.viewerID == channel.ID
	canCheckFollow := d.viewerID != "" && d.twitch != nil && !isOwn

	err := (declarative.Dialog{
		AssignTo:      &dlg,
		Icon:          appIcon(),
		Title:         "Канал: " + title,
		MinSize:       declarative.Size{Width: 380},
		Layout:        declarative.VBox{},
		DefaultButton: &closeBtn,
		CancelButton:  &closeBtn,
		Children: []declarative.Widget{
			declarative.Composite{
				Layout: declarative.HBox{MarginsZero: true},
				Children: []declarative.Widget{
					declarative.ImageView{
						AssignTo: &avatar,
						Image:    avatarImage,
						Mode:     declarative.ImageViewModeCenter,
						MinSize:  declarative.Size{Width: profileAvatarSize, Height: profileAvatarSize},
						MaxSize:  declarative.Size{Width: profileAvatarSize, Height: profileAvatarSize},
					},
					declarative.Composite{
						Layout: declarative.VBox{MarginsZero: true},
						Children: []declarative.Widget{
							declarative.Label{
								Text: title,
								Font: declarative.Font{Family: "Tahoma", PointSize: 11, Bold: true},
							},
							declarative.Label{Text: "@" + channel.Name},
							declarative.Label{
								AssignTo:  &statusLabel,
								Text:      "Загрузка…",
								TextColor: statusOfflineColor,
							},
							declarative.Label{AssignTo: &typeLabel, Visible: false},
							declarative.VSpacer{},
						},
					},
				},
			},

			// Подробности трансляции: игра, название, зрители, длительность.
			// Поле видно с самого начала, чтобы окно сразу имело итоговый
			// размер и не росло при появлении данных.
			declarative.TextEdit{
				AssignTo: &streamEdit,
				ReadOnly: true,
				VScroll:  true,
				Text:     "Загрузка…",
				MinSize:  declarative.Size{Height: 54},
			},

			// Статус подписки и кнопка. Подписаться и отписаться из
			// приложения нельзя — Twitch убрал такие запросы из API в 2021
			// году (без замены), поэтому кнопка открывает страницу канала,
			// где это делается в один клик.
			declarative.Composite{
				Layout: declarative.HBox{MarginsZero: true},
				Children: []declarative.Widget{
					declarative.Label{AssignTo: &followLabel},
					declarative.HSpacer{},
					declarative.PushButton{
						AssignTo: &followBtn,
						Text:     "Открыть на Twitch",
						ToolTipText: "Twitch не позволяет менять подписку из сторонних приложений — " +
							"откроется страница канала",
						OnClicked: func() { openChannelPage(channel.Name) },
					},
				},
			},

			declarative.TextEdit{
				AssignTo: &descEdit,
				ReadOnly: true,
				VScroll:  true,
				MinSize:  declarative.Size{Height: 70},
			},

			declarative.Composite{
				Layout: declarative.HBox{MarginsZero: true},
				Children: []declarative.Widget{
					declarative.HSpacer{},
					declarative.PushButton{
						AssignTo:  &closeBtn,
						Text:      "Закрыть",
						OnClicked: func() { dlg.Accept() },
					},
				},
			},
		},
	}).Create(owner)
	if err != nil {
		log.Println("окно канала:", err)
		return
	}

	// Предупреждение: каждый обработчик ниже, прежде чем трогать виджеты,
	// проверяет closed — к моменту ответа окно могли закрыть.

	switch {
	case isOwn:
		followLabel.SetText("Это ваш канал")
	case !canCheckFollow:
		followLabel.SetText("")
	default:
		followLabel.SetText("Проверяем подписку…")
	}

	// Описание, тип канала, аватарка.
	if d.twitch != nil {
		go func() {
			profile, err := d.twitch.GetUserProfile(channel.ID)
			if err != nil {
				log.Printf("профиль канала %s: %v", channel.Name, err)
				owner.Synchronize(func() {
					if !closed {
						descEdit.SetText("Не удалось загрузить описание")
					}
				})
				return
			}

			owner.Synchronize(func() {
				if closed {
					return
				}

				if label := profile.BroadcasterLabel(); label != "" {
					typeLabel.SetText(label)
					typeLabel.SetVisible(true)
				}

				desc := strings.TrimSpace(profile.Description)
				if desc == "" {
					desc = "Нет описания"
				}
				descEdit.SetText(textEditText(desc))
			})

			loadAvatar(owner, avatar, d.fetchImage, profile.AvatarURL, channel.Name,
				func() bool { return !closed },
				func(bmp *walk.Bitmap) { avatarBmp = bmp })
		}()
	}

	// Статус трансляции — на момент открытия окна; дальше не обновляется.
	if d.twitch != nil {
		go func() {
			info, err := d.twitch.GetStream(channel.ID)
			if err != nil {
				log.Printf("трансляция %s: %v", channel.Name, err)
			}

			now := time.Now()
			owner.Synchronize(func() {
				if closed {
					return
				}

				if err != nil {
					statusLabel.SetText("Статус неизвестен")
					streamEdit.SetText("Не удалось узнать, идёт ли трансляция")
					return
				}

				if info.Live {
					statusLabel.SetTextColor(statusLiveColor)
					statusLabel.SetText("● " + info.StatusText())
				} else {
					statusLabel.SetTextColor(statusOfflineColor)
					statusLabel.SetText("● " + info.StatusText())
				}
				streamEdit.SetText(textEditText(info.Details(now)))
			})
		}()
	}

	// Подписан ли зритель на канал.
	if canCheckFollow {
		go func() {
			status, err := d.twitch.GetFollowStatus(d.viewerID, channel.ID)
			if err != nil {
				// Самая вероятная причина — токен выдан до того, как
				// приложению понадобилось право читать подписки.
				log.Printf("подписка на %s: %v", channel.Name, err)
			}

			owner.Synchronize(func() {
				if closed {
					return
				}

				if err != nil {
					followLabel.SetText("Статус подписки недоступен")
					return
				}

				followLabel.SetText(status.Text())
				if status.Following {
					followBtn.SetText("Отписаться на сайте")
				} else {
					followBtn.SetText("Подписаться на сайте")
				}
			})
		}()
	}

	dlg.Run()

	closed = true
	if avatarBmp != nil {
		avatarBmp.Dispose()
	}
	if placeholderBmp != nil {
		placeholderBmp.Dispose()
	}
}
