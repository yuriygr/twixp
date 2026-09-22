//go:build windows
// +build windows

package ui

import (
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/lxn/walk"
	"github.com/lxn/win"

	"twixp/internal/app"
	"twixp/internal/domain"
)

// maxHistoryLines — сколько сообщений истории держим на канал.
// historyTrimBatch — на сколько лишних сообщений даём накопиться
// сверху maxHistoryLines, прежде чем обрезать разом до maxHistoryLines.
// Без этого запаса, один раз упёршись в потолок, обрезка (а с ней —
// полный пересчёт раскладки chatView для активного канала, см.
// appendMessage) происходила бы на КАЖДОМ следующем сообщении — не то,
// чего хочется на 630МГц Celeron в шумном чате.
const (
	maxHistoryLines  = 500
	historyTrimBatch = 200
)

// chatPane — область чата справа: история сообщений и поле ввода. Не
// знает ничего про сайдбар или про то, как каналы туда попадают —
// только про то, что сейчас показано (displayedChannelID) и что нужно
// слушать в фоне (ensureWatching, вызывается снаружи). Сама отрисовка
// (цвета, перенос строк, скролл) — в chatView (chatview.go); chatPane
// отвечает только за данные: историю по каналам и её обрезку.
type chatPane struct {
	view  *chatView
	input *walk.LineEdit

	// replyBanner/replyLabel — строка над полем ввода "Ответ Х: текст"
	// с кнопкой отмены, видна только пока идёт ответ (см. startReply/
	// cancelReply). AssignTo из declarative-дерева в build().
	replyBanner *walk.Composite
	replyLabel  *walk.Label

	window *walk.MainWindow // для Synchronize из горутин; выставляется в New() после build()
	status statusReporter

	workspace *app.ChatWorkspace // nil, пока не выполнен успешный вход

	// history — накопленные строки чата по каждому каналу (по ID),
	// независимо от того, показан ли он сейчас. У каждого канала своя
	// история, а chatView просто отображает историю того канала,
	// который сейчас выбран (см. showChannel). Обрезается до
	// maxHistoryLines с запасом historyTrimBatch (см. appendMessage) —
	// иначе на слабом железе долгая сессия в шумном чате съест память
	// без ограничений.
	history map[string][]chatLine

	// watching — какие каналы (по ID) уже читаются постоянной
	// горутиной watchMessages. У каждого открытого канала свой читатель
	// на всё время его жизни, независимо от того, активен ли он сейчас
	// — иначе сообщения неактивных каналов просто некому копить в
	// history.
	watching map[string]bool

	// viewer — авторизованный пользователь. Используется только для
	// подсветки сообщений с упоминанием (см. isMentioned) — нулевое
	// значение (пустой domain.User, до успешного входа) просто никогда
	// ни с чем не совпадает.
	viewer domain.User

	// fetchIcon — общий с sidebar способ "скачать+декодировать картинку
	// по URL" (см. ui.ImageFetcher), здесь используется только для
	// иконок бейджей (см. resolveBadge/ensureBadgeIcon).
	fetchIcon ImageFetcher
	// badgeCatalog — способ узнать URL картинки бейджа для конкретного
	// канала (см. ui.BadgeCatalog). nil, пока не выполнен успешный вход
	// (см. setBadgeCatalog) — ensureBadgeCatalog в этом случае просто
	// ничего не делает, бейджи молча не подсвечиваются, а не падают.
	badgeCatalog BadgeCatalog

	// badgeURLs — кэш каталога бейджей по каждому каналу (по ID):
	// какой URL соответствует паре (set_id, id) бейджа. Тянется из
	// badgeCatalog один раз на канал (см. ensureBadgeCatalog) — Helix
	// дёргать на каждое сообщение незачем, набор бейджей канала meняется
	// не чаще, чем раз в сессию. Тут только бейджи САМОГО канала
	// (кастомные уровни подписки и т.п.) — общие для всех каналов
	// (модератор, Prime и т.п.) лежат отдельно, в globalBadges.
	badgeURLs map[string]map[domain.Badge]string
	// badgeURLsInFlight — какие каналы (по ID) прямо сейчас в процессе
	// первой загрузки каталога — тот же приём, что avatarsInFlight в
	// sidebar.go: см. pendingSet/fetchOnce в asyncfetch.go. Без него
	// повторный reload успел бы запустить вторую параллельную загрузку
	// одного и того же каталога.
	badgeURLsInFlight pendingSet

	// globalBadges — общий для всех каналов каталог (модератор, Prime,
	// турбо и т.п. — одна и та же картинка везде), загружается один раз
	// за сессию сразу после входа (см. setGlobalBadgeCatalog), а не
	// лениво по требованию — раз он всё равно понадобится почти сразу
	// на любом канале, разумно не ждать первого повода. nil, пока не
	// подтянулся (или пока не выполнен вход) — badgeImageURL в этом
	// случае просто ищет только по каталогу канала.
	globalBadges map[domain.Badge]string

	// badgeIcons — кэш уже скачанных иконок бейджей, ключ — URL
	// картинки, а НЕ пара (channelID, Badge): глобальные бейджи
	// (модератор, Prime и т.п.) — одна и та же картинка на всех
	// каналах, кэш по URL не тянет её заново для второго открытого
	// чата.
	badgeIcons map[string]*walk.Bitmap
	// badgeIconsInFlight — тот же приём, что badgeURLsInFlight, только
	// для отдельных иконок, а не каталога целиком.
	badgeIconsInFlight pendingSet

	// displayedChannelID — какой канал сейчас реально показан в view.
	// Единственный источник правды об этом (раньше appendMessage
	// параллельно спрашивал ещё и workspace.Active() — см. forget:
	// после ChatWorkspace.Remove тот уже не знает про удалённый канал,
	// а chatPane в этот момент ещё должен решить, чистить ли видимую
	// область).
	displayedChannelID string

	// replyTo — сообщение, на которое отвечаем следующей отправкой, из
	// контекстного меню chatView ("Ответить"). nil — обычная отправка.
	// Сбрасывается после отправки, явной отмены баннера и при
	// переключении на другой канал (см. showChannel) — reply на чужой
	// канал был бы невалидным запросом к Twitch, а не просто путаницей.
	replyTo *chatLine

	// chatters — по каждому каналу (по ID) те, кто уже успел написать
	// хотя бы одно сообщение за эту сессию (по ID автора, чтобы не
	// плодить дубликатов при смене регистра или ника). Источник для
	// автодополнения "@..." (см. onInputTextChanged/matchChatters) —
	// не настоящий список зрителей канала (Twitch отдаёт его только
	// модератору/вещателю через отдельный scope, см. историю
	// обсуждения), а те, кого мы САМИ уже видели говорящими. Копится с
	// нуля на каждый запуск приложения, не персистится.
	chatters map[string]map[string]domain.User

	// mentionPopup — всплывающий список подсказок для "@..." (см.
	// mentionpopup.go). Создаётся один раз в attachMentionPopup, когда
	// уже есть настоящий hwnd главного окна — до этого момента (и если
	// создание почему-то не удалось) nil, и onInputTextChanged просто
	// ничего не показывает, не падая.
	mentionPopup *mentionPopup

	// mentionStart — позиция символа "@" текущего набираемого
	// упоминания в тексте поля ввода (в рунах, не в байтах — см.
	// onInputTextChanged), пока mentionPopup открыт. -1 — упоминание
	// сейчас не набирается. Нужна в commitMention, чтобы знать, что
	// именно (от "@" до курсора) заменить на выбранный вариант.
	mentionStart int

	// mentionAutocomplete — переключатель из настроек (см.
	// domain.Settings.MentionAutocomplete). true по умолчанию
	// (newChatPane) — до входа, пока applySettings ещё не подъехал с
	// настоящим значением, автодополнение ведёт себя так же, как и
	// всегда раньше.
	mentionAutocomplete bool
}

