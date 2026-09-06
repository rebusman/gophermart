package integration_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"gophermart/internal/auth"
	"gophermart/internal/domain"
	"gophermart/internal/service"
	"gophermart/internal/storage/postgres"
	httptransport "gophermart/internal/transport/http"
	"gophermart/internal/transport/http/handlers"
	"gophermart/internal/transport/http/middleware"
	"gophermart/migrations"
	"gophermart/tests/testutil"
)

// authRouterTokenTTL — срок действия токена, используемый сквозными тестами
// аутентификации.
const authRouterTokenTTL = time.Hour

// stubOrderService — заглушка сервиса заказов для тестов аутентификации.
//
// Маршруты заказов обязательны при сборке маршрутизатора, но в этих тестах не
// используются. Заглушка вместо nil-сервиса даёт им отвечать пустым списком, а
// не аварийно завершать процесс.
type stubOrderService struct{}

func (stubOrderService) Upload(context.Context, domain.OrderNumber, domain.UserID) (domain.OrderUpload, error) {
	return domain.OrderUploadAccepted, nil
}

func (stubOrderService) List(context.Context, domain.UserID) ([]domain.Order, error) {
	return []domain.Order{}, nil
}

// stubBalanceService — заглушка сервиса счёта лояльности для тестов
// аутентификации.
//
// Маршруты счёта обязательны при сборке маршрутизатора, но в этих тестах не
// используются. Заглушка вместо nil-сервиса даёт им отвечать пустым
// результатом, а не аварийно завершать процесс.
type stubBalanceService struct{}

func (stubBalanceService) Balance(context.Context, domain.UserID) (domain.Balance, error) {
	return domain.Balance{}, nil
}

func (stubBalanceService) Withdraw(
	context.Context,
	domain.OrderNumber,
	decimal.Decimal,
	domain.UserID,
) error {
	return nil
}

func (stubBalanceService) Withdrawals(context.Context, domain.UserID) ([]domain.Withdrawal, error) {
	return []domain.Withdrawal{}, nil
}

// newAuthRouter собирает маршрутизатор с реальным сервисом аутентификации
// поверх свежей базы данных и регистрирует тестовый защищённый маршрут,
// отвечающий идентификатором пользователя из контекста.
//
// Стоимость хеширования паролей взята минимальной: сквозные тесты проверяют
// связность слоёв, а не производительность bcrypt.
func newAuthRouter(t *testing.T) *httptransport.Router {
	t.Helper()

	dsn := testutil.NewDatabase(t)

	if _, err := postgres.Migrate(t.Context(), dsn, migrations.FS); err != nil {
		t.Fatalf("применение миграций: %v", err)
	}

	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("создание пула подключений: %v", err)
	}

	t.Cleanup(pool.Close)

	hasher, err := auth.NewHasher(auth.MinCost)
	if err != nil {
		t.Fatalf("инициализация хеширования паролей: %v", err)
	}

	tokens, err := auth.NewTokenIssuer("сквозной-тестовый-секрет", authRouterTokenTTL)
	if err != nil {
		t.Fatalf("инициализация выпуска токенов: %v", err)
	}

	authService := service.NewAuth(postgres.NewUserRepository(pool), hasher, tokens)

	router, err := httptransport.NewRouter(httptransport.RouterConfig{
		Logger:              slog.New(slog.DiscardHandler),
		MaxRequestBodyBytes: 1 << 20,
		Auth:                handlers.NewAuth(authService, authRouterTokenTTL),
		Orders:              handlers.NewOrders(stubOrderService{}),
		Balance:             handlers.NewBalance(stubBalanceService{}),
		Authenticator:       authService,
	})
	if err != nil {
		t.Fatalf("сборка маршрутизатора: %v", err)
	}

	router.Protected().Get("/api/user/test-protected", func(w http.ResponseWriter, r *http.Request) {
		userID, ok := middleware.UserIDFromContext(r.Context())
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)

			return
		}

		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, userID.String())
	})

	return router
}

// testPassword — пароль, используемый сквозными тестами аутентификации.
const testPassword = "пароль"

// registerRequest выполняет запрос регистрации с заданным логином и тестовым паролем.
func registerRequest(login string) *http.Request {
	body := `{"login":"` + login + `","password":"` + testPassword + `"}`

	return httptest.NewRequest(http.MethodPost, "/api/user/register", strings.NewReader(body))
}

// loginRequest выполняет запрос входа с заданным логином и тестовым паролем.
func loginRequest(login string) *http.Request {
	body := `{"login":"` + login + `","password":"` + testPassword + `"}`

	return httptest.NewRequest(http.MethodPost, "/api/user/login", strings.NewReader(body))
}

// protectedRequestWithToken формирует запрос к тестовому защищённому
// маршруту, предъявляя токен способом, заданным attach.
func protectedRequestWithToken(attach func(r *http.Request)) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/api/user/test-protected", nil)
	attach(request)

	return request
}

// TestEndToEndRegisterThenAccessProtectedRoute закрепляет сценарий
// «регистрация → обращение к защищённому маршруту полученным токеном» без
// повторного входа.
func TestEndToEndRegisterThenAccessProtectedRoute(t *testing.T) {
	router := newAuthRouter(t)

	registerRecorder := httptest.NewRecorder()
	router.ServeHTTP(registerRecorder, registerRequest("gopher"))

	if registerRecorder.Code != http.StatusOK {
		t.Fatalf("неожиданный код ответа регистрации: got %d, want %d", registerRecorder.Code, http.StatusOK)
	}

	token := bearerToken(t, registerRecorder)

	protectedRecorder := httptest.NewRecorder()
	router.ServeHTTP(protectedRecorder, protectedRequestWithToken(func(r *http.Request) {
		r.Header.Set(middleware.HeaderAuthorization, middleware.BearerScheme+" "+token)
	}))

	if protectedRecorder.Code != http.StatusOK {
		t.Errorf("обращение к защищённому маршруту не удалось: got %d, want %d",
			protectedRecorder.Code, http.StatusOK)
	}
}

