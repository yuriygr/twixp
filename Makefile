# TwiXP — Twitch-чат клиент для Windows XP.
#
# Единственная цель сборки в этом репозитории — GUI-приложение под
# Windows (терминальный клиент выделен в отдельный форк). Из-за
# internal/ui (lxn/walk) main.go и config.go собираются только под
# GOOS=windows и требуют отдельный GOPATH с запиненными до 24.08.2018
# версиями lxn/walk/lxn/win/govaluate.v3, GO111MODULE=off — см.
# контекст проекта. Ядро (domain/app/infra) от этого не зависит и
# продолжает жить на go.mod + современном Go, поэтому vet-core работает
# без всякого кросс-компильного антуража.
#
# GO_XP указывает на настоящий Go 1.10.8 для финальной сборки — тот же
# способ его добыть, что и в CI (.github/workflows/release.yml):
#   go install golang.org/dl/go1.10.8@latest && go1.10.8 download
# По умолчанию ожидается в PATH под этим именем; свой путь — через
# `make build GO_XP=/путь/до/go1.10.8`. Если такого бинарника ещё нет
# под рукой, build упадёт с понятной подсказкой — check тем временем
# даёт быструю проверку компиляции текущим (современным) Go, без него.

GO        ?= go
GO_XP     ?= go1.10.8
BIN_DIR   ?= bin
GOPATH_XP ?= $(CURDIR)/.gopath-xp

# Коммиты — "канун 24.08.2018": последние перед тем, как lxn/walk начал
# использовать strings.ReplaceAll (Go 1.12), несовместимое с целевым
# Go 1.10.8.
WALK_COMMIT = 1afcf534b52d51572124f079c8fb53b6d5601b0e
WIN_COMMIT  = 785c4956069227e430929ac27bfa26af2ff25dfb

.PHONY: help deps check build clean vet-core fmt test rsrc

help:
	@echo "Ядро (domain/app/infra, текущий Go, без GOPATH):"
	@echo "  make vet-core   — go vet по domain/app/infra"
	@echo "  make test       — go test по domain/app/infra (то же ядро, что и vet-core)"
	@echo "  make fmt        — список неотформатированных файлов во всём репозитории"
	@echo "  make rsrc       — сборка манифеста"
	@echo ""
	@echo "TwiXP (main.go + internal/ui, lxn/walk):"
	@echo "  make deps       — разово стянуть запиненные lxn/walk/lxn/win/govaluate.v3"
	@echo "  make check      — быстрая проверка компиляции текущим Go (без GO_XP)"
	@echo "  make build      — финальная сборка GO_XP=$(GO_XP) в $(BIN_DIR)/twixp.exe"
	@echo "  make clean      — убрать $(BIN_DIR) и $(GOPATH_XP)"

# --- Ядро --------------------------------------------------------------

vet-core:
	$(GO) vet ./internal/domain/... ./internal/app/... ./internal/infra/...

# Тесты пока есть только у internal/domain (см. domain/*_test.go —
# чистые функции, перенесённые сюда именно затем, чтобы их вообще
# можно было проверить без Windows/GOPATH-тулчейна, см. историю
# коммитов). internal/app/internal/infra тоже в списке заранее —
# добавить им тесты в будущем не потребует трогать Makefile, а без
# тестов go test на пакете просто молча скажет "no test files".
test:
	$(GO) test ./internal/domain/... ./internal/app/... ./internal/infra/...

fmt:
	@gofmt -l .

rsrc:
	go run github.com/akavel/rsrc -ico .assets/app.ico -manifest .assets/manifest.xml -arch 386 -o rsrc.syso

# --- TwiXP ---------------------------------------------------------------

# Идемпотентно: повторный make deps ничего не перекачивает, если нужные
# коммиты уже на месте.
deps:
	@mkdir -p $(GOPATH_XP)/src/github.com/lxn $(GOPATH_XP)/src/gopkg.in/Knetic
	@if [ ! -d $(GOPATH_XP)/src/github.com/lxn/walk ]; then \
		git clone --quiet https://github.com/lxn/walk.git $(GOPATH_XP)/src/github.com/lxn/walk; \
	fi
	@git -C $(GOPATH_XP)/src/github.com/lxn/walk checkout --quiet $(WALK_COMMIT)
	@if [ ! -d $(GOPATH_XP)/src/github.com/lxn/win ]; then \
		git clone --quiet https://github.com/lxn/win.git $(GOPATH_XP)/src/github.com/lxn/win; \
	fi
	@git -C $(GOPATH_XP)/src/github.com/lxn/win checkout --quiet $(WIN_COMMIT)
	@if [ ! -d $(GOPATH_XP)/src/gopkg.in/Knetic/govaluate.v3 ]; then \
		git clone --quiet https://github.com/Knetic/govaluate.git $(GOPATH_XP)/src/gopkg.in/Knetic/govaluate.v3; \
	fi
	@ln -sfn $(CURDIR) $(GOPATH_XP)/src/twitchclient
	@echo "GOPATH_XP готов: $(GOPATH_XP)"

# Быстрая проверка, что main.go/internal/ui компилируются и линкуются в
# настоящий Windows PE — текущим (современным) Go, без ожидания GO_XP.
# НЕ подходит для финального exe: современный Go может молча принять
# то, что упадёт на 1.10.8 (например, случайный strings.ReplaceAll в
# нашем собственном коде).
check: deps
	@test -f config.go || { \
		echo "Нет config.go — скопируйте config.go.example в config.go и впишите свой Twitch Client ID."; \
		exit 1; \
	}
	cd $(GOPATH_XP)/src/twitchclient && \
	GOPATH=$(GOPATH_XP) GO111MODULE=off GOOS=windows GOARCH=386 \
		$(GO) vet .
	@mkdir -p $(BIN_DIR)
	cd $(GOPATH_XP)/src/twitchclient && \
	GOPATH=$(GOPATH_XP) GO111MODULE=off GOOS=windows GOARCH=386 \
		$(GO) build -ldflags="-H windowsgui" -o $(CURDIR)/$(BIN_DIR)/twixp-check.exe .
	@echo "check: собралось текущим Go для GOOS=windows/386 — $(BIN_DIR)/twixp-check.exe"

# Финальная сборка — тем самым GO_XP (Go 1.10.8), которым будет
# собираться реальный XP-бинарник. -H windowsgui обязателен: без него
# получится консольный субсистемный exe, и Windows откроет чёрное окно
# консоли позади GUI.
build: deps
	@test -f config.go || { \
		echo "Нет config.go — скопируйте config.go.example в config.go и впишите свой Twitch Client ID."; \
		exit 1; \
	}
	@command -v $(GO_XP) >/dev/null 2>&1 || { \
		echo "Не найден $(GO_XP) в PATH."; \
		echo "Финальная сборка под XP требует настоящий Go 1.10.8:"; \
		echo "  go install golang.org/dl/go1.10.8@latest && go1.10.8 download"; \
		echo "Для быстрой проверки без него: make check"; \
		exit 1; \
	}
	@mkdir -p $(BIN_DIR)
	cd $(GOPATH_XP)/src/twitchclient && \
	GOPATH=$(GOPATH_XP) GO111MODULE=off GOOS=windows GOARCH=386 \
		$(GO_XP) build -ldflags="-H windowsgui" -o $(CURDIR)/$(BIN_DIR)/twixp.exe .
	@echo "Готово: $(BIN_DIR)/twixp.exe"