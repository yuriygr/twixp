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

// profileAvatarSize — сторона квадрата под аватарку в окне профиля.
const profileAvatarSize = 72

// profileDialogData — всё, что нужно окну профиля. line — сообщение, по
// которому кликнули: оттуда берётся то, что известно сразу, без сети
// (имя, цвет, бейджи), чтобы окно открывалось мгновенно и не пустым.
type profileDialogData struct {
	line         chatLine
	resolveBadge func(domain.Badge) *walk.Bitmap
	twitch       app.TwitchAPI
	fetchImage   ImageFetcher
}

// showProfileDialog показывает модальное окно "Профиль пользователя".
// Сразу выводит то, что известно из самого сообщения, а данные из API
// (тип канала, возраст аккаунта, описание, аватарка) подгружает в фоне
// и дорисовывает — тот же принцип, что у аватарок в сайдбаре.
//
// Модальное, как диалог настроек: окно простое, живёт секунды, а
// немодальному пришлось бы разбираться с закрытием вместе с главным.
func showProfileDialog(owner *walk.MainWindow, d profileDialogData) {
	var (
		dlg      *walk.Dialog
		closeBtn *walk.PushButton

		avatar    *walk.ImageView
		typeLabel *walk.Label
		ageLabel  *walk.Label
		descEdit  *walk.TextEdit

		// avatarBmp и placeholderBmp хранятся, чтобы освободить
		// GDI-объекты после закрытия окна.
		avatarBmp      *walk.Bitmap
		placeholderBmp *walk.Bitmap
		// closed ставится в UI-потоке после закрытия окна; фоновая
		// загрузка проверяет его перед тем, как трогать виджеты.
		closed bool
	)

	line := d.line

	// Заглушка вместо аватарки, пока настоящая качается: на её месте не
	// должно быть пустого квадрата, который потом резко заполняется.
	// Не получилось создать — не страшно, просто будет пустое место.
	var avatarImage walk.Image
	if bmp := newAvatarPlaceholder(); bmp != nil {
		placeholderBmp = bmp
		avatarImage = bmp
	}

	// Бейджи — полоса одних иконок, как в вебе Twitch (см.
	// badgeStrip). Названия в окне не пишем: бейджей бывает много, и
	// подписи рядом с каждым съели бы всё место; они доступны во
	// всплывающей подсказке над полосой.
	var (
		icons  []walk.Image
		titles []string
	)
	for _, b := range line.Badges {
		titles = append(titles, domain.BadgeTitle(b.Name))

		if d.resolveBadge == nil {
			continue
		}
		// Иконка могла ещё не загрузиться (resolveBadge вернёт nil) — такой
		// бейдж в полосе пропускаем. Приводим к walk.Image только непустой
		// указатель: иначе в интерфейсе окажется (*Bitmap)(nil), а не nil.
		if bmp := d.resolveBadge(b); bmp != nil {
			icons = append(icons, bmp)
		}
	}

	children := []declarative.Widget{
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
						// Имя — цветом ника, как в чате.
						declarative.Label{
							Text:      line.Author,
							TextColor: line.Color,
							Font:      declarative.Font{Family: "Tahoma", PointSize: 11, Bold: true},
						},
						declarative.Label{Text: "@" + line.AuthorLogin},
						declarative.Label{AssignTo: &typeLabel, Visible: false},
						declarative.Label{AssignTo: &ageLabel, Text: "Загрузка…"},
						declarative.VSpacer{},
					},
				},
			},
		},
	}

	switch {
	case len(icons) > 0:
		children = append(children, newBadgeStrip(icons, strings.Join(titles, ", ")))
	case len(titles) > 0:
		// Бейджи есть, но ни одна иконка не загрузилась (например, нет
		// сети) — хотя бы названиями, чтобы информация не пропала.
		children = append(children, declarative.Label{Text: strings.Join(titles, ", ")})
	}

	children = append(children,
		declarative.TextEdit{
			AssignTo: &descEdit,
			ReadOnly: true,
			VScroll:  true,
			// Поле видно с самого начала, чтобы окно сразу имело итоговый
			// размер: если скрывать его до загрузки, то при появлении
			// описания окну приходится вырасти, и это заметный скачок.
			MinSize: declarative.Size{Height: 70},
		},
		declarative.Composite{
			Layout: declarative.HBox{MarginsZero: true},
			Children: []declarative.Widget{
				declarative.HSpacer{},
				declarative.PushButton{
					Text:      "Открыть на Twitch",
					OnClicked: func() { openChannelPage(line.AuthorLogin) },
				},
				declarative.PushButton{
					AssignTo:  &closeBtn,
					Text:      "Закрыть",
					OnClicked: func() { dlg.Accept() },
				},
			},
		},
	)

	err := (declarative.Dialog{
		AssignTo:      &dlg,
		Icon:          appIcon(),
		Title:         "Профиль: " + line.Author,
		MinSize:       declarative.Size{Width: 360},
		Layout:        declarative.VBox{},
		DefaultButton: &closeBtn,
		CancelButton:  &closeBtn,
		Children:      children,
	}).Create(owner)
	if err != nil {
		log.Println("окно профиля:", err)
		return
	}

	// Данные из API — в фоне; окно к этому моменту уже на экране.
	go func() {
		profile, err := d.twitch.GetUserProfile(line.AuthorID)
		if err != nil {
			log.Printf("профиль %s: %v", line.AuthorLogin, err)
			owner.Synchronize(func() {
				if !closed {
					ageLabel.SetText("Не удалось загрузить профиль")
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

			// Нет даты — строку про возраст не показываем, а не оставляем "Загрузка…".
			age := domain.AccountAgeText(profile.CreatedAt, time.Now())
			ageLabel.SetText(age)
			ageLabel.SetVisible(age != "")

			// У walk TextEdit переводы строк — "\r\n", а Twitch отдаёт "\n".
			desc := strings.TrimSpace(profile.Description)
			if desc == "" {
				desc = "Нет описания"
			}
			descEdit.SetText(textEditText(desc))
		})

		loadAvatar(owner, avatar, d.fetchImage, profile.AvatarURL, line.AuthorLogin,
			func() bool { return !closed },
			func(bmp *walk.Bitmap) { avatarBmp = bmp })
	}()

	dlg.Run()

	closed = true
	if avatarBmp != nil {
		avatarBmp.Dispose()
	}
	if placeholderBmp != nil {
		placeholderBmp.Dispose()
	}
}

// Параметры полосы бейджей. Иконки рисуются в родном размере (Helix
// отдаёт 18×18, см. комментарий у badgeIconSize в chatview.go) — без
// масштабирования, чтобы не мылились; в чате их сжимают до 14 пикселей под
// высоту строки, в окне места достаточно.
const (
	badgeStripIcon = 18
	badgeStripGap  = 4
	badgeStripStep = badgeStripIcon + badgeStripGap
	// badgeStripPerRow — сколько иконок в ряду, потом перенос. Число
	// фиксированное, а не зависящее от ширины окна: в пиненном lxn/walk
	// нет раскладки с переносом, а свой ряд из 14 иконок (около 300
	// пикселей) гарантированно помещается в окно минимальной ширины.
	badgeStripPerRow = 14
)

// newBadgeStrip — одна самописная полоса, рисующая все иконки сразу,
// вместо отдельного виджета на каждую: бейджей бывает за десяток, а
// виджет — это отдельное окно в Windows. Перенос на следующую строку —
// по числу иконок (badgeStripPerRow), высота считается из их количества.
func newBadgeStrip(icons []walk.Image, tooltip string) declarative.Widget {
	rows := (len(icons) + badgeStripPerRow - 1) / badgeStripPerRow
	height := rows*badgeStripStep - badgeStripGap

	return declarative.CustomWidget{
		ToolTipText:      tooltip,
		ClearsBackground: true,
		// Цвет фона диалога — иначе под иконками осталась бы чёрная
		// заливка.
		Background: declarative.SystemColorBrush{Color: walk.SysColor3DFace},
		MinSize:    declarative.Size{Width: badgeStripPerRow*badgeStripStep - badgeStripGap, Height: height},
		MaxSize:    declarative.Size{Height: height},
		Paint: func(canvas *walk.Canvas, _ walk.Rectangle) error {
			for i, icon := range icons {
				at := walk.Point{
					X: (i % badgeStripPerRow) * badgeStripStep,
					Y: (i / badgeStripPerRow) * badgeStripStep,
				}
				if err := canvas.DrawImage(icon, at); err != nil {
					return err
				}
			}
			return nil
		},
	}
}