func newChatPane(status statusReporter, fetchIcon ImageFetcher) *chatPane {
	p := &chatPane{
		status:              status,
		fetchIcon:           fetchIcon,
		history:             make(map[string][]chatLine),
		watching:            make(map[string]bool),
		badgeURLs:           make(map[string]map[domain.Badge]string),
		badgeURLsInFlight:   make(pendingSet),
		badgeIcons:          make(map[string]*walk.Bitmap),
		badgeIconsInFlight:  make(pendingSet),
		chatters:            make(map[string]map[string]domain.User),
		mentionStart:        -1,
		mentionAutocomplete: true,
	}
	p.view = newChatView(p.startReply, p.resolveBadge)
	return p
}

// attachMentionPopup создаёт попап автодополнения "@..." — вызывать
// нужно только после того, как у главного окна появился настоящий hwnd
// (см. chatpage.go, там же, где chatPane.view.attach()), owner
// раньше этого момента просто не существует.
func (p *chatPane) attachMentionPopup(owner win.HWND) {
	p.mentionPopup = newMentionPopup(owner)
}

// setWorkspace подключает chatPane к рабочей сессии сразу после
// успешного входа.
func (p *chatPane) setWorkspace(ws *app.ChatWorkspace) {
	p.workspace = ws
}

// setViewer запоминает авторизованного пользователя — нужен только для
// подсветки сообщений с упоминанием (см. isMentioned/appendMessage).
func (p *chatPane) setViewer(viewer domain.User) {
	p.viewer = viewer
}

