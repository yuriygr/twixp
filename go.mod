module twitchclient

go 1.11

require (
	github.com/akavel/rsrc v0.10.2 // indirect
	github.com/lxn/walk v0.0.0-00010101000000-000000000000
	github.com/lxn/win v0.0.0-00010101000000-000000000000
	golang.org/x/sys v0.0.0-00010101000000-000000000000
	gopkg.in/Knetic/govaluate.v3 v3.0.0-00010101000000-000000000000
)

// Все четыре зависимости — GOPATH-стиль без версий и без публикации
// в модульном реестре. replace указывает gopls (и любой современный
// go build/vet, если вдруг случайно запустите его отсюда) искать код
// локально, там же, где лежат наши git-чекауты на нужных коммитах.
//
// go1.10.8 при сборке под XP (см. Makefile, GO111MODULE=off) этот файл
// целиком игнорирует — резолвит зависимости по-старому через GOPATH.
replace (
	github.com/lxn/walk => ../github.com/lxn/walk
	github.com/lxn/win => ../github.com/lxn/win
	golang.org/x/sys => ../golang.org/x/sys
	gopkg.in/Knetic/govaluate.v3 => ../gopkg.in/Knetic/govaluate.v3
)
