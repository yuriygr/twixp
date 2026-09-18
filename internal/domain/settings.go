package domain

// Settings — пользовательские настройки приложения, персистируемые в
// Store между запусками (см. infra/store). Плоская структура из
// простых переключателей — по смыслу это скорее принадлежность UI, но
// живёт в domain по той же причине, что и Token/Channel: и infra/store
// (персистирует на диск), и ui (применяет к виджетам и читает для
// страницы настроек) должны видеть один и тот же тип, не создавая цикл
// ui -> store -> domain -> ui.
type Settings struct {
	// ShowTimestamps — показывать ли время отправки у каждого
	// сообщения в чате.
	ShowTimestamps bool
	// ShowBadges — показывать ли иконки бейджей (модератор, VIP,
	// подписчик и т.п.) у сообщений.
	ShowBadges bool
	// HighlightMentions — подсвечивать ли сообщения с упоминанием
	// текущего пользователя едва красным фоном.
	HighlightMentions bool
	// ShowChannelAvatars — показывать ли аватарки каналов в списке
	// слева.
	ShowChannelAvatars bool
	// MentionAutocomplete — предлагать ли автодополнение "@..." при
	// наборе сообщения.
	MentionAutocomplete bool
	// FontSize — размер шрифта чата в pt. 0 — использовать системный
	// шрифт по умолчанию (MS Shell Dlg 2, 8pt на большинстве систем),
	// а не хранить конкретное число специально под него: если однажды
	// сменится системный шрифт по умолчанию, значение 0 продолжит
	// означать "как у системы", а не "как было на момент установки".
	FontSize int
	// AlwaysOnTop — держать окно поверх остальных.
	AlwaysOnTop bool
}

// DefaultSettings — то, что действует при самом первом запуске, пока
// пользователь ничего не настраивал (Store.LoadSettings отдаёт именно
// это, если ключа ещё нет в state.json). Все переключатели включены —
// ровно то поведение, что было в приложении ДО появления страницы
// настроек, поэтому обновление с прежней версии не меняет вид чата
// незаметно для пользователя.
func DefaultSettings() Settings {
	return Settings{
		ShowTimestamps:      true,
		ShowBadges:          true,
		HighlightMentions:   true,
		ShowChannelAvatars:  true,
		MentionAutocomplete: true,
		FontSize:            0,
		AlwaysOnTop:         false,
	}
}
