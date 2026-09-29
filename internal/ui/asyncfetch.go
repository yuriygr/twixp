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
//
// window == nil — не паникуем, просто ничего не грузим: fetchOnce
// вызывается из мест, куда теоретически можно попасть до того, как
// window вообще выставлен (см. sidebar/chatPane, поле window
// выставляется в MainWindow.New уже после конструктора) — сам
// вызывающий код сейчас гарантирует обратное своим порядком
// инициализации, но дублировать эту гарантию тут дешевле, чем полагаться
// на то, что её никто никогда не нарушит.
func fetchOnce(window *walk.MainWindow, pending pendingSet, key, errLabel string, bg func() (apply func(), err error)) {
	if window == nil {
		return
	}
	if !pending.startIfNeeded(key) {
		return
	}

	go func() {
		// bg — это сеть (fetchAvatar/fetchIcon), декодирование картинки
		// и, у некоторых вызывающих, создание walk.Bitmap — на любом из
		// этих шагов паника не то чтобы невозможна. Без recover она
		// была бы НЕ "ключ навсегда останется в pending" (как могло бы
		// показаться) — непойманная паника в горутине останавливает
		// весь процесс целиком, это обычное поведение Go, а не
		// особенность конкретно этого места. pending.finish(key) в
		// recover — уже скорее для порядка (после паники здесь всё
		// равно ничего не восстановить), чем реальная защита: настоящая
		// защита — это то, что процесс переживёт сетевую ошибку при
		// загрузке одной аватарки, а не упадёт целиком.
		defer func() {
			if r := recover(); r != nil {
				log.Println(errLabel, "паника:", r)
				window.Synchronize(func() {
					pending.finish(key)
				})
			}
		}()

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