// setBadgeCatalog подключает способ узнавать бейджи канала сразу после
// успешного входа — до этого момента (и если вход ещё не произошёл)
// ensureBadgeCatalog просто ничего не делает.
func (p *chatPane) setBadgeCatalog(catalog BadgeCatalog) {
	p.badgeCatalog = catalog
}

// setGlobalBadgeCatalog запускает разовую загрузку глобального каталога
// бейджей сразу после успешного входа — не откладывая до первого
// сообщения с бейджем: раз он один на всю сессию и понадобится почти
// сразу на любом канале, разумно стартовать загрузку сейчас, а не ждать
// повода (в отличие от каталога КОНКРЕТНОГО канала, см.
// ensureBadgeCatalog — там ожидание оправдано, потому что каналов может
// быть много и не факт, что все будут открыты).
func (p *chatPane) setGlobalBadgeCatalog(catalog GlobalBadgeCatalog) {
	if catalog == nil {
		return
	}

	go func() {
		badges, err := catalog()

		p.window.Synchronize(func() {
			if err != nil {
				log.Println("глобальные бейджи:", err)
				return
			}
			p.globalBadges = badges

			// Сообщения с глобальными бейджами могли уже отрисоваться
			// без них — пересчитываем показанный сейчас канал (тот же
			// приём, что и в ensureBadgeCatalog/ensureBadgeIcon).
			// false — не прыгать в низ, если читают историю выше (см.
			// chatView.setLines).
			if p.displayedChannelID != "" {
				p.view.setLines(p.history[p.displayedChannelID], false)
			}
		})
	}()
}

// showChannel показывает накопленную историю канала — иначе после
// переключения на давно не смотренный канал пользователь упирался бы
// в самое начало истории, а не в свежие сообщения (setLines сама
// прокручивает к низу). Сбрасывает незавершённый ответ (баннер
// "Ответить") — reply_parent_message_id привязан к конкретному
// каналу (сообщение оттуда), и отправка такого ответа уже в другой,
// открытый позже канал была бы невалидным запросом к Twitch, а не
// просто путаницей в интерфейсе.
func (p *chatPane) showChannel(channelID string) {
	p.cancelReply()
	p.closeMentionPopup()
	p.displayedChannelID = channelID
	// true — принудительно в низ: scrollTop сейчас ещё от ДРУГОГО,
	// прежде показанного канала, сохранять его тут было бы бессмысленно
	// (см. chatView.setLines).
	p.view.setLines(p.history[channelID], true)
}

// forget вычищает состояние канала, который только что удалили из
// workspace (см. sidebar.onDeleteChannelClicked). watching чистить не
// нужно: как только workspace.Remove закроет ChatService, канал
// Messages() у watchMessages сам закроется, и она удалит себя из
// watching (см. watchMessages) — но history может годами копиться в
// памяти, если не почистить явно, а видимая область — просто
// продолжит показывать текст уже несуществующего канала, если это
// был именно тот, что сейчас открыт.
func (p *chatPane) forget(channelID string) {
	delete(p.history, channelID)
	delete(p.badgeURLs, channelID)
	delete(p.chatters, channelID)

	if p.displayedChannelID == channelID {
		p.displayedChannelID = ""
		p.view.setLines(nil, true) // true тут значения не имеет (nil-история и так пуста), но для единообразия с showChannel
	}
}

