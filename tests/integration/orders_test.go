package integration_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"gophermart/internal/app"
	"gophermart/internal/auth"
	"gophermart/internal/service"
	"gophermart/internal/storage/postgres"
	httptransport "gophermart/internal/transport/http"
	"gophermart/internal/transport/http/handlers"
	"gophermart/internal/transport/http/middleware"
	"gophermart/migrations"
	"gophermart/tests/testutil"
)

// newOrdersRouter собирает маршрутизатор с реальными сервисами
// аутентификации и заказов поверх свежей базы данных.
//
// Стоимость хеширования паролей взята минимальной: сквозные тесты проверяют
// связность слоёв, а не производительность bcrypt.
func newOrdersRouter(t *testing.T) (*httptransport.Router, *pgxpool.Pool) {
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
	orderService := service.NewOrders(postgres.NewOrderRepository(pool))
	balanceService := service.NewBalances(postgres.NewBalanceRepository(pool))

	router, err := httptransport.NewRouter(httptransport.RouterConfig{
		Logger:              slog.New(slog.DiscardHandler),
		MaxRequestBodyBytes: 1 << 20,
		Auth:                handlers.NewAuth(authService, authRouterTokenTTL),
		Orders:              handlers.NewOrders(orderService),
		Balance:             handlers.NewBalance(balanceService),
		Authenticator:       authService,
	})
	if err != nil {
		t.Fatalf("сборка маршрутизатора: %v", err)
	}

	return router, pool
}

// registerOrderUser регистрирует пользователя и возвращает его токен доступа.
func registerOrderUser(t *testing.T, router *httptransport.Router, login string) string {
	t.Helper()

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, registerRequest(login))

	if recorder.Code != http.StatusOK {
		t.Fatalf("регистрация %s не удалась: got %d, want %d", login, recorder.Code, http.StatusOK)
	}

	return bearerToken(t, recorder)
}

// uploadOrder загружает номер заказа от имени владельца токена.
func uploadOrder(t *testing.T, router *httptransport.Router, token, number string) *httptest.ResponseRecorder {
	t.Helper()

	request := httptest.NewRequest(http.MethodPost, "/api/user/orders", strings.NewReader(number))
	request.Header.Set("Content-Type", "text/plain")
	request.Header.Set(middleware.HeaderAuthorization, middleware.BearerScheme+" "+token)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	return recorder
}

// listOrders запрашивает список заказов владельца токена.
func listOrders(t *testing.T, router *httptransport.Router, token string) *httptest.ResponseRecorder {
	t.Helper()

	request := httptest.NewRequest(http.MethodGet, "/api/user/orders", nil)
	request.Header.Set(middleware.HeaderAuthorization, middleware.BearerScheme+" "+token)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	return recorder
}

// decodeOrders разбирает тело ответа как массив заказов.
func decodeOrders(t *testing.T, recorder *httptest.ResponseRecorder) []map[string]any {
	t.Helper()

	var payload []map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("ответ не является массивом JSON: %v (%s)", err, recorder.Body.String())
	}

	return payload
}

// TestEndToEndUploadThenListOrder закрепляет сценарий «Заказ виден владельцу
// сразу после загрузки»: регистрация, загрузка номера и список заказов
// проходят одной цепочкой, а поле начисления в ответе отсутствует.
func TestEndToEndUploadThenListOrder(t *testing.T) {
	router, _ := newOrdersRouter(t)
	token := registerOrderUser(t, router, "gopher")

	if recorder := uploadOrder(t, router, token, orderNumberFirst); recorder.Code != http.StatusAccepted {
		t.Fatalf("неожиданный код ответа загрузки: got %d, want %d", recorder.Code, http.StatusAccepted)
	}

	recorder := listOrders(t, router, token)
	if recorder.Code != http.StatusOK {
		t.Fatalf("неожиданный код ответа списка: got %d, want %d", recorder.Code, http.StatusOK)
	}

	payload := decodeOrders(t, recorder)

	if len(payload) != 1 {
		t.Fatalf("неожиданное число заказов: got %d, want 1", len(payload))
	}

	if payload[0]["number"] != orderNumberFirst {
		t.Errorf("неожиданный номер заказа: got %v, want %s", payload[0]["number"], orderNumberFirst)
	}

	if payload[0]["status"] != "NEW" {
		t.Errorf("неожиданное состояние расчёта: got %v, want NEW", payload[0]["status"])
	}

	if _, ok := payload[0]["accrual"]; ok {
		t.Errorf("поле начисления присутствует у нерассчитанного заказа: %v", payload[0])
	}

	if _, ok := payload[0]["uploaded_at"]; !ok {
		t.Errorf("в ответе нет времени загрузки: %v", payload[0])
	}
}

