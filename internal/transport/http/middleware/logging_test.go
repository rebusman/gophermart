package middleware_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gophermart/internal/logging"
	"gophermart/internal/transport/http/middleware"
)

func TestLoggingRecordsRequestFields(t *testing.T) {
	logger, logs := captureLogger()

	handler := middleware.RequestID(middleware.Logging(logger)(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTeapot)
			_, _ = w.Write([]byte("тело"))
		})))

	_ = serve(handler, httptest.NewRequest(http.MethodPost, "/api/user/orders", nil))

	var record map[string]any
	if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
		t.Fatalf("запись не является JSON: %v (%s)", err, logs.String())
	}

	if record["method"] != http.MethodPost {
		t.Errorf("метод не записан: %v", record["method"])
	}

	if record["path"] != "/api/user/orders" {
		t.Errorf("путь не записан: %v", record["path"])
	}

	if record["status"] != float64(http.StatusTeapot) {
		t.Errorf("код ответа не записан: %v", record["status"])
	}

	if _, ok := record["duration"]; !ok {
		t.Error("длительность не записана")
	}

	if record[logging.AttrRequestID] == "" {
		t.Error("идентификатор запроса не записан")
	}

	if record["bytes"] != float64(len("тело")) {
		t.Errorf("объём тела не записан: %v", record["bytes"])
	}
}

func TestLoggingWritesSingleRecordPerRequest(t *testing.T) {
	logger, logs := captureLogger()

	handler := middleware.Logging(logger)(okHandler())
	_ = serve(handler, httptest.NewRequest(http.MethodGet, "/", nil))

	if lines := strings.Count(strings.TrimSpace(logs.String()), "\n"); lines != 0 {
		t.Errorf("ожидалась одна запись на запрос, получено %d", lines+1)
	}
}

func TestLoggingDoesNotRecordCredentials(t *testing.T) {
	const token = "Bearer очень-секретный-токен"

	logger, logs := captureLogger()

	handler := middleware.Logging(logger)(okHandler())

	request := httptest.NewRequest(http.MethodPost, "/api/user/login", strings.NewReader(`{"password":"тайна"}`))
	request.Header.Set("Authorization", token)

	_ = serve(handler, request)

	output := logs.String()

	for _, secret := range []string{token, "очень-секретный-токен", "тайна"} {
		if strings.Contains(output, secret) {
			t.Errorf("учётные данные попали в журнал: %s", output)
		}
	}
}

func TestLoggingPutsLoggerIntoContext(t *testing.T) {
	logger, logs := captureLogger()

	handler := middleware.RequestID(middleware.Logging(logger)(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			logging.FromContext(r.Context()).InfoContext(r.Context(), "запись обработчика")
			w.WriteHeader(http.StatusOK)
		})))

	_ = serve(handler, httptest.NewRequest(http.MethodGet, "/", nil))

	if !strings.Contains(logs.String(), "запись обработчика") {
		t.Fatalf("обработчик не получил логгер из контекста: %s", logs.String())
	}

	first, _, _ := strings.Cut(logs.String(), "\n")

	var record map[string]any
	if err := json.Unmarshal([]byte(first), &record); err != nil {
		t.Fatalf("запись не является JSON: %v", err)
	}

	if id, ok := record[logging.AttrRequestID].(string); !ok || id == "" {
		t.Errorf("запись обработчика без идентификатора запроса: %v", record)
	}
}
