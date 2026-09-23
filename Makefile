GO      ?= go
BIN     := bin
# Кросс-компиляция с ноутбука: на сервере не нужно ни Go, ни рантайма —
# статические бинарники сервисов.
GOOS    ?= linux
GOARCH  ?= amd64
LDFLAGS := -s -w

# Адрес сервера для ручных целей: make deploy-manual HOST=user@vps
HOST ?=
# Порт SSH, если он не 22. Пустой по умолчанию — тогда ssh берёт настройки из
# ~/.ssh/config, как и при обычном заходе руками.
SSH_PORT ?=
SSHFLAGS := $(if $(SSH_PORT),-p $(SSH_PORT))
SCPFLAGS := $(if $(SSH_PORT),-P $(SSH_PORT))
# Образ локальной сборки.
IMAGE      ?= university-schedule:local
REMOTE_DIR ?= /opt/rasp

.PHONY: all build test test-ui vet fmt lint clean deploy deploy-manual deploy-binary \
        docker-build docker-up docker-down docker-logs backup \
        run-rasp run-bot run-admin run-web preview-web test-tools

all: vet test test-ui test-tools check-boundaries check-schema build

build:
	@mkdir -p $(BIN)
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) $(GO) build -ldflags="$(LDFLAGS)" -o $(BIN)/raspd  ./cmd/raspd
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) $(GO) build -ldflags="$(LDFLAGS)" -o $(BIN)/botd   ./cmd/botd
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) $(GO) build -ldflags="$(LDFLAGS)" -o $(BIN)/webd ./cmd/webd
	@ls -lh $(BIN)

test:
	$(GO) test ./...

# Node.js нужен только для регрессионных тестов панели, без npm-зависимостей.
test-tools:
	PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s tests -p "test_*.py"

# Node.js проверяет интерфейс.
test-ui:
	node --test tests/admin-ui.test.cjs tests/web.test.mjs tests/login-ui.test.mjs tests/pwa.test.mjs

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

lint: fmt vet test

clean:
	rm -rf $(BIN) tests/__pycache__ scripts/__pycache__ examples/adapter-json/__pycache__

# Развёртывание требует профиля и адаптера; общий CI не изменяет сервер.
deploy deploy-manual deploy-binary:
	@echo "Следуйте README.md и docs/ADMIN.md: нужны отдельный адаптер и профиль установки."
	@exit 1

docker-build:
	docker compose build

docker-up:
	docker compose up -d

docker-down:
	docker compose down

docker-logs:
	docker compose logs -f

# Бэкап базы с сервера: архив едет потоком в файл на ноутбуке, на сервере не
# остаётся ничего. База лежит в томе докера, а в образе нет ни шелла, ни tar,
# поэтому tar запускает одноразовый контейнер с alpine.
#
# Забирать базу стоит не ради расписания — оно за ночь скачается заново, — а
# ради таблицы пользователей: привязки, подгруппы и подписки восстановить
# неоткуда.
#
# Это файловый архив, а не SQLite backup API. Использовать только после остановки
# писателя и сервисов, которые отправляют ему изменения: архив живых DB/WAL-файлов
# не гарантирует согласованность. Для копии без остановки нужен SQLite backup API;
# порядок обновления и восстановления описан в docs/OPERATIONS.md.
backup:
	@test -n "$(HOST)" || (echo "укажи HOST=user@vps" && exit 1)
	ssh $(SSHFLAGS) $(HOST) 'docker run --rm -v rasp_db:/db alpine tar czf - -C /db .' \
	  > rasp-db-$(shell date +%F).tgz
	@ls -lh rasp-db-$(shell date +%F).tgz

# Локальный запуск для разработки: база и сокет в ./run.
run-rasp:
	@mkdir -p run
	RASP_PROFILE=$${RASP_PROFILE:-profiles/example.json} RASP_DB=run/schedule.db RASP_SOCKET=run/api.sock $(GO) run ./cmd/raspd

run-bot:
	RASP_PROFILE=$${RASP_PROFILE:-profiles/example.json} RASP_SOCKET=run/api.sock $(GO) run ./cmd/botd

# Панель работает внутри сайта на /admin/.
run-admin:
	@echo "Админка встроена в webd: настройте ADMIN_ENABLED, ADMIN_ALLOW_IPS и профиль; используйте make run-web."

# Сайт с настоящим расписанием; raspd запускается отдельно через run-rasp.
run-web:
	RASP_PROFILE=$${RASP_PROFILE:-profiles/example.json} RASP_SOCKET=run/api.sock $(GO) run ./cmd/webd

# Явный просмотр интерфейса на вымышленных данных, без базы и токенов.
preview-web:
	$(GO) run ./cmd/webd -demo

.PHONY: run-adapter-json run-adapter-kgu check-adapter
run-adapter-json:
	python3 examples/adapter-json/server.py
ifneq ($(wildcard cmd/adapter-kgu),)
run-adapter-kgu:
	@mkdir -p run
	ADAPTER_DB=run/adapter.db $(GO) run ./cmd/adapter-kgu
endif
check-adapter:
	$(GO) run ./cmd/check-adapter

.PHONY: check-boundaries check-schema
check-boundaries:
	python3 tests/check-boundaries.py
check-schema:
	@$(GO) run ./cmd/provider-schema > /tmp/dairy-provider-schema.$$$$.json; \
	cmp docs/provider.openapi.json /tmp/dairy-provider-schema.$$$$.json; result=$$?; \
	rm -f /tmp/dairy-provider-schema.$$$$.json; exit $$result

.PHONY: build-adapter-kgu export-platform
ifneq ($(wildcard cmd/adapter-kgu),)
build-adapter-kgu:
	@mkdir -p $(BIN)
	CGO_ENABLED=0 $(GO) build -o $(BIN)/adapter-kgu ./cmd/adapter-kgu
endif
export-platform:
	python3 scripts/export-platform.py "$(or $(OUTPUT),dist/platform)" $(if $(filter 1 true,$(REPLACE)),--replace) $(if $(MODULE),--module "$(MODULE)")
