package handlers_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gophermart/internal/domain"
	"gophermart/internal/transport/http/handlers"
	"gophermart/internal/transport/http/middleware"
)

// Параметры, общие для тестов обработчиков аутентификации.
//
// Значение testToken состоит только из ASCII-символов: cookie-значения не
// допускают произвольные байты, и net/http молча вырезает недопустимые байты
// из значения cookie, что сделало бы проверку значения бессмысленной.
const (
	testToken    = "issued-token"
	testTokenTTL = time.Hour
)

// errInternal — внутренняя ошибка, не являющаяся доменной.
//
// Текст намеренно похож на сообщение PostgreSQL: тест проверяет, что он не
// попадает в тело ответа.
var errInternal = errors.New(
	`ERROR: duplicate key value violates unique constraint "users_login_unique" (SQLSTATE 23505)`,
)

// authServiceStub подменяет прикладной сервис в тестах обработчиков.
type authServiceStub struct {
	register func(ctx context.Context, login, password string) (string, error)
	login    func(ctx context.Context, login, password string) (string, error)

	registerCalls int
	loginCalls    int
	gotLogin      string
	gotPassword   string
}

// route выбирает проверяемый маршрут.
type route struct {
	name   string
	handle func(h *handlers.Auth) http.HandlerFunc
	call   func(stub *authServiceStub, fn func(ctx context.Context, login, password string) (string, error))
}

// routes перечисляет оба маршрута аутентификации: сценарии разбора тела,
// доставки токена и сокрытия внутренних деталей одинаковы для обоих.
func routes() []route {
	return []route{
		{
			name:   "register",
			handle: func(h *handlers.Auth) http.HandlerFunc { return h.Register },
			call: func(stub *authServiceStub, fn func(context.Context, string, string) (string, error)) {
				stub.register = fn
			},
		},
		{
			name:   "login",
			handle: func(h *handlers.Auth) http.HandlerFunc { return h.Login },
			call: func(stub *authServiceStub, fn func(context.Context, string, string) (string, error)) {
				stub.login = fn
			},
		},
	}
}

// do выполняет запрос к обработчику с заданным телом.
func do(t *testing.T, handler http.HandlerFunc, body string) *httptest.ResponseRecorder {
	t.Helper()

	request := httptest.NewRequest(http.MethodPost, "/api/user/register", strings.NewReader(body))
	recorder := httptest.NewRecorder()

	handler(recorder, request)

	return recorder
}

// authCookie возвращает cookie с токеном из ответа.
func authCookie(t *testing.T, recorder *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()

	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == middleware.CookieAuthToken {
			return cookie
		}
	}

	return nil
}

func (s *authServiceStub) Register(ctx context.Context, login, password string) (string, error) {
	s.registerCalls++
	s.gotLogin = login
	s.gotPassword = password

	if s.register == nil {
		return testToken, nil
	}

	return s.register(ctx, login, password)
}

func (s *authServiceStub) Login(ctx context.Context, login, password string) (string, error) {
	s.loginCalls++
	s.gotLogin = login
	s.gotPassword = password

	if s.login == nil {
		return testToken, nil
	}

	return s.login(ctx, login, password)
}

func TestHandlersDeliverTokenBothWays(t *testing.T) {
	for _, r := range routes() {
		t.Run(r.name, func(t *testing.T) {
			handler := handlers.NewAuth(&authServiceStub{}, testTokenTTL)

			recorder := do(t, r.handle(handler), `{"login":"gopher","password":"пароль"}`)

			if recorder.Code != http.StatusOK {
				t.Fatalf("неожиданный код ответа: got %d, want %d", recorder.Code, http.StatusOK)
			}

			assertAuthorizationHeader(t, recorder)
			assertAuthCookie(t, recorder)
		})
	}
}

// assertAuthorizationHeader проверяет, что ответ несёт токен заголовком Authorization.
func assertAuthorizationHeader(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()

	authorization := recorder.Header().Get(middleware.HeaderAuthorization)
	if authorization != middleware.BearerScheme+" "+testToken {
		t.Errorf("неожиданный заголовок Authorization: got %q", authorization)
	}
}

// assertAuthCookie проверяет все атрибуты cookie с токеном, требуемые спецификацией.
func assertAuthCookie(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()

	cookie := authCookie(t, recorder)
	if cookie == nil {
		t.Fatal("ответ не устанавливает cookie с токеном")
	}

	if cookie.Value != testToken {
		t.Errorf("cookie содержит другой токен: got %q, want %q", cookie.Value, testToken)
	}

	if !cookie.HttpOnly {
		t.Error("cookie не помечена HttpOnly")
	}

	if cookie.Secure {
		t.Error("cookie помечена Secure: клиент отбросил бы её при работе по HTTP")
	}

	if cookie.Path != "/" {
		t.Errorf("неожиданный путь cookie: got %q, want %q", cookie.Path, "/")
	}

	if cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("неожиданное значение SameSite: got %v", cookie.SameSite)
	}

	if cookie.MaxAge != int(testTokenTTL.Seconds()) {
		t.Errorf("время жизни cookie не совпадает с TTL токена: got %d, want %d",
			cookie.MaxAge, int(testTokenTTL.Seconds()))
	}
}