// ensureWatching запускает постоянного читателя сообщений канала,
// если он ещё не запущен, и заодно (см. ensureBadgeCatalog) убеждается,
// что каталог бейджей канала загружен или уже грузится — вызывать
// нужно для КАЖДОГО открытого канала (см. sidebar.reload), а не только
// при выборе в сайдбаре.
func (p *chatPane) ensureWatching(channel domain.Channel) {
	p.ensureBadgeCatalog(channel)

	if p.watching[channel.ID] {
		return
	}
	_, service, ok := p.workspace.Get(channel.ID)
	if !ok {
		return
	}

	p.watching[channel.ID] = true
	go p.watchMessages(channel.ID, service)
}

// ensureBadgeCatalog запускает загрузку каталога бейджей канала, если
// его ещё нет в кэше и он прямо сейчас не грузится — тот же приём, что
// sidebar.ensureAvatar для аватарок: сеть в отдельной горутине,
// применение — через Synchronize (см. fetchOnce в asyncfetch.go).
func (p *chatPane) ensureBadgeCatalog(channel domain.Channel) {
	if p.badgeCatalog == nil {
		return
	}
	if _, ok := p.badgeURLs[channel.ID]; ok {
		return
	}

	fetchOnce(p.window, p.badgeURLsInFlight, channel.ID,
		fmt.Sprintf("бейджи канала %s:", channel.Name),
		func() (apply func(), err error) {
			catalog, err := p.badgeCatalog(channel)
			if err != nil {
				return nil, err
			}

			return func() {
				p.badgeURLs[channel.ID] = catalog

				// Сообщения могли прийти и уже отрисоваться раньше, чем
				// подтянулся каталог этого канала — без пересчёта их
				// бейджи так и остались бы пустыми местами до следующего
				// переключения на этот чат. Если он показан прямо
				// сейчас — пересчитываем; неактивные каналы просто
				// досчитаются сами в showChannel/setLines при следующем
				// выборе.
				if p.displayedChannelID == channel.ID {
					p.recomputeDisplayedChannel()
				}
			}, nil
		})
}

// recomputeDisplayedChannel пересчитывает раскладку показанного сейчас
// канала без прыжка скролла вниз — false у chatView.setLines. Общий
// хвост у ensureBadgeCatalog/ensureBadgeIcon: оба догружают что-то
// асинхронно уже после того, как соответствующие строки могли успеть
// отрисоваться без этого (бейдж/иконка — пустым местом до следующего
// сообщения). Именно false и есть причина, по которой у setLines
// вообще появился этот параметр: иконки бейджей догружаются часто, и
// раньше каждая такая догрузка выдёргивала бы читающего историю
// пользователя вниз.
func (p *chatPane) recomputeDisplayedChannel() {
	if p.displayedChannelID == "" {
		return
	}
	p.view.setLines(p.history[p.displayedChannelID], false)
}

// watchMessages читает сообщения одного канала на всё время его жизни
// (пока Messages() не закроется — уход канала из workspace или
// отзыв/провал ресабскрайба на стороне Hub'а закрывают его сами). Не
// привязан к тому, активен ли канал сейчас — история копится в фоне
// для всех открытых каналов одновременно, иначе сообщения неактивных
// чатов просто некому было бы читать вообще.
// watchMessages — единственная фоновая горутина на канал, читающая ОБА
// потока сразу (обычные сообщения и удаления, см. app.ChatReader) —
// select между двумя каналами, а не две отдельные горутины: тот же
// принцип "одна горутина на подписку", что и раньше, просто теперь
// подписок для одного канала физически три (см. eventsub.Hub.subscribe),
// а горутина-читатель на стороне UI как была одна, так и осталась.
//
// messages/deletions поочерёдно зануляются при закрытии — цикл
// продолжается, пока жив хотя бы один из двух (на практике оба
// закрываются одновременно в Hub.dropChannel, но раздельная проверка
// не помешает, если это когда-нибудь перестанет быть так).
func (p *chatPane) watchMessages(channelID string, service *app.ChatService) {
	messages := service.Messages()
	deletions := service.Deletions()

	for messages != nil || deletions != nil {
		select {
		case msg, ok := <-messages:
			if !ok {
				messages = nil
				continue
			}
			p.appendMessage(channelID, msg)
		case del, ok := <-deletions:
			if !ok {
				deletions = nil
				continue
			}
			p.deleteMessage(channelID, del.MessageID)
		}
	}

	p.window.Synchronize(func() {
		// Канал закрылся (отозван/не пересоздался) — освобождаем флаг:
		// если пользователь позже откроет тот же логин заново, это
		// будет уже другой ChatService с чистой подпиской, и для него
		// нужен новый читатель, а не "уже же смотрим" из прошлого раза.
		delete(p.watching, channelID)
	})
}

