//go:build windows
// +build windows

package ui

import (
	"twixp/internal/app"
	"twixp/internal/domain"
)

// Session — то, что нужно окнам после успешного входа: рабочий
// ChatWorkspace, вошедший пользователь и доступ к Twitch. Собирает её
// composition root (там же, где инфра — Helix-клиент, EventSub-хаб), UI
// сами infra-типы не видит.
//
// Компоненты (sidebar, chatPane) получают её одним вызовом setSession
// после входа, а до него хранят nil: "вошли ли" — это ровно одна
// проверка, session == nil, а не набор отдельных nil-функций.
type Session struct {
	Workspace *app.ChatWorkspace
	// Viewer — авторизованный пользователь (тот же аккаунт, что
	// отправляет сообщения): его логин/имя нужны для подсветки
	// упоминаний, ID — для запросов, привязанных к зрителю (подписки).
	Viewer domain.User
	// Twitch — все запросы к Twitch, см. app.TwitchAPI.
	Twitch app.TwitchAPI
}
