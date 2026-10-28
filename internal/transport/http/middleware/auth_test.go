package middleware_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"gophermart/internal/domain"
	"gophermart/internal/transport/http/middleware"
)

// authenticatorStub подменяет проверку токена в тестах middleware.Auth.
type authenticatorStub struct {
	authenticate func(ctx context.Context, token string) (domain.UserID, error)

	gotToken string
	calls    int
}

func (s *authenticatorStub) Authenticate(ctx context.Context, token string) (domain.UserID, error) {
	s.calls++
	s.gotToken = token

	if s.authenticate == nil {
		return domain.UserID{}, domain.ErrUnauthenticated
	}

	return s.authenticate(ctx, token)
}

// protectedCall описывает вызов защищённого обработчика: выполнился ли он и
// какой идентификатор пользователя он увидел в контексте.
type protectedCall struct {
	called bool
	userID domain.UserID
	found  bool
}

// protectedHandler возвращает обработчик, фиксирующий факт своего вызова и
// идентификатор пользователя, извлечённый из контекста.
func protectedHandler(call *protectedCall) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call.called = true
		call.userID, call.found = middleware.UserIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
}

func TestAuthRejectsRequestWithoutToken(t *testing.T) {
	stub := &authenticatorStub{}
	call := &protectedCall{}

	handler := middleware.Auth(stub)(protectedHandler(call))
	recorder := serve(handler, httptest.NewRequest(http.MethodGet, "/protected", nil))

	if recorder.Code != http.StatusUnauthorized {
		t.Errorf("неожиданный код ответа: got %d, want %d", recorder.Code, http.StatusUnauthorized)
	}

	if call.called {
		t.Error("защищённый обработчик вызван без токена")
	}

	if stub.calls != 0 {
		t.Error("проверка токена выполнена при его отсутствии")
	}
}

func TestAuthRejectsInvalidToken(t *testing.T) {
	tests := map[string]error{
		"подделанная подпись":         domain.ErrUnauthenticated,
		"истёкший токен":              domain.ErrUnauthenticated,
		"несуществующий пользователь": domain.ErrUnauthenticated,
	}

	for name, wantErr := range tests {
		t.Run(name, func(t *testing.T) {
			stub := &authenticatorStub{authenticate: func(context.Context, string) (domain.UserID, error) {
				return domain.UserID{}, wantErr
			}}
			call := &protectedCall{}

			handler := middleware.Auth(stub)(protectedHandler(call))

			request := httptest.NewRequest(http.MethodGet, "/protected", nil)
			request.Header.Set(middleware.HeaderAuthorization, middleware.BearerScheme+" мусор")

			recorder := serve(handler, request)

			if recorder.Code != http.StatusUnauthorized {
				t.Errorf("неожиданный код ответа: got %d, want %d", recorder.Code, http.StatusUnauthorized)
			}

			if call.called {
				t.Error("защищённый обработчик вызван при недействительном токене")
			}
		})
	}
}

func TestAuthRejectsMalformedAuthorizationHeader(t *testing.T) {
	headers := map[string]string{
		"без схемы":         "просто-токен",
		"неверная схема":    "Basic токен",
		"пустой токен":      "Bearer",
		"токен из пробелов": "Bearer    ",
	}

	for name, header := range headers {
		t.Run(name, func(t *testing.T) {
			stub := &authenticatorStub{}
			call := &protectedCall{}

			handler := middleware.Auth(stub)(protectedHandler(call))

			request := httptest.NewRequest(http.MethodGet, "/protected", nil)
			request.Header.Set(middleware.HeaderAuthorization, header)

			recorder := serve(handler, request)

			if recorder.Code != http.StatusUnauthorized {
				t.Errorf("неожиданный код ответа: got %d, want %d", recorder.Code, http.StatusUnauthorized)
			}

			if call.called {
				t.Error("защищённый обработчик вызван при повреждённом заголовке")
			}
		})
	}
}

func TestAuthAcceptsValidTokenFromHeader(t *testing.T) {
	wantID := domain.NewUserID()
	stub := &authenticatorStub{authenticate: func(_ context.Context, token string) (domain.UserID, error) {
		if token != "действительный-токен" {
			return domain.UserID{}, domain.ErrUnauthenticated
		}

		return wantID, nil
	}}
	call := &protectedCall{}

	handler := middleware.Auth(stub)(protectedHandler(call))

	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.Header.Set(middleware.HeaderAuthorization, middleware.BearerScheme+" действительный-токен")

	recorder := serve(handler, request)

	if recorder.Code != http.StatusOK {
		t.Errorf("неожиданный код ответа: got %d, want %d", recorder.Code, http.StatusOK)
	}

	if !call.called {
		t.Fatal("защищённый обработчик не вызван при действительном токене")
	}

	if !call.found || call.userID != wantID {
		t.Errorf("неожиданный идентификатор пользователя в контексте: got %v, found %t, want %v",
			call.userID, call.found, wantID)
	}
}

