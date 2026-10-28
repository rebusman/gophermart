package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gophermart/internal/transport/http/middleware"
)

func TestRecoveryReturnsInternalServerError(t *testing.T) {
	const panicText = "секретная деталь реализации"

	logger, logs := captureLogger()

	handler := middleware.Recovery(logger)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(panicText)
	}))

	response := serve(handler, httptest.NewRequest(http.MethodGet, "/", nil))

	if response.Code != http.StatusInternalServerError {
		t.Errorf("неожиданный код ответа: got %d, want %d", response.Code, http.StatusInternalServerError)
	}

	body := response.Body.String()

	if strings.Contains(body, panicText) {
		t.Errorf("текст panic раскрыт клиенту: %s", body)
	}

	if strings.Contains(body, "goroutine") {
		t.Errorf("stack trace раскрыт клиенту: %s", body)
	}

	if !strings.Contains(logs.String(), panicText) {
		t.Errorf("подробности panic не записаны в журнал: %s", logs.String())
	}

	if !strings.Contains(logs.String(), "goroutine") {
		t.Errorf("stack trace не записан в журнал: %s", logs.String())
	}
}

func TestRecoveryKeepsServingAfterPanic(t *testing.T) {
	logger, _ := captureLogger()

	var requests int

	handler := middleware.Recovery(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		if requests == 1 {
			panic("первый запрос падает")
		}

		w.WriteHeader(http.StatusOK)
	}))

	_ = serve(handler, httptest.NewRequest(http.MethodGet, "/", nil))
	second := serve(handler, httptest.NewRequest(http.MethodGet, "/", nil))

	if second.Code != http.StatusOK {
		t.Errorf("сервис не обслужил следующий запрос: got %d, want %d", second.Code, http.StatusOK)
	}
}

func TestRecoveryPassesThroughNormalRequests(t *testing.T) {
	logger, _ := captureLogger()

	handler := middleware.Recovery(logger)(okHandler())
	response := serve(handler, httptest.NewRequest(http.MethodGet, "/", nil))

	if response.Code != http.StatusOK {
		t.Errorf("неожиданный код ответа: got %d, want %d", response.Code, http.StatusOK)
	}
}
