package integration_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"gophermart/internal/app"
	"gophermart/tests/testutil"
)

// startTimeout ограничивает ожидание готовности сервиса в тестах.
const startTimeout = 15 * time.Second

// runService запускает сервис на свободном порту и возвращает его базовый
// адрес, накопленный журнал и функцию остановки.
func runService(t *testing.T, env map[string]string) (string, *syncBuffer, func() error) {
	t.Helper()

	logs := &syncBuffer{}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)

	go func() {
		done <- app.Main(ctx, []string{"-a", "127.0.0.1:0"}, lookupFrom(env), logs)
	}()

	baseURL := waitForAddress(t, logs)

	return baseURL, logs, func() error {
		cancel()

		select {
		case err := <-done:
			return err
		case <-time.After(startTimeout):
			return errors.New("сервис не остановился за отведённое время")
		}
	}
}

// lookupFrom превращает карту в функцию поиска переменных окружения.
func lookupFrom(env map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := env[key]

		return value, ok
	}
}

// waitForAddress извлекает из журнала адрес, на котором сервис открыл сокет.
func waitForAddress(t *testing.T, logs *syncBuffer) string {
	t.Helper()

	deadline := time.Now().Add(startTimeout)
	for time.Now().Before(deadline) {
		for line := range strings.SplitSeq(logs.String(), "\n") {
			const marker = `"address":"`

			if _, rest, found := strings.Cut(line, marker); found {
				addr, _, _ := strings.Cut(rest, `"`)

				return "http://" + addr
			}
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("сервис не сообщил адрес прослушивания: %s", logs.String())

	return ""
}

// get выполняет GET-запрос к сервису.
func get(t *testing.T, url string) *http.Response {
	t.Helper()

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("создание запроса: %v", err)
	}

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("выполнение запроса: %v", err)
	}

	t.Cleanup(func() {
		_ = response.Body.Close()
	})

	return response
}

func TestServiceStartsInReviewerConfiguration(t *testing.T) {
	dsn := testutil.NewDatabase(t)

	// Конфигурация проверяющего: только обязательные параметры, без JWT_SECRET.
	env := map[string]string{
		app.EnvDatabaseURI:          dsn,
		app.EnvAccrualSystemAddress: "http://localhost:8081",
	}

	baseURL, logs, stop := runService(t, env)

	response := get(t, baseURL+"/нет-такого-маршрута")
	if response.StatusCode != http.StatusNotFound {
		t.Errorf("неожиданный код ответа: got %d, want %d", response.StatusCode, http.StatusNotFound)
	}

	if err := stop(); err != nil {
		t.Errorf("остановка сервиса вернула ошибку: %v", err)
	}

	output := logs.String()

	if !strings.Contains(output, app.EnvJWTSecret) {
		t.Errorf("нет предупреждения о сгенерированном секрете подписи: %s", output)
	}

	if !strings.Contains(output, "схема базы данных актуальна") {
		t.Errorf("миграции не применялись при старте: %s", output)
	}

	if !strings.Contains(output, "сервис остановлен") {
		t.Errorf("сервис не сообщил о штатной остановке: %s", output)
	}
}

func TestServiceDoesNotLeakSecrets(t *testing.T) {
	dsn := testutil.NewDatabase(t)

	env := map[string]string{
		app.EnvDatabaseURI:          dsn,
		app.EnvAccrualSystemAddress: "http://localhost:8081",
		app.EnvJWTSecret:            "очень-секретное-значение",
	}

	_, logs, stop := runService(t, env)

	if err := stop(); err != nil {
		t.Errorf("остановка сервиса вернула ошибку: %v", err)
	}

	output := logs.String()

	for _, secret := range []string{"очень-секретное-значение", "gophermart:gophermart@"} {
		if strings.Contains(output, secret) {
			t.Errorf("секрет попал в журнал: %s", output)
		}
	}
}

