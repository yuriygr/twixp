//go:build windows
// +build windows

package ui

import (
	"log"
	"net/url"
	"strings"

	"github.com/lxn/walk"

	"twixp/internal/domain"
)

// Общие мелочи окон "Профиль пользователя" и "Информация о канале".
//
// ВАЖНО для любых новых окон: информационные окна — только walk.Dialog
// (declarative.Dialog + Run), а не walk.MsgBox. В пиненной версии
// lxn/walk функции, переданные в Synchronize, выполняет собственный цикл
// сообщений walk (FormBase.mainLoop → runSynchronized), а MsgBox — это
// системный MessageBox со своим внутренним циклом, где этого вызова нет:
// пока такое окошко открыто, все обновления из фоновых горутин (в том
// числе новые сообщения чата) копятся в очереди и не показываются.

// newAvatarPlaceholder создаёт bitmap-заглушку аватарки (см.
// domain.PlaceholderAvatar) или nil, если не получилось: тогда на месте
// аватарки будет просто пустое место, не страшно. Освобождать
// (Dispose) должен вызывающий код после закрытия окна.
func newAvatarPlaceholder() *walk.Bitmap {
	bmp, err := walk.NewBitmapFromImage(domain.PlaceholderAvatar(profileAvatarSize))
	if err != nil {
		log.Println("заглушка аватарки:", err)
		return nil
	}
	return bmp
}

// loadAvatar скачивает аватарку (блокирующий вызов — звать из фоновой
// горутины!) и показывает её в view. alive сообщает, что окно ещё
// открыто (зовётся уже в UI-потоке), keep получает созданный bitmap,
// чтобы вызывающий код освободил его при закрытии окна. what — подпись
// для лога. Ошибки не критичны: остаётся заглушка.
func loadAvatar(owner *walk.MainWindow, view *walk.ImageView, fetch ImageFetcher,
	avatarURL, what string, alive func() bool, keep func(*walk.Bitmap)) {

	if avatarURL == "" || fetch == nil {
		return
	}

	img, err := fetch(avatarURL)
	if err != nil {
		log.Printf("аватарка %s: %v", what, err)
		return
	}

	owner.Synchronize(func() {
		if !alive() {
			return
		}
		// Bitmap — это GDI, создаём в UI-потоке (как toSidebarIcon в
		// sidebar.go и ensureBadgeIcon в chatpane.go).
		bmp, err := walk.NewBitmapFromImage(domain.ResizeNearest(img, profileAvatarSize))
		if err != nil {
			log.Printf("аватарка %s: %v", what, err)
			return
		}
		keep(bmp)
		view.SetImage(bmp)
	})
}

// textEditText приводит переводы строк к тому виду, что ждёт
// walk.TextEdit: "\r\n" вместо "\n", с которым приходят тексты из
// Twitch и из domain.
func textEditText(s string) string {
	return strings.Replace(s, "\n", "\r\n", -1)
}

// openChannelPage открывает страницу канала на twitch.tv в браузере.
func openChannelPage(login string) {
	if login == "" {
		return
	}
	if err := openURL("https://www.twitch.tv/" + url.PathEscape(login)); err != nil {
		log.Println("открыть страницу в браузере:", err)
	}
}
