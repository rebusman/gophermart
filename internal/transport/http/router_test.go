package httptransport_test

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	httptransport "gophermart/internal/transport/http"
	"gophermart/internal/transport/http/middleware"
)

// routerBodyLimit — лимит размера тела, используемый тестами маршрутизатора.
const routerBodyLimit = 32

// newRouter собирает маршрутизатор с логгером, пишущим в возвращаемый буфер, и
// одним зарегистрированным публичным маршрутом, отвечающим кодом 200.
//
// Путь тестового маршрута не совпадает с путями сервиса: маршруты заказов
// регистрирует сам конструктор, и повторная регистрация того же пути подменила
// бы проверяемый обработчик.
func newRouter(t *testing.T) (*httptransport.Router, *bytes.Buffer) {
	t.Helper()

	logs := &bytes.Buffer{}

	router := httptransport.NewRouter(httptransport.RouterConfig{
		Logger:              slog.New(slog.NewJSONHandler(logs, nil)),
		MaxRequestBodyBytes: routerBodyLimit,
	})

	router.Post("/api/user/тестовый-маршрут", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "принято")
	})

	// Маршрут читает тело целиком и потому обнаруживает превышение лимита
	// даже тогда, когда длина запроса заранее неизвестна.
	router.Post("/echo", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			if middleware.IsBodyTooLarge(err) {
				w.WriteHeader(http.StatusRequestEntityTooLarge)

				return
			}

			w.WriteHeader(http.StatusBadRequest)

			return
		}

		_, _ = w.Write(body)
	})

	return router, logs
}

func TestRouterReturnsNotFoundForUnknownPath(t *testing.T) {
	router, _ := newRouter(t)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/нет-такого-пути", nil))

	if recorder.Code != http.StatusNotFound {
		t.Errorf("неожиданный код ответа: got %d, want %d", recorder.Code, http.StatusNotFound)
	}
}

func TestRouterReturnsMethodNotAllowed(t *testing.T) {
	router, _ := newRouter(t)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodDelete, "/api/user/orders", nil))

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Errorf("неожиданный код ответа: got %d, want %d", recorder.Code, http.StatusMethodNotAllowed)
	}
}

func TestRouterServesRegisteredRoute(t *testing.T) {
	router, _ := newRouter(t)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder,
		httptest.NewRequest(http.MethodPost, "/api/user/тестовый-маршрут", strings.NewReader("1")))

	if recorder.Code != http.StatusOK {
		t.Errorf("неожиданный код ответа: got %d, want %d", recorder.Code, http.StatusOK)
	}
}

func TestRouterAppliesBodyLimitBelowDecompression(t *testing.T) {
	router, _ := newRouter(t)

	// Сжатое представление короче лимита, а распакованное — длиннее.
	var compressed bytes.Buffer

	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write([]byte(strings.Repeat("a", routerBodyLimit*10))); err != nil {
		t.Fatalf("сжатие: %v", err)
	}

	if err := writer.Close(); err != nil {
		t.Fatalf("закрытие компрессора: %v", err)
	}

	if compressed.Len() > routerBodyLimit {
		t.Fatalf("сжатое тело не короче лимита: %d байт", compressed.Len())
	}

	request := httptest.NewRequest(http.MethodPost, "/echo", bytes.NewReader(compressed.Bytes()))
	request.Header.Set("Content-Encoding", "gzip")

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("лимит применён к сжатому телу вместо распакованного: got %d, want %d",
			recorder.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestRouterLogsRealStatusWhenResponseCompressed(t *testing.T) {
	router, logs := newRouter(t)

	request := httptest.NewRequest(http.MethodGet, "/нет-такого-пути", nil)
	request.Header.Set("Accept-Encoding", "gzip")

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if got := recorder.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("ответ не сжат, проверка порядка цепочки бессмысленна: got %q", got)
	}

	var record map[string]any
	if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
		t.Fatalf("запись не является JSON: %v (%s)", err, logs.String())
	}

	if record["status"] != float64(http.StatusNotFound) {
		t.Errorf("в журнал попал не реальный код ответа: got %v, want %d", record["status"], http.StatusNotFound)
	}
}

func TestRouterRecoversFromPanicInHandler(t *testing.T) {
	logs := &bytes.Buffer{}

	router := httptransport.NewRouter(httptransport.RouterConfig{
		Logger:              slog.New(slog.NewJSONHandler(logs, nil)),
		MaxRequestBodyBytes: routerBodyLimit,
	})

	router.Get("/panic", func(http.ResponseWriter, *http.Request) {
		panic("падение обработчика")
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/panic", nil))

	if recorder.Code != http.StatusInternalServerError {
		t.Errorf("неожиданный код ответа: got %d, want %d", recorder.Code, http.StatusInternalServerError)
	}

	if strings.Contains(recorder.Body.String(), "падение обработчика") {
		t.Errorf("текст panic раскрыт клиенту: %s", recorder.Body.String())
	}
}

func TestRouterSetsRequestIDOnEveryResponse(t *testing.T) {
	router, _ := newRouter(t)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/нет-такого-пути", nil))

	if recorder.Header().Get("X-Request-Id") == "" {
		t.Error("идентификатор запроса не возвращён клиенту")
	}
}

func TestRouterAppliesMiddlewareWithoutRegisteredRoutes(t *testing.T) {
	logs := &bytes.Buffer{}

	// Маршруты намеренно не регистрируются: цепочка сквозных обработчиков
	// обязана работать и на «пустом» сервисе.
	router := httptransport.NewRouter(httptransport.RouterConfig{
		Logger:              slog.New(slog.NewJSONHandler(logs, nil)),
		MaxRequestBodyBytes: routerBodyLimit,
	})

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Accept-Encoding", "gzip")

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Errorf("неожиданный код ответа: got %d, want %d", recorder.Code, http.StatusNotFound)
	}

	if recorder.Header().Get("X-Request-Id") == "" {
		t.Error("обработчик идентификатора запроса не выполнился")
	}

	if got := recorder.Header().Get("Content-Encoding"); got != "gzip" {
		t.Errorf("обработчик сжатия не выполнился: got %q", got)
	}

	if got := recorder.Header().Get("Vary"); !strings.Contains(got, "Accept-Encoding") {
		t.Errorf("отсутствует заголовок Vary: got %q", got)
	}

	if logs.Len() == 0 {
		t.Error("обработчик журналирования не выполнился")
	}
}

// TestRouterProtectsOrderRoutes закрепляет сценарий «Запрос без токена»
// требования «Доступ к заказам только аутентифицированному пользователю»: оба
// маршрута заказов зарегистрированы в группе защищённых, а неизвестный путь
// по-прежнему даёт 404.
func TestRouterProtectsOrderRoutes(t *testing.T) {
	router, _ := newRouter(t)

	tests := []struct {
		method string
		path   string
		status int
	}{
		{method: http.MethodPost, path: "/api/user/orders", status: http.StatusUnauthorized},
		{method: http.MethodGet, path: "/api/user/orders", status: http.StatusUnauthorized},
		{method: http.MethodGet, path: "/api/user/orders/9278923470", status: http.StatusNotFound},
	}

	for _, test := range tests {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(test.method, test.path, strings.NewReader("9278923470")))

			if recorder.Code != test.status {
				t.Errorf("неожиданный код ответа: got %d, want %d", recorder.Code, test.status)
			}
		})
	}
}
