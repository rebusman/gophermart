.DEFAULT_GOAL := help

BINARY      ?= gophermart
BUILD_DIR   ?= bin
MAIN_PKG    ?= ./cmd/gophermart

RUN_ADDRESS            ?= :8080
DATABASE_URI           ?= postgres://gophermart:gophermart@localhost:5432/gophermart?sslmode=disable
ACCRUAL_SYSTEM_ADDRESS ?= http://localhost:8081

# Фиксированный секрет подписи для локальной разработки: без него сервис
# генерирует случайный секрет при каждом старте, и выпущенные токены перестают
# работать после перезапуска. Для рабочих окружений значение задаётся извне.
JWT_SECRET ?= local-development-secret-not-for-production

# Строка подключения, используемая интеграционными тестами. Тесты создают в
# этом кластере отдельные базы и удаляют их после себя.
TEST_DATABASE_URI ?= $(DATABASE_URI)

.PHONY: help
help: ## Показать список целей
	@echo "Доступные цели:"
	@echo "  build          собрать бинарник в $(BUILD_DIR)/"
	@echo "  run            запустить сервис локально"
	@echo "  fmt            отформатировать исходный код"
	@echo "  lint           статический анализ (go vet + golangci-lint)"
	@echo "  test           юнит-тесты"
	@echo "  test-race      все тесты с детектором гонок"
	@echo "  test-integration  интеграционные тесты (нужен PostgreSQL)"
	@echo "  cover          отчёт о покрытии тестами"
	@echo "  tidy           привести go.mod и go.sum в порядок"
	@echo "  compose-up     поднять PostgreSQL для локальной разработки"
	@echo "  compose-down   остановить PostgreSQL и удалить данные"
	@echo "  migrate-up     применить миграции вручную"
	@echo "  migrate-down   откатить последнюю миграцию"
	@echo "  openapi-lint   проверить OpenAPI-контракт"
	@echo "  openapi-docs   собрать HTML-документацию по контракту"

.PHONY: build
build: ## Собрать бинарник
	go build -o $(BUILD_DIR)/$(BINARY) $(MAIN_PKG)

.PHONY: run
run: ## Запустить сервис локально
	JWT_SECRET="$(JWT_SECRET)" go run $(MAIN_PKG) -a "$(RUN_ADDRESS)" -d "$(DATABASE_URI)" -r "$(ACCRUAL_SYSTEM_ADDRESS)"

.PHONY: fmt
fmt: ## Отформатировать исходный код
	gofmt -w cmd internal migrations tests

.PHONY: lint
lint: ## Статический анализ
	go vet ./...
	golangci-lint run

.PHONY: test
test: ## Юнит-тесты
	go test ./...

.PHONY: test-race
test-race: ## Все тесты с детектором гонок
	go test -race ./...

.PHONY: test-integration
test-integration: ## Интеграционные тесты (нужен PostgreSQL)
	TEST_DATABASE_URI="$(TEST_DATABASE_URI)" go test -count=1 ./tests/...

.PHONY: cover
cover: ## Отчёт о покрытии тестами
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

.PHONY: tidy
tidy: ## Привести go.mod и go.sum в порядок
	go mod tidy

.PHONY: compose-up
compose-up: ## Поднять PostgreSQL для локальной разработки
	docker compose up -d postgres

.PHONY: compose-down
compose-down: ## Остановить PostgreSQL и удалить данные
	docker compose down -v

.PHONY: migrate-up
migrate-up: ## Применить миграции вручную
	docker compose run --rm migrate up

.PHONY: migrate-down
migrate-down: ## Откатить последнюю миграцию
	docker compose run --rm migrate down 1

# Проверка и сборка документации OpenAPI выполняются в контейнере: отдельный
# инструмент устанавливать не нужно, достаточно Docker.
REDOCLY_IMAGE ?= redocly/cli:latest
REDOCLY_RUN   ?= docker run --rm -v "$(CURDIR)/api:/spec" -w /spec $(REDOCLY_IMAGE)

.PHONY: openapi-lint
openapi-lint: ## Проверить OpenAPI-контракт
	$(REDOCLY_RUN) lint --config redocly.yaml

.PHONY: openapi-docs
openapi-docs: ## Собрать HTML-документацию по контракту
	$(REDOCLY_RUN) build-docs openapi.yaml -o openapi.html
