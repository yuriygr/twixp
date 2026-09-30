// Package appdir отвечает за один вопрос: где на диске приложение
// хранит свои данные (настройки, токен, лог). Никакой записи и
// чтения файлов здесь нет — только вычисление путей.
package appdir

import (
	"os"
	"path/filepath"
)

// Dir возвращает каталог данных текущего пользователя:
//
//	Windows XP:    C:\Documents and Settings\<пользователь>\Application Data\TwiXP
//	Vista и новее: C:\Users\<пользователь>\AppData\Roaming\TwiXP
//
// Это штатное место для настроек приложений (а не "Мои документы",
// куда пользователь складывает свои файлы). Каталог здесь не
// создаётся — это забота Store/applog при первой записи.
//
// Если %APPDATA% не задана (нетипичная среда — например, запуск из
// служебной сессии), пробуем %USERPROFILE%\Application Data, а если и
// его нет — откатываемся на ./data рядом с рабочим каталогом, как
// было до переезда: лучше работать по-старому, чем не работать.
func Dir() string {
	if base := os.Getenv("APPDATA"); base != "" {
		return filepath.Join(base, "TwiXP")
	}
	if profile := os.Getenv("USERPROFILE"); profile != "" {
		return filepath.Join(profile, "Application Data", "TwiXP")
	}
	return "./data"
}

// LegacyDirs возвращает каталоги, где старые версии приложения могли
// оставить данные: data рядом с exe и data в рабочем каталоге (раньше
// путь был относительным — "./data", то есть зависел от того, откуда
// запущен exe, поэтому проверяем оба). Используется только для
// одноразовой миграции, см. store.New.
func LegacyDirs() []string {
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Join(filepath.Dir(exe), "data"))
	}
	dirs = append(dirs, "./data")
	return dirs
}
