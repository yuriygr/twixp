//go:build windows
// +build windows

package ui

import (
	"log"

	"github.com/lxn/walk"
)

// pendingSet — набор ключей "прямо сейчас грузится": не даёт начать
// вторую сетевую загрузку по тому же ключу, пока первая ещё не
// закончилась (см. fetchOnce). Без мьютекса — обращения только из
// UI-потока: startIfNeeded вызывается из ensureX() (сам вызывается по
// событиям UI), finish — уже внутри Synchronize в fetchOnce, тоже
// UI-поток.
type pendingSet map[string]bool

func (p pendingSet) startIfNeeded(key string) bool {
	if p[key] {
		return false
	}
	p[key] = true
	return true
}

func (p pendingSet) finish(key string) {
	delete(p, key)
}

// fetchOnce — общий скелет "сеть в фоновой горутине, применение
// результата — в UI-потоке через window.Synchronize, не начинать
// вторую загрузку по тому же ключу, пока первая не закончилась".
// Раньше был продублирован (с точностью до сетевой операции и того,
// куда класть результат) в sidebar.ensureAvatar,
// chatPane.ensureBadgeCatalog и chatPane.ensureBadgeIcon.
//
// Проверка "а нужно ли вообще грузить" (уже есть в кэше? включена ли
// эта фича?) остаётся на вызывающей стороне — она у каждого случая
// своя и не сводится к одному общему виду. fetchOnce отвечает только
// за дедупликацию по pending и за доставку результата в UI-поток.
//
// bg выполняется в фоне и либо возвращает ошибку, либо отдаёт apply —
// замыкание, которое кладёт результат туда, куда нужно (в кэш модели,
// в map с иконками и т.п.); apply вызывается уже в UI-потоке, сразу
// после снятия pending.
func fetchOnce(window *walk.MainWindow, pending pendingSet, key, errLabel string, bg func() (apply func(), err error)) {
	if !pending.startIfNeeded(key) {
		return
	}

	go func() {
		apply, err := bg()

		window.Synchronize(func() {
			pending.finish(key)

			if err != nil {
				log.Println(errLabel, err)
				return
			}
			apply()
		})
	}()
}