// TestEndToEndUploadOwnAndForeignNumber закрепляет сценарии «Повторная
// загрузка своего номера» и «Загрузка чужого номера»: 200 и 409 соответственно,
// а в базе остаётся ровно один заказ с неизменным состоянием.
func TestEndToEndUploadOwnAndForeignNumber(t *testing.T) {
	router, pool := newOrdersRouter(t)
	owner := registerOrderUser(t, router, "gopher")
	stranger := registerOrderUser(t, router, "другой-gopher")

	if recorder := uploadOrder(t, router, owner, orderNumberFirst); recorder.Code != http.StatusAccepted {
		t.Fatalf("неожиданный код ответа первой загрузки: got %d, want %d", recorder.Code, http.StatusAccepted)
	}

	before := decodeOrders(t, listOrders(t, router, owner))

	if recorder := uploadOrder(t, router, owner, orderNumberFirst); recorder.Code != http.StatusOK {
		t.Errorf("неожиданный код ответа повторной загрузки своего номера: got %d, want %d",
			recorder.Code, http.StatusOK)
	}

	recorder := uploadOrder(t, router, stranger, orderNumberFirst)
	if recorder.Code != http.StatusConflict {
		t.Errorf("неожиданный код ответа загрузки чужого номера: got %d, want %d",
			recorder.Code, http.StatusConflict)
	}

	if body := recorder.Body.String(); strings.Contains(body, "gopher") {
		t.Errorf("ответ 409 раскрывает владельца номера: %s", body)
	}

	if count := countOrders(t, pool); count != 1 {
		t.Errorf("неожиданное число заказов с номером: got %d, want 1", count)
	}

	after := decodeOrders(t, listOrders(t, router, owner))
	if len(after) != 1 || after[0]["status"] != before[0]["status"] ||
		after[0]["uploaded_at"] != before[0]["uploaded_at"] {
		t.Errorf("состояние заказа изменилось: got %v, want %v", after, before)
	}

	if _, ok := after[0]["accrual"]; ok {
		t.Errorf("у заказа появилось начисление: %v", after[0])
	}

	if strangerRecorder := listOrders(t, router, stranger); strangerRecorder.Code != http.StatusNoContent {
		t.Errorf("чужой заказ попал в список второго пользователя: got %d, want %d",
			strangerRecorder.Code, http.StatusNoContent)
	}
}

// TestEndToEndConcurrentUploadOfSameNumber закрепляет сценарий «Конкурентная
// загрузка одного номера»: два одновременных запроса дают ровно один заказ, ни
// один из них не завершается кодом 500, и исходы соответствуют правилам для
// своего и чужого номера.
func TestEndToEndConcurrentUploadOfSameNumber(t *testing.T) {
	router, pool := newOrdersRouter(t)
	first := registerOrderUser(t, router, "gopher")
	second := registerOrderUser(t, router, "другой-gopher")

	const attempts = 8

	codes := make([]int, attempts)
	tokens := []string{first, second}

	var (
		wg    sync.WaitGroup
		start = make(chan struct{})
	)

	for i := range attempts {
		wg.Go(func() {
			<-start

			codes[i] = uploadOrder(t, router, tokens[i%len(tokens)], orderNumberFirst).Code
		})
	}

	close(start)
	wg.Wait()

	if count := countOrders(t, pool); count != 1 {
		t.Errorf("конкурентная загрузка создала не один заказ: got %d, want 1", count)
	}

	accepted := 0

	for i, code := range codes {
		switch code {
		case http.StatusAccepted:
			accepted++
		case http.StatusOK, http.StatusConflict:
		default:
			t.Errorf("запрос %d завершился неожиданным кодом: got %d", i, code)
		}
	}

	if accepted != 1 {
		t.Errorf("заказ принят в обработку не ровно один раз: got %d, want 1", accepted)
	}
}