func TestAuthAcceptsValidTokenFromCookie(t *testing.T) {
	wantID := domain.NewUserID()
	stub := &authenticatorStub{authenticate: func(_ context.Context, token string) (domain.UserID, error) {
		if token != "cookie-token" {
			return domain.UserID{}, domain.ErrUnauthenticated
		}

		return wantID, nil
	}}
	call := &protectedCall{}

	handler := middleware.Auth(stub)(protectedHandler(call))

	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.AddCookie(&http.Cookie{Name: middleware.CookieAuthToken, Value: "cookie-token"})

	recorder := serve(handler, request)

	if recorder.Code != http.StatusOK {
		t.Errorf("неожиданный код ответа: got %d, want %d", recorder.Code, http.StatusOK)
	}

	if !call.called || !call.found || call.userID != wantID {
		t.Errorf("аутентификация по cookie не выполнена: called=%t found=%t id=%v",
			call.called, call.found, call.userID)
	}
}

// TestAuthHeaderTakesPriorityOverCookie закрепляет сценарий требования
// «Заголовок имеет приоритет над cookie»: при расхождении источников
// проверяется только токен из заголовка.
func TestAuthHeaderTakesPriorityOverCookie(t *testing.T) {
	stub := &authenticatorStub{authenticate: func(context.Context, string) (domain.UserID, error) {
		return domain.NewUserID(), nil
	}}
	call := &protectedCall{}

	handler := middleware.Auth(stub)(protectedHandler(call))

	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.Header.Set(middleware.HeaderAuthorization, middleware.BearerScheme+" header-token")
	request.AddCookie(&http.Cookie{Name: middleware.CookieAuthToken, Value: "cookie-token"})

	serve(handler, request)

	if stub.gotToken != "header-token" {
		t.Errorf("проверен не токен из заголовка: got %q", stub.gotToken)
	}
}

// TestAuthIgnoresClientSuppliedUserID закрепляет требование «Достоверный
// идентификатор пользователя»: значение, помещённое в контекст, совпадает с
// возвращённым Authenticate и не зависит от заголовков запроса.
func TestAuthIgnoresClientSuppliedUserID(t *testing.T) {
	tokenOwner := domain.NewUserID()
	spoofed := domain.NewUserID()

	stub := &authenticatorStub{authenticate: func(context.Context, string) (domain.UserID, error) {
		return tokenOwner, nil
	}}
	call := &protectedCall{}

	handler := middleware.Auth(stub)(protectedHandler(call))

	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.Header.Set(middleware.HeaderAuthorization, middleware.BearerScheme+" токен")
	request.Header.Set("X-User-Id", spoofed.String())

	serve(handler, request)

	if call.userID != tokenOwner {
		t.Errorf("идентификатор подменён: got %v, want %v (владелец токена)", call.userID, tokenOwner)
	}
}

// errAuthenticatorFailure — произвольная внутренняя ошибка проверки токена,
// отличная от domain.ErrUnauthenticated.
var errAuthenticatorFailure = errors.New("сбой проверки токена")

// TestAuthRejectsOnAnyAuthenticatorError закрепляет, что любая ошибка
// Authenticate, не только доменная, приводит к 401 без вызова обработчика.
func TestAuthRejectsOnAnyAuthenticatorError(t *testing.T) {
	stub := &authenticatorStub{authenticate: func(context.Context, string) (domain.UserID, error) {
		return domain.UserID{}, errAuthenticatorFailure
	}}
	call := &protectedCall{}

	handler := middleware.Auth(stub)(protectedHandler(call))

	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.Header.Set(middleware.HeaderAuthorization, middleware.BearerScheme+" токен")

	recorder := serve(handler, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Errorf("неожиданный код ответа: got %d, want %d", recorder.Code, http.StatusUnauthorized)
	}

	if call.called {
		t.Error("защищённый обработчик вызван при сбое проверки токена")
	}
}

// TestUserIDFromContextReportsAbsence проверяет, что запрос без прохождения
// middleware.Auth не находит идентификатор пользователя в контексте.
func TestUserIDFromContextReportsAbsence(t *testing.T) {
	_, found := middleware.UserIDFromContext(t.Context())
	if found {
		t.Error("идентификатор найден в контексте, не прошедшем проверку токена")
	}
}
