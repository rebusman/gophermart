package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"gophermart/internal/transport/http/middleware"
)

func TestRequestIDGeneratedWhenAbsent(t *testing.T) {
	var seen string

	handler := middleware.RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = middleware.RequestIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	response := serve(handler, httptest.NewRequest(http.MethodGet, "/", nil))

	returned := response.Header().Get(middleware.HeaderRequestID)
	if returned == "" {
		t.Fatal("идентификатор запроса не возвращён клиенту")
	}

	if seen != returned {
		t.Errorf("идентификатор в контексте отличается от возвращённого: %q и %q", seen, returned)
	}
}

func TestRequestIDTakenFromClient(t *testing.T) {
	const clientID = "abc-123"

	var seen string

	handler := middleware.RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = middleware.RequestIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set(middleware.HeaderRequestID, clientID)

	response := serve(handler, request)

	if got := response.Header().Get(middleware.HeaderRequestID); got != clientID {
		t.Errorf("клиентский идентификатор не возвращён: got %q, want %q", got, clientID)
	}

	if seen != clientID {
		t.Errorf("клиентский идентификатор не попал в контекст: got %q, want %q", seen, clientID)
	}
}

func TestGeneratedRequestIDsAreUnique(t *testing.T) {
	handler := middleware.RequestID(okHandler())

	first := serve(handler, httptest.NewRequest(http.MethodGet, "/", nil))
	second := serve(handler, httptest.NewRequest(http.MethodGet, "/", nil))

	if first.Header().Get(middleware.HeaderRequestID) == second.Header().Get(middleware.HeaderRequestID) {
		t.Error("сгенерированные идентификаторы совпали")
	}
}

func TestRequestIDFromContextWithoutMiddleware(t *testing.T) {
	if got := middleware.RequestIDFromContext(t.Context()); got != "" {
		t.Errorf("ожидалась пустая строка, получено %q", got)
	}
}