// deleteMessage помечает сообщение с данным ID как удалённое (см.
// domain.MessageDeletion) — ищет его в уже накопленной истории канала
// и, если этот канал сейчас показан, отражает изменение в chatView.
// Тихо ничего не делает, если сообщение не нашлось — например, его уже
// успели обрезать из истории (historyTrimBatch, см. appendMessage) —
// это не ошибка, просто нечего красить.
func (p *chatPane) deleteMessage(channelID, messageID string) {
	p.window.Synchronize(func() {
		lines, ok := p.history[channelID]
		if !ok {
			return
		}

		found := false
		for i := range lines {
			if lines[i].MessageID == messageID {
				lines[i].Deleted = true
				found = true
				break
			}
		}
		if !found || p.displayedChannelID != channelID {
			return
		}

		// false — не прыгать в низ: удалённое сообщение может быть
		// где угодно в истории, а не обязательно в самом конце (см.
		// chatView.setLines).
		p.view.setLines(lines, false)
	})
}

// appendMessage копит сообщение в истории канала (обрезая её раз в
// historyTrimBatch сообщений — см. константы) и, если этот канал
// сейчас показан, отражает изменение в chatView: при обрезке —
// пересчёт раскладки целиком (setLines), иначе — обычное appendLine.
func (p *chatPane) appendMessage(channelID string, msg domain.ChatMessage) {
	// isMentioned — от ОРИГИНАЛЬНОГО текста, до обрезки ведущего
	// "@Автор" ниже: если сообщение отвечает именно viewer'у, этот же
	// "@viewer" в начале — самое настоящее упоминание, и оно не должно
	// пропасть просто из-за того, что мы прячем его из отображаемого
	// текста (см. stripReplyMentionPrefix).
	mentioned := domain.IsMentioned(msg.Text, p.viewer)

	nc := domain.NicknameColor(msg.Author.ID, msg.Author.Login, msg.Author.Color)

	line := chatLine{
		Time:          msg.SentAt,
		Author:        msg.Author.DisplayName,
		MessageID:     msg.ID,
		Color:         walk.RGB(nc.R, nc.G, nc.B),
		Text:          domain.StripReplyMentionPrefix(msg.Text, msg.ReplyTo),
		Mentioned:     mentioned,
		Badges:        msg.Badges,
		ReplyTo:       msg.ReplyTo,
		Highlighted:   msg.Highlighted,
		SystemMessage: msg.SystemMessage,
	}

	p.window.Synchronize(func() {
		// trackChatter — здесь, а не раньше, до Synchronize: watchMessages
		// вызывает appendMessage из фоновой горутины чтения, а chatters —
		// обычная map без своей блокировки, как и все остальные поля
		// chatPane (история, кэш бейджей и т.п.) — вся мутация состояния
		// нарочно стянута в UI-поток через Synchronize, а не разбросана
		// по горутинам разных каналов вперемешку.
		p.trackChatter(channelID, msg.Author)

		lines := append(p.history[channelID], line)
		trimmed := false
		if len(lines) > maxHistoryLines+historyTrimBatch {
			lines = lines[len(lines)-maxHistoryLines:]
			trimmed = true
		}
		p.history[channelID] = lines

		if p.displayedChannelID != channelID {
			return
		}

		if trimmed {
			// false — обрезка истории не повод прыгать в низ, если
			// пользователь читает её выше (см. chatView.setLines).
			p.view.setLines(lines, false)
		} else {
			p.view.appendLine(line)
		}
	})
}