func TestHandlersPassCredentialsToService(t *testing.T) {
	for _, r := range routes() {
		t.Run(r.name, func(t *testing.T) {
			stub := &authServiceStub{}
			handler := handlers.NewAuth(stub, testTokenTTL)

			do(t, r.handle(handler), `{"login":"  Gopher  ","password":"пароль"}`)

			if stub.gotLogin != "  Gopher  " {
				t.Errorf("логин изменён обработчиком: got %q", stub.gotLogin)
			}

			if stub.gotPassword != "пароль" {
				t.Errorf("пароль изменён обработчиком: got %q", stub.gotPassword)
			}
		})
	}
}

func TestHandlersRejectMalformedBody(t *testing.T) {
	bodies := map[string]string{
		"не JSON":             "это не json",
		"обрезанный":          `{"login":"gopher"`,
		"массив":              `["gopher","пароль"]`,
		"пустое тело":         "",
		"число вместо строки": `{"login":42,"password":"пароль"}`,
	}

	for _, r := range routes() {
		for name, body := range bodies {
			t.Run(r.name+"/"+name, func(t *testing.T) {
				stub := &authServiceStub{}
				handler := handlers.NewAuth(stub, testTokenTTL)

				recorder := do(t, r.handle(handler), body)

				if recorder.Code != http.StatusBadRequest {
					t.Errorf("неожиданный код ответа: got %d, want %d", recorder.Code, http.StatusBadRequest)
				}

				if stub.registerCalls+stub.loginCalls != 0 {
					t.Error("сервис вызван при некорректном теле запроса")
				}

				assertNoToken(t, recorder)
			})
		}
	}
}

func TestHandlersMapDomainErrorsToStatuses(t *testing.T) {
	tests := map[string]struct {
		err  error
		want int
	}{
		"занятый логин":          {err: domain.ErrLoginTaken, want: http.StatusConflict},
		"неверные данные":        {err: domain.ErrInvalidCredentials, want: http.StatusUnauthorized},
		"пустой логин":           {err: domain.ErrEmptyLogin, want: http.StatusBadRequest},
		"пустой пароль":          {err: domain.ErrEmptyPassword, want: http.StatusBadRequest},
		"слишком длинный пароль": {err: domain.ErrPasswordTooLong, want: http.StatusBadRequest},
		"внутренняя ошибка":      {err: errInternal, want: http.StatusInternalServerError},
	}

	for _, r := range routes() {
		for name, test := range tests {
			t.Run(r.name+"/"+name, func(t *testing.T) {
				stub := &authServiceStub{}
				r.call(stub, func(context.Context, string, string) (string, error) {
					return "", test.err
				})

				handler := handlers.NewAuth(stub, testTokenTTL)
				recorder := do(t, r.handle(handler), `{"login":"gopher","password":"пароль"}`)

				if recorder.Code != test.want {
					t.Errorf("неожиданный код ответа: got %d, want %d", recorder.Code, test.want)
				}

				assertNoToken(t, recorder)
			})
		}
	}
}

// TestHandlersHideInternalDetails закрепляет требование не раскрывать
// внутренние подробности: сообщение PostgreSQL, имена таблиц и ограничений в
// тело ответа не попадают.
func TestHandlersHideInternalDetails(t *testing.T) {
	for _, r := range routes() {
		t.Run(r.name, func(t *testing.T) {
			stub := &authServiceStub{}
			r.call(stub, func(context.Context, string, string) (string, error) {
				return "", errInternal
			})

			handler := handlers.NewAuth(stub, testTokenTTL)
			recorder := do(t, r.handle(handler), `{"login":"gopher","password":"пароль"}`)

			body := recorder.Body.String()

			for _, secret := range []string{"users_login_unique", "SQLSTATE", "duplicate key", "users"} {
				if strings.Contains(body, secret) {
					t.Errorf("тело ответа раскрывает внутреннюю деталь %q: %s", secret, body)
				}
			}
		})
	}
}

// TestHandlersNeverEchoPassword закрепляет требование «пароль не попадает в
// ответ»: ни успешный, ни любой неуспешный ответ его не содержит.
func TestHandlersNeverEchoPassword(t *testing.T) {
	const password = "correct-horse-battery-staple"

	errorsByStatus := []error{
		nil,
		domain.ErrLoginTaken,
		domain.ErrInvalidCredentials,
		domain.ErrEmptyLogin,
		errInternal,
	}

	for _, r := range routes() {
		for _, serviceErr := range errorsByStatus {
			t.Run(r.name, func(t *testing.T) {
				stub := &authServiceStub{}
				r.call(stub, func(context.Context, string, string) (string, error) {
					if serviceErr != nil {
						return "", serviceErr
					}

					return testToken, nil
				})

				handler := handlers.NewAuth(stub, testTokenTTL)
				recorder := do(t, r.handle(handler), `{"login":"gopher","password":"`+password+`"}`)

				var dumped strings.Builder
				dumped.WriteString(recorder.Body.String())

				for key, values := range recorder.Header() {
					dumped.WriteString(key)
					dumped.WriteString(strings.Join(values, " "))
				}

				if strings.Contains(dumped.String(), password) {
					t.Errorf("пароль попал в ответ: %s", dumped.String())
				}
			})
		}
	}
}

// assertNoToken проверяет, что ответ не выдаёт токен ни одним из способов.
func assertNoToken(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()

	if header := recorder.Header().Get(middleware.HeaderAuthorization); header != "" {
		t.Errorf("неуспешный ответ содержит заголовок Authorization: %q", header)
	}

	if cookie := authCookie(t, recorder); cookie != nil {
		t.Errorf("неуспешный ответ устанавливает cookie с токеном: %q", cookie.Value)
	}
}
