//go:build windows
// +build windows

package main

import (
	"fmt"
	"image"
	_ "image/jpeg" // для image.Decode в fetchImage ниже
	_ "image/png"
	"log"
	"net/http"

	"twixp/internal/app"
	"twixp/internal/domain"
	"twixp/internal/infra/appdir"
	"twixp/internal/infra/applog"
	"twixp/internal/infra/auth"
	"twixp/internal/infra/eventsub"
	"twixp/internal/infra/helix"
	"twixp/internal/infra/nettls"
	"twixp/internal/infra/store"
	"twixp/internal/ui"
)

// Компиляционная проверка: helix.Client реализует то, что ожидает
// eventsub.Hub.
var _ eventsub.SubscriptionManager = (*helix.Client)(nil)

// TwiXP — Twitch-чат клиент для Windows XP. Composition root: собирает
// signIn-замыкание (хранилище → авторизация → Helix → EventSub-хаб →
// ChatWorkspace) и передаёт его в internal/ui, которое само решает,
// когда его вызвать — один раз тихо сразу после старта, а если не
// получилось — показывает страницу входа (MainWindow.EnsureSignedIn).
//
// Окно обязано открыться в любом случае — это единственный способ
// пользователя вообще взаимодействовать с приложением. Поэтому ничего
// здесь не паникует и не выходит с os.Exit на ошибках старта: они
// уходят в лог-файл (applog.Open) и всплывают в статус-баре уже
// изнутри ui.MainWindow, когда signIn однажды будет вызван.
func main() {
	// Данные живут в профиле пользователя (%APPDATA%\TwiXP), а не рядом
	// с exe: так они переживают переезд/обновление папки с программой,
	// не лежат в каталоге, куда обычному пользователю может быть
	// запрещена запись, и у каждого пользователя машины свои.
	dataDir := appdir.Dir()

	if f := applog.Open(dataDir); f != nil {
		defer f.Close()
	}

	// Старый data рядом с exe (LegacyDirs) — источник одноразовой
	// миграции: store.New сам перенесёт оттуда данные, зашифрует токен
	// и удалит старый файл с открытым токеном.
	appStore, err := store.New(dataDir, appdir.LegacyDirs()...)
	if err != nil {
		log.Println("открыть хранилище:", err)
		// appStore остаётся nil — signIn ниже сразу вернёт понятную
		// ошибку вместо паники на nil.AsTokenStore().
	}

	var authService *app.AuthService
	if appStore != nil {
		authFlow := auth.NewDeviceFlow(clientID, scopes)
		authService = app.NewAuthService(appStore.AsTokenStore(), authFlow, authFlow)
	}

	// mw присваивается ниже, после ui.New — signIn на неё ссылается
	// (для hub.OnRevoked/OnResubscribeFailed), но реально вызывается
	// только позже, когда mw уже точно не nil (см. ui.MainWindow.
	// EnsureSignedIn и комментарий в internal/ui/mainwindow.go).
	var mw *ui.MainWindow

	signIn := func(onPrompt func(userCode, verificationURI string)) (ui.SignInResult, error) {
		if appStore == nil || authService == nil {
			return ui.SignInResult{}, fmt.Errorf("хранилище недоступно")
		}

		if onPrompt == nil {
			if _, err := authService.TryReuse(); err != nil {
				return ui.SignInResult{}, err
			}
		} else {
			if _, err := authService.EnsureAuthenticated(onPrompt); err != nil {
				return ui.SignInResult{}, err
			}
		}

		helixClient := helix.NewClient(clientID, func() (domain.Token, error) {
			return authService.EnsureAuthenticated(nil)
		})
		// Живой 401 (Twitch отверг токен, который мы только что
		// считали рабочим) — сигнал не доверять больше локальному
		// таймеру истечения и пойти на принудительное обновление.
		helixClient.InvalidateToken = authService.MarkInvalid

		me, err := helixClient.GetAuthenticatedUser()
		if err != nil {
			return ui.SignInResult{}, fmt.Errorf("получить профиль: %v", err)
		}
		helixClient.SenderID = me.ID

		hub := eventsub.NewHub(helixClient, me)

		// workspace присваивается ниже (NewChatWorkspace принимает
		// hub.NewReader, которому нужен уже готовый hub) — коллбэки
		// ссылаются на неё тем же приёмом forward-declaration, что и mw
		// выше: реально они вызовутся только после того, как канал уже
		// был добавлен через workspace.Add, то есть workspace к тому
		// моменту точно не nil.
		var workspace *app.ChatWorkspace

		hub.OnRevoked = func(channel domain.Channel, status string) {
			// Hub убирает канал только из СВОЕГО реестра (dropChannel) —
			// ChatWorkspace про это не узнаёт сам по себе, иначе канал
			// оставался бы в сайдбаре и в state.json как будто ничего
			// не случилось, хотя дальше молча не работал бы.
			if err := workspace.Remove(channel.ID); err != nil {
				log.Printf("удалить отозванный канал %s: %v", channel.Name, err)
			}
			mw.NotifyChannelClosed(channel, fmt.Sprintf("подписка отозвана Twitch (%s)", status))
		}
		hub.OnResubscribeFailed = func(channel domain.Channel, err error) {
			if removeErr := workspace.Remove(channel.ID); removeErr != nil {
				log.Printf("удалить канал %s после провала ресабскрайба: %v", channel.Name, removeErr)
			}
			mw.NotifyChannelClosed(channel, fmt.Sprintf("не удалось восстановить подписку: %v", err))
		}
		hub.OnConnected = func() {
			mw.SetConnectionStatus(true)
		}
		hub.OnDisconnected = func() {
			mw.SetConnectionStatus(false)
		}

		channelStore := appStore.AsChannelStore()
		workspace = app.NewChatWorkspace(hub.NewReader, helixClient, func(channels []domain.Channel) {
			// Персистентность — забота вызывающей стороны (main.go), а
			// не ChatWorkspace: он только сообщает "список изменился".
			// Ошибку сознательно не поднимаем выше — так же, как раньше
			// молчал сам ChatWorkspace: потеря сохранённого списка не
			// должна ронять операцию над чатом, которая его вызвала
			// (Add/Remove уже применились к in-memory состоянию к этому
			// моменту).
			if err := channelStore.Save(channels); err != nil {
				log.Println("сохранить список чатов:", err)
			}
		})

		if saved, err := channelStore.Load(); err != nil {
			log.Println("восстановить сохранённые чаты:", err)
		} else {
			for _, ch := range saved {
				if _, err := workspace.Add(ch); err != nil {
					log.Printf("восстановить чат %s: %v", ch.Name, err)
				}
			}
		}

		return ui.SignInResult{
			Workspace:    workspace,
			Resolve:      helixClient.GetChannelByLogin,
			Viewer:       me,
			Badges:       helixClient.ChannelBadges,
			GlobalBadges: helixClient.GlobalBadges,
		}, nil
	}

	// Аватарки и иконки бейджей — обычные публичные CDN-картинки, токен
	// не нужен, поэтому отдельный HTTP-клиент, не завязанный на сессию
	// входа (в отличие от helixClient, который появляется только внутри
	// signIn). TLS-конфигурация всё та же общая — иначе это был бы
	// уже четвёртый потребитель, заново наступающий на грабли с
	// корневыми сертификатами.
	imageClient := nettls.NewHTTPClient(nettls.DefaultTimeout)
	fetchImage := func(url string) (image.Image, error) {
		resp, err := imageClient.Get(url)
		if err != nil {
			return nil, fmt.Errorf("скачать: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("скачать: статус %d", resp.StatusCode)
		}

		img, _, err := image.Decode(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("декодировать: %v", err)
		}
		return img, nil
	}

	// loadSettings/saveSettings — обёртки над appStore.LoadSettings/
	// SaveSettings с защитой от appStore == nil (см. выше): в отличие
	// от signIn, ui.New вызывает loadSettings сразу и безусловно, ещё
	// до входа, так что там же нужна и проверка, а не только внутри
	// signIn.
	loadSettings := func() domain.Settings {
		if appStore == nil {
			return domain.DefaultSettings()
		}
		return appStore.LoadSettings()
	}
	saveSettings := func(s domain.Settings) error {
		if appStore == nil {
			return fmt.Errorf("хранилище недоступно")
		}
		return appStore.SaveSettings(s)
	}

	mw, err = ui.New(signIn, fetchImage, loadSettings, saveSettings)
	if err != nil {
		// Единственное по-настоящему фатальное состояние: если не
		// удалось создать само окно (сбой Windows API), показать
		// пользователю нечего — остаётся только лог.
		log.Println("создать окно:", err)
		return
	}

	go mw.EnsureSignedIn()

	mw.Run()
}