// trackChatter запоminает автора сообщения как "уже писал в этом
// канале" — источник для автодополнения "@..." (см. matchChatters).
// По ID автора, а не по логину: ID не меняется, даже если человек
// сменит ник, а перезаписывать значение при каждом сообщении всё равно
// дешевле, чем проверять "а не изменилось ли".
func (p *chatPane) trackChatter(channelID string, author domain.User) {
	if author.ID == "" {
		return
	}

	authors, ok := p.chatters[channelID]
	if !ok {
		authors = make(map[string]domain.User)
		p.chatters[channelID] = authors
	}
	authors[author.ID] = author
}

// resolveBadge — коллбэк для chatView.paint: отдаёт уже скачанную
// иконку бейджа, если она есть. Должен быть дешёвым и никогда не
// блокировать (сама загрузка при необходимости уходит в фоновую
// горутину, см. ensureBadgeIcon) — вызывается прямо из отрисовки.
func (p *chatPane) resolveBadge(badge domain.Badge) *walk.Bitmap {
	imageURL := p.badgeImageURL(badge)
	if imageURL == "" {
		return nil
	}

	if icon, ok := p.badgeIcons[imageURL]; ok {
		return icon
	}

	p.ensureBadgeIcon(imageURL)
	return nil
}

// badgeImageURL ищет URL картинки бейджа: сперва в каталоге ТЕКУЩЕГО
// показанного канала (кастомные уровни подписки и бейджи за биты — у
// каждого канала свои), и только если там пусто — в глобальном
// каталоге (модератор, Prime, турбо и т.п. — один на все каналы). Тот
// же порядок приоритета, что и в вебе Twitch: свой канал важнее общего
// набора для одинаковых имён бейджей.
//
// Каталог канала берём для p.displayedChannelID, а не для канала
// конкретной строки: chatView в любой момент показывает историю только
// ОДНОГО канала (см. showChannel/forget) — то же самое, что уже
// показано, дополнительно протаскивать channelID через
// chatLine/chatView ради этого не нужно.
func (p *chatPane) badgeImageURL(badge domain.Badge) string {
	if catalog, ok := p.badgeURLs[p.displayedChannelID]; ok {
		if url, ok := catalog[badge]; ok && url != "" {
			return url
		}
	}
	if url, ok := p.globalBadges[badge]; ok {
		return url
	}
	return ""
}

// ensureBadgeIcon запускает загрузку иконки бейджа по URL, если она ещё
// ensureBadgeIcon запускает загрузку иконки бейджа по URL, если она ещё
// не скачана и прямо сейчас не грузится — тот же приём, что
// sidebar.ensureAvatar. Кэш общий на все каналы (см. badgeIcons) —
// одна и та же глобальная иконка не тянется по новой для каждого
// открытого чата.
func (p *chatPane) ensureBadgeIcon(imageURL string) {
	if p.fetchIcon == nil {
		return
	}

	fetchOnce(p.window, p.badgeIconsInFlight, imageURL, "иконка бейджа:",
		func() (apply func(), err error) {
			img, err := p.fetchIcon(imageURL)
			if err != nil {
				return nil, err
			}

			// walk.NewBitmapFromImage — GDI-вызов: как и в оригинале до
			// рефакторинга, оставляем его внутри apply (UI-поток), а не
			// здесь, в фоновой горутине.
			return func() {
				icon, err := walk.NewBitmapFromImage(img)
				if err != nil {
					log.Println("иконка бейджа:", err)
					return
				}
				p.badgeIcons[imageURL] = icon

				// Строка, из-за которой запустилась эта загрузка, уже
				// могла отрисоваться без иконки (resolveBadge вернул nil
				// выше) — пересчитываем показанный сейчас канал, чтобы
				// бейдж не провисел пустым местом до следующего
				// сообщения (см. recomputeDisplayedChannel).
				p.recomputeDisplayedChannel()
			}, nil
		})
}

// startReply включает режим ответа на сообщение — коллбэк из
// контекстного меню chatView ("Ответить"). Показывает баннер над
// полем ввода и переносит туда фокус, чтобы можно было сразу печатать.
func (p *chatPane) startReply(line chatLine) {
	l := line
	p.replyTo = &l

	p.replyLabel.SetText(fmt.Sprintf("Ответ %s: %s", line.Author, domain.TruncateRunes(line.Text, 60)))
	p.replyBanner.SetVisible(true)
	p.input.SetFocus()
}