func TestServiceCompressesResponse(t *testing.T) {
	dsn := testutil.NewDatabase(t)

	env := map[string]string{
		app.EnvDatabaseURI:          dsn,
		app.EnvAccrualSystemAddress: "http://localhost:8081",
	}

	baseURL, _, stop := runService(t, env)

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/нет-такого-маршрута", nil)
	if err != nil {
		t.Fatalf("создание запроса: %v", err)
	}

	request.Header.Set("Accept-Encoding", "gzip")

	// Транспорт по умолчанию распаковывает ответ прозрачно; отключаем это,
	// чтобы увидеть заголовки сжатия.
	transport := &http.Transport{DisableCompression: true}
	client := &http.Client{Transport: transport}

	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("выполнение запроса: %v", err)
	}

	defer func() {
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		transport.CloseIdleConnections()
	}()

	if got := response.Header.Get("Content-Encoding"); got != "gzip" {
		t.Errorf("ответ не сжат: got %q", got)
	}

	if got := response.Header.Get("Vary"); !strings.Contains(got, "Accept-Encoding") {
		t.Errorf("отсутствует заголовок Vary: got %q", got)
	}

	if err = stop(); err != nil {
		t.Errorf("остановка сервиса вернула ошибку: %v", err)
	}
}

// TestServiceDoesNotLogPasswordDuringAuthScenarios закрепляет требование
// «Пароль не попадает в ответ и логи» на уровне полностью запущенного
// сервиса: пароль, переданный при регистрации и при последующем входе, не
// должен появиться ни в одной записи журнала.
func TestServiceDoesNotLogPasswordDuringAuthScenarios(t *testing.T) {
	dsn := testutil.NewDatabase(t)

	env := map[string]string{
		app.EnvDatabaseURI:          dsn,
		app.EnvAccrualSystemAddress: "http://localhost:8081",
	}

	baseURL, logs, stop := runService(t, env)

	const password = "очень-секретный-пароль-для-теста"

	registerBody := `{"login":"gopher","password":"` + password + `"}`
	if response := postJSON(t, baseURL+"/api/user/register", registerBody); response.StatusCode != http.StatusOK {
		t.Fatalf("неожиданный код ответа регистрации: got %d, want %d", response.StatusCode, http.StatusOK)
	}

	loginBody := `{"login":"gopher","password":"` + password + `"}`
	if response := postJSON(t, baseURL+"/api/user/login", loginBody); response.StatusCode != http.StatusOK {
		t.Fatalf("неожиданный код ответа входа: got %d, want %d", response.StatusCode, http.StatusOK)
	}

	if err := stop(); err != nil {
		t.Errorf("остановка сервиса вернула ошибку: %v", err)
	}

	if output := logs.String(); strings.Contains(output, password) {
		t.Errorf("пароль попал в журнал: %s", output)
	}
}

// postJSON выполняет POST-запрос с телом JSON.
func postJSON(t *testing.T, url, body string) *http.Response {
	t.Helper()

	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("создание запроса: %v", err)
	}

	request.Header.Set("Content-Type", "application/json")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("выполнение запроса: %v", err)
	}

	t.Cleanup(func() {
		_ = response.Body.Close()
	})

	return response
}

func TestMainFailsWithoutDatabaseURI(t *testing.T) {
	env := map[string]string{app.EnvAccrualSystemAddress: "http://localhost:8081"}

	err := app.Main(t.Context(), []string{"-a", "127.0.0.1:0"}, lookupFrom(env), io.Discard)
	if !errors.Is(err, app.ErrMissingConfig) {
		t.Errorf("ожидалась ошибка отсутствующего параметра: %v", err)
	}
}

func TestMainFailsWhenDatabaseUnreachable(t *testing.T) {
	env := map[string]string{
		app.EnvDatabaseURI:          "postgres://gophermart:gophermart@127.0.0.1:1/gophermart?sslmode=disable",
		app.EnvAccrualSystemAddress: "http://localhost:8081",
	}

	err := app.Main(t.Context(), []string{"-a", "127.0.0.1:0"}, lookupFrom(env), io.Discard)
	if err == nil {
		t.Error("ожидалась ошибка подключения к недоступной базе данных")
	}
}