// TestEndToEndRegisterThenLoginBothTokensWork закрепляет сценарий
// «регистрация → вход → обращение новым токеном»: оба токена принимаются и
// оба ведут к одному и тому же пользователю.
func TestEndToEndRegisterThenLoginBothTokensWork(t *testing.T) {
	router := newAuthRouter(t)

	registerRecorder := httptest.NewRecorder()
	router.ServeHTTP(registerRecorder, registerRequest("gopher"))

	if registerRecorder.Code != http.StatusOK {
		t.Fatalf("неожиданный код ответа регистрации: got %d, want %d", registerRecorder.Code, http.StatusOK)
	}

	registerToken := bearerToken(t, registerRecorder)

	loginRecorder := httptest.NewRecorder()
	router.ServeHTTP(loginRecorder, loginRequest("gopher"))

	if loginRecorder.Code != http.StatusOK {
		t.Fatalf("неожиданный код ответа входа: got %d, want %d", loginRecorder.Code, http.StatusOK)
	}

	loginToken := bearerToken(t, loginRecorder)

	registerOwner := protectedOwner(t, router, registerToken)
	loginOwner := protectedOwner(t, router, loginToken)

	if registerOwner == "" || registerOwner != loginOwner {
		t.Errorf("токены ведут к разным пользователям: регистрация %q, вход %q", registerOwner, loginOwner)
	}
}

// TestEndToEndConcurrentRegistrationSameLogin закрепляет сценарий требования
// «Конкурентная регистрация одного логина»: из одновременных запросов ровно
// один получает 200, а в системе остаётся ровно одна учётная запись.
func TestEndToEndConcurrentRegistrationSameLogin(t *testing.T) {
	router := newAuthRouter(t)

	const attempts = 8

	codes := make([]int, attempts)

	var wg sync.WaitGroup

	for i := range attempts {
		wg.Go(func() {
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, registerRequest("одновременный-гофер"))
			codes[i] = recorder.Code
		})
	}

	wg.Wait()

	var ok, conflict int

	for _, code := range codes {
		switch code {
		case http.StatusOK:
			ok++
		case http.StatusConflict:
			conflict++
		default:
			t.Errorf("неожиданный код ответа при конкурентной регистрации: %d", code)
		}
	}

	if ok != 1 {
		t.Errorf("ожидался ровно один успешный ответ, получено %d", ok)
	}

	if conflict != attempts-1 {
		t.Errorf("ожидалось %d ответов с конфликтом, получено %d", attempts-1, conflict)
	}
}

// TestEndToEndProtectedRouteAcceptsCookieOnly закрепляет доставку токена через
// cookie: обращение к защищённому маршруту только с cookie, без заголовка
// Authorization, проходит аутентификацию.
func TestEndToEndProtectedRouteAcceptsCookieOnly(t *testing.T) {
	router := newAuthRouter(t)

	registerRecorder := httptest.NewRecorder()
	router.ServeHTTP(registerRecorder, registerRequest("gopher"))

	if registerRecorder.Code != http.StatusOK {
		t.Fatalf("неожиданный код ответа регистрации: got %d, want %d", registerRecorder.Code, http.StatusOK)
	}

	var cookie *http.Cookie

	for _, c := range registerRecorder.Result().Cookies() {
		if c.Name == middleware.CookieAuthToken {
			cookie = c
		}
	}

	if cookie == nil {
		t.Fatal("ответ регистрации не устанавливает cookie с токеном")
	}

	protectedRecorder := httptest.NewRecorder()
	router.ServeHTTP(protectedRecorder, protectedRequestWithToken(func(r *http.Request) {
		r.AddCookie(cookie)
	}))

	if protectedRecorder.Code != http.StatusOK {
		t.Errorf("аутентификация только по cookie не удалась: got %d, want %d",
			protectedRecorder.Code, http.StatusOK)
	}
}

// TestEndToEndProtectedRouteRejectsRequestWithoutToken закрепляет требование
// «Запрос без токена» на реальном маршрутизаторе: тестовый защищённый маршрут,
// подключённый через Protected, отвечает 401 без предъявленного токена.
func TestEndToEndProtectedRouteRejectsRequestWithoutToken(t *testing.T) {
	router := newAuthRouter(t)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/user/test-protected", nil))

	if recorder.Code != http.StatusUnauthorized {
		t.Errorf("неожиданный код ответа: got %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
}

// bearerToken извлекает токен доступа из заголовка Authorization ответа.
func bearerToken(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()

	header := recorder.Header().Get(middleware.HeaderAuthorization)

	_, token, found := strings.Cut(header, " ")
	if !found || token == "" {
		t.Fatalf("ответ не содержит токен в заголовке Authorization: %q", header)
	}

	return token
}

// protectedOwner обращается к тестовому защищённому маршруту с указанным
// токеном и возвращает идентификатор пользователя из тела ответа.
func protectedOwner(t *testing.T, router *httptransport.Router, token string) string {
	t.Helper()

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, protectedRequestWithToken(func(r *http.Request) {
		r.Header.Set(middleware.HeaderAuthorization, middleware.BearerScheme+" "+token)
	}))

	if recorder.Code != http.StatusOK {
		t.Fatalf("обращение к защищённому маршруту не удалось: got %d, want %d", recorder.Code, http.StatusOK)
	}

	return recorder.Body.String()
}