func (p *chatPane) cancelReply() {
	p.replyTo = nil
	p.replyBanner.SetVisible(false)
}

func (p *chatPane) onCancelReplyClicked() {
	p.cancelReply()
}

// onSendClicked/onInputKeyDown — отправка сообщения. Send — тоже
// блокирующий HTTP-вызов к Helix, поэтому тоже в горутине; поле ввода
// очищаем сразу (оптимистично), ошибку, если она случится, показываем
// в статус-баре, не пытаясь вернуть текст обратно в поле — для первой
// версии каркаса этого достаточно.
func (p *chatPane) onSendClicked() {
	p.sendCurrentInput()
}

// onInputKeyDown обрабатывает Enter (отправка) и, пока открыт попап
// автодополнения "@..." (см. mentionPopup), ↑/↓ (перемещение по
// списку), Enter (вставить выбранное — тогда НЕ отправляет сообщение)
// и Esc (закрыть без вставки). Все эти клавиши в обычном
// однострочном LineEdit сами по себе ничего не делают (кроме Enter,
// который мы уже перехватываем под отправку) — перехват им не мешает.
func (p *chatPane) onInputKeyDown(key walk.Key) {
	if p.mentionPopup != nil && p.mentionPopup.visible() {
		switch key {
		case walk.KeyUp:
			p.mentionPopup.move(-1)
			return
		case walk.KeyDown:
			p.mentionPopup.move(1)
			return
		case walk.KeyReturn:
			p.mentionPopup.commitSelected()
			return
		case walk.KeyEscape:
			p.closeMentionPopup()
			return
		}
	}

	if key == walk.KeyReturn {
		p.sendCurrentInput()
	}
}

// setMentionAutocomplete включает/выключает автодополнение "@..." (см.
// domain.Settings.MentionAutocomplete). При выключении на всякий
// случай закрывает уже открытый попап — иначе он мог бы остаться
// висеть до следующего изменения текста.
func (p *chatPane) setMentionAutocomplete(enabled bool) {
	p.mentionAutocomplete = enabled
	if !enabled {
		p.closeMentionPopup()
	}
}

// onInputTextChanged ищет незакрытое "@..." перед курсором при каждом
// изменении текста поля ввода и показывает/обновляет/закрывает попап
// автодополнения соответственно. Не отслеживает перемещение курсора
// БЕЗ изменения текста (клик мышью, стрелки влево/вправо) — если
// пользователь просто кликнул в другое место строки, уже открытый
// попап может на мгновение остаться привязан к прежней позиции; это
// сознательное упрощение, а не недосмотр — закрывается он всё равно
// при следующем же нажатии клавиши, меняющей текст.
func (p *chatPane) onInputTextChanged() {
	if p.mentionPopup == nil || !p.mentionAutocomplete {
		return
	}

	text := []rune(p.input.Text())
	caret, _ := p.input.TextSelection()

	token, start, ok := domain.MentionTokenBefore(text, caret)
	if !ok {
		p.closeMentionPopup()
		return
	}

	channel, _, ok := p.workspace.Active()
	if !ok {
		p.closeMentionPopup()
		return
	}

	matches := p.matchChatters(channel, token)
	if len(matches) == 0 {
		p.closeMentionPopup()
		return
	}

	p.mentionStart = start
	p.mentionPopup.show(p.input.Handle(), matches, p.commitMention)
}