// TestEndToEndUploadWhenAccrualSystemUnavailable закрепляет сценарий «Система
// расчёта недоступна»: полностью запущенный сервис, настроенный на
// недоступный адрес системы расчёта, принимает номер заказа кодом 202 и
// сохраняет заказ в состоянии NEW.
func TestEndToEndUploadWhenAccrualSystemUnavailable(t *testing.T) {
	dsn := testutil.NewDatabase(t)

	// Порт 1 заведомо не слушается: любое обращение к системе расчёта
	// завершилось бы отказом соединения.
	env := map[string]string{
		app.EnvDatabaseURI:          dsn,
		app.EnvAccrualSystemAddress: "http://127.0.0.1:1",
	}

	baseURL, _, stop := runService(t, env)

	registerResponse := postJSON(t, baseURL+"/api/user/register",
		`{"login":"gopher","password":"`+testPassword+`"}`)
	if registerResponse.StatusCode != http.StatusOK {
		t.Fatalf("неожиданный код ответа регистрации: got %d, want %d",
			registerResponse.StatusCode, http.StatusOK)
	}

	token := registerResponse.Header.Get(middleware.HeaderAuthorization)
	if token == "" {
		t.Fatal("ответ регистрации не содержит токен доступа")
	}

	uploadRequest, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		baseURL+"/api/user/orders", strings.NewReader(orderNumberFirst))
	if err != nil {
		t.Fatalf("создание запроса загрузки: %v", err)
	}

	uploadRequest.Header.Set("Content-Type", "text/plain")
	uploadRequest.Header.Set(middleware.HeaderAuthorization, token)

	uploadResponse, err := http.DefaultClient.Do(uploadRequest)
	if err != nil {
		t.Fatalf("выполнение запроса загрузки: %v", err)
	}

	defer func() {
		_, _ = io.Copy(io.Discard, uploadResponse.Body)
		_ = uploadResponse.Body.Close()
	}()

	if uploadResponse.StatusCode != http.StatusAccepted {
		t.Errorf("недоступная система расчёта повлияла на ответ: got %d, want %d",
			uploadResponse.StatusCode, http.StatusAccepted)
	}

	listRequest, err := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/api/user/orders", nil)
	if err != nil {
		t.Fatalf("создание запроса списка: %v", err)
	}

	listRequest.Header.Set(middleware.HeaderAuthorization, token)

	listResponse, err := http.DefaultClient.Do(listRequest)
	if err != nil {
		t.Fatalf("выполнение запроса списка: %v", err)
	}

	defer func() {
		_ = listResponse.Body.Close()
	}()

	body, err := io.ReadAll(listResponse.Body)
	if err != nil {
		t.Fatalf("чтение тела списка: %v", err)
	}

	if !strings.Contains(string(body), `"status":"NEW"`) {
		t.Errorf("заказ сохранён не в состоянии NEW: %s", body)
	}

	if err = stop(); err != nil {
		t.Errorf("остановка сервиса вернула ошибку: %v", err)
	}
}

// TestEndToEndOrderListsAreIsolated закрепляет сценарий «Чужие заказы не
// видны»: каждый пользователь видит только свои заказы, а пользователь без
// заказов получает 204 с пустым телом.
func TestEndToEndOrderListsAreIsolated(t *testing.T) {
	router, _ := newOrdersRouter(t)
	first := registerOrderUser(t, router, "gopher")
	second := registerOrderUser(t, router, "другой-gopher")
	third := registerOrderUser(t, router, "gopher-без-заказов")

	if recorder := uploadOrder(t, router, first, orderNumberFirst); recorder.Code != http.StatusAccepted {
		t.Fatalf("загрузка заказа первым пользователем: got %d", recorder.Code)
	}

	if recorder := uploadOrder(t, router, second, orderNumberSecond); recorder.Code != http.StatusAccepted {
		t.Fatalf("загрузка заказа вторым пользователем: got %d", recorder.Code)
	}

	owners := map[string]string{first: orderNumberFirst, second: orderNumberSecond}
	foreign := map[string]string{first: orderNumberSecond, second: orderNumberFirst}

	for token, own := range owners {
		payload := decodeOrders(t, listOrders(t, router, token))

		if len(payload) != 1 || payload[0]["number"] != own {
			t.Errorf("неожиданный список заказов: got %v, want единственный номер %s", payload, own)
		}

		for _, order := range payload {
			if order["number"] == foreign[token] {
				t.Errorf("в списке присутствует чужой заказ: %v", order)
			}
		}
	}

	recorder := listOrders(t, router, third)
	if recorder.Code != http.StatusNoContent {
		t.Errorf("неожиданный код ответа для пользователя без заказов: got %d, want %d",
			recorder.Code, http.StatusNoContent)
	}

	if recorder.Body.Len() != 0 {
		t.Errorf("ответ 204 содержит тело: %s", recorder.Body.String())
	}
}
