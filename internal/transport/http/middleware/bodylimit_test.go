package middleware_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gophermart/internal/transport/http/middleware"
)

// bodyLimit — лимит, используемый тестами ограничения размера тела.
const bodyLimit = 16

func TestBodyLimitRejectsOversizedBody(t *testing.T) {
	handler := middleware.BodyLimit(bodyLimit)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("обработчик не должен вызываться при превышении лимита")
	}))

	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("x", bodyLimit+1)))
	response := serve(handler, request)

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("неожиданный код ответа: got %d, want %d", response.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestBodyLimitPassesThroughAllowedBody(t *testing.T) {
	var received string

	handler := middleware.BodyLimit(bodyLimit)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("чтение тела: %v", err)
		}

		received = string(body)
		w.WriteHeader(http.StatusOK)
	}))

	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("тело"))
	response := serve(handler, request)

	if response.Code != http.StatusOK {
		t.Errorf("неожиданный код ответа: got %d, want %d", response.Code, http.StatusOK)
	}

	if received != "тело" {
		t.Errorf("тело искажено: got %q", received)
	}
}

func TestBodyLimitDetectsOverflowWithUnknownLength(t *testing.T) {
	var readErr error

	handler := middleware.BodyLimit(bodyLimit)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, readErr = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))

	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("y", bodyLimit+1)))
	request.ContentLength = -1

	_ = serve(handler, request)

	if readErr == nil {
		t.Fatal("чтение тела сверх лимита не вернуло ошибку")
	}

	if !middleware.IsBodyTooLarge(readErr) {
		t.Errorf("ошибка не распознана как превышение лимита: %v", readErr)
	}
}

func TestIsBodyTooLargeIgnoresOtherErrors(t *testing.T) {
	if middleware.IsBodyTooLarge(io.ErrUnexpectedEOF) {
		t.Error("посторонняя ошибка распознана как превышение лимита")
	}

	if middleware.IsBodyTooLarge(nil) {
		t.Error("отсутствие ошибки распознано как превышение лимита")
	}
}