// matchChatters отдаёт до maxMentionMatches подходящих под token
// (без учёта регистра, по префиксу логина) вариантов для автодополнения
// "@...". Источник — chatters канала (кто уже писал в этом чате, см.
// trackChatter) плюс сам вещатель канала — его добавляем отдельно,
// поскольку он вполне может ни разу не написать в собственном чате и
// тогда не попал бы в chatters, а упомянуть его всё равно хотят
// (ровно так же ведёт себя веб-версия Twitch).
//
// При ПУСТОМ token (только что напечатали "@", ничего после) вещатель
// идёт первым — опять же как в вебе. При непустом — обычная
// алфавитная сортировка по логину, без искусственного приоритета: раз
// пользователь уже что-то печатает, кого показывать первым решает
// совпадение, а не то, кто из них вещатель.
func (p *chatPane) matchChatters(channel domain.Channel, token string) []domain.User {
	broadcaster := domain.User{ID: channel.ID, Login: channel.Name, DisplayName: channel.DisplayName}

	candidates := map[string]domain.User{broadcaster.ID: broadcaster}
	for id, u := range p.chatters[channel.ID] {
		candidates[id] = u
	}

	token = strings.ToLower(token)

	var matches []domain.User
	for _, u := range candidates {
		if strings.HasPrefix(strings.ToLower(u.Login), token) {
			matches = append(matches, u)
		}
	}

	sort.Slice(matches, func(i, j int) bool {
		if token == "" {
			// Вещатель — первый, остальные — по алфавиту после него.
			iBroadcaster := matches[i].ID == broadcaster.ID
			jBroadcaster := matches[j].ID == broadcaster.ID
			if iBroadcaster != jBroadcaster {
				return iBroadcaster
			}
		}
		return strings.ToLower(matches[i].Login) < strings.ToLower(matches[j].Login)
	})

	const maxMentionMatches = 8
	if len(matches) > maxMentionMatches {
		matches = matches[:maxMentionMatches]
	}

	return matches
}

// commitMention заменяет "@token" (от mentionStart до текущего курсора)
// на "@Логин " выбранного пользователя и закрывает попап. Логин, а не
// отображаемое имя — так оформляет упоминания сам Twitch, и это же
// сравнивает isMentioned.
func (p *chatPane) commitMention(user domain.User) {
	text := []rune(p.input.Text())
	caret, _ := p.input.TextSelection()

	if p.mentionStart < 0 || p.mentionStart > len(text) || caret > len(text) || caret < p.mentionStart {
		p.closeMentionPopup()
		return
	}

	replacement := []rune("@" + user.Login + " ")

	newText := make([]rune, 0, len(text)-(caret-p.mentionStart)+len(replacement))
	newText = append(newText, text[:p.mentionStart]...)
	newText = append(newText, replacement...)
	newText = append(newText, text[caret:]...)
	newCaret := p.mentionStart + len(replacement)

	p.closeMentionPopup()
	p.input.SetText(string(newText))
	p.input.SetTextSelection(newCaret, newCaret)
	p.input.SetFocus()
}

// closeMentionPopup прячет попап и сбрасывает mentionStart — безопасно
// вызывать в любой момент, даже если попап и так уже закрыт.
func (p *chatPane) closeMentionPopup() {
	if p.mentionPopup != nil {
		p.mentionPopup.hide()
	}
	p.mentionStart = -1
}

func (p *chatPane) sendCurrentInput() {
	text := strings.TrimSpace(p.input.Text())
	if text == "" {
		return
	}

	_, service, ok := p.workspace.Active()
	if !ok {
		p.status.logAndShowError(fmt.Errorf("нет активного чата"))
		return
	}

	var replyToMessageID string
	// originalReplyTo — на случай отказа (см. ниже): нужно не только
	// ID для запроса, но и вся строка целиком, чтобы вернуть баннер
	// "Ответ Автору: ..." ровно таким же, каким он был до отправки.
	var originalReplyTo *chatLine
	if p.replyTo != nil {
		replyToMessageID = p.replyTo.MessageID
		l := *p.replyTo
		originalReplyTo = &l
	}

	p.closeMentionPopup()
	p.input.SetText("")
	p.cancelReply()

	go func() {
		err := service.Send(text, replyToMessageID)

		p.window.Synchronize(func() {
			if err == nil {
				return
			}

			p.status.logAndShowError(err)

			// Twitch мог отклонить отправку уже ПОСЛЕ того, как мы
			// оптимистично очистили поле и баннер ответа (бан, таймаут,
			// режим для подписчиков, AutoMod и т.п. — см.
			// helix.Client.Send) — заставлять печатать всё заново было бы
			// недружелюбно. Восстанавливаем, только если поле всё ещё
			// пустое: если пользователь уже начал печатать что-то новое,
			// пока ответ летел туда-обратно, затирать это не нужно.
			if p.input.Text() != "" {
				return
			}

			p.input.SetText(text)
			textLen := len([]rune(text))
			p.input.SetTextSelection(textLen, textLen)

			if originalReplyTo != nil {
				p.startReply(*originalReplyTo)
			}
		})
	}()
}
