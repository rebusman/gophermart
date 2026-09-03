package integration_test

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"gophermart/internal/auth"
	"gophermart/internal/service"
	"gophermart/internal/storage/postgres"
	httptransport "gophermart/internal/transport/http"
	"gophermart/internal/transport/http/handlers"
	"gophermart/internal/transport/http/middleware"
	"gophermart/migrations"
	"gophermart/tests/testutil"
)

// newBalanceRouter собирает маршрутизатор с реальными сервисами
// аутентификации, заказов и счёта лояльности поверх свежей базы данных.
//
// Сервис заказов нужен сценариям, проверяющим, что номер заказа в списании
// гипотетический: списание по чужому загруженному номеру должно выполняться на
// общих основаниях.
func newBalanceRouter(t *testing.T) (*httptransport.Router, *pgxpool.Pool) {
	t.Helper()

	dsn := testutil.NewDatabase(t)

	if err := postgres.Migrate(t.Context(), dsn, migrations.FS); err != nil {
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

// creditUser начисляет баллы пользователю, известному по его логину.
//
// Фонового расчёта начислений ещё нет, а списывать нечего, пока счёт пуст:
// помощник заменяет собой будущего второго писателя счёта.
func creditUser(t *testing.T, pool *pgxpool.Pool, login, amount string) {
	t.Helper()

	const credit = `
		UPDATE balances SET current = current + $2
		WHERE user_id = (SELECT id FROM users WHERE login = $1)`

	tag, err := pool.Exec(t.Context(), credit, login, amount)
	if err != nil {
		t.Fatalf("начисление баллов пользователю %s: %v", login, err)
	}

	if tag.RowsAffected() != 1 {
		t.Fatalf("счёт пользователя %s не найден", login)
	}
}

// getBalance запрашивает состояние счёта владельца токена.
func getBalance(t *testing.T, router *httptransport.Router, token string) *httptest.ResponseRecorder {
	t.Helper()

	request := httptest.NewRequest(http.MethodGet, "/api/user/balance", nil)
	request.Header.Set(middleware.HeaderAuthorization, middleware.BearerScheme+" "+token)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	return recorder
}

// withdraw выполняет списание от имени владельца токена.
func withdraw(t *testing.T, router *httptransport.Router, token, number, sum string) *httptest.ResponseRecorder {
	t.Helper()

	body := `{"order":"` + number + `","sum":` + sum + `}`

	return withdrawRawJSON(t, router, token, body)
}

// withdrawRawJSON выполняет списание от имени владельца токена с произвольным
// JSON-телом запроса.
func withdrawRawJSON(t *testing.T, router *httptransport.Router, token, body string) *httptest.ResponseRecorder {
	t.Helper()

	request := httptest.NewRequest(http.MethodPost, "/api/user/balance/withdraw", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(middleware.HeaderAuthorization, middleware.BearerScheme+" "+token)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	return recorder
}

// listWithdrawals запрашивает историю списаний владельца токена.
func listWithdrawals(t *testing.T, router *httptransport.Router, token string) *httptest.ResponseRecorder {
	t.Helper()

	request := httptest.NewRequest(http.MethodGet, "/api/user/withdrawals", nil)
	request.Header.Set(middleware.HeaderAuthorization, middleware.BearerScheme+" "+token)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	return recorder
}

// decodeBalance разбирает тело ответа как состояние счёта.
func decodeBalance(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()

	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("ответ не является объектом JSON: %v (%s)", err, recorder.Body.String())
	}

	return payload
}

// decodeWithdrawals разбирает тело ответа как массив списаний.
func decodeWithdrawals(t *testing.T, recorder *httptest.ResponseRecorder) []map[string]any {
	t.Helper()

	var payload []map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("ответ не является массивом JSON: %v (%s)", err, recorder.Body.String())
	}

	return payload
}

// requireBalanceResponse сверяет обе суммы в ответе о состоянии счёта.
func requireBalanceResponse(t *testing.T, router *httptransport.Router, token string, current, withdrawn float64) {
	t.Helper()

	recorder := getBalance(t, router, token)
	if recorder.Code != http.StatusOK {
		t.Fatalf("неожиданный код ответа баланса: got %d, want %d", recorder.Code, http.StatusOK)
	}

	payload := decodeBalance(t, recorder)

	if payload["current"] != current {
		t.Errorf("неожиданная текущая сумма баллов: got %v, want %v", payload["current"], current)
	}

	if payload["withdrawn"] != withdrawn {
		t.Errorf("неожиданная сумма списаний: got %v, want %v", payload["withdrawn"], withdrawn)
	}
}

// TestEndToEndRegisterThenReadBalance закрепляет сценарий «Счёт без операций»:
// только что зарегистрированный пользователь получает 200 с нулями, а не 204.
func TestEndToEndRegisterThenReadBalance(t *testing.T) {
	router, _ := newBalanceRouter(t)
	token := registerOrderUser(t, router, "gopher")

	recorder := getBalance(t, router, token)
	if recorder.Code != http.StatusOK {
		t.Fatalf("неожиданный код ответа: got %d, want %d", recorder.Code, http.StatusOK)
	}

	payload := decodeBalance(t, recorder)

	for _, field := range []string{"current", "withdrawn"} {
		value, ok := payload[field]
		if !ok {
			t.Fatalf("поле %s отсутствует в ответе: %s", field, recorder.Body)
		}

		if value != float64(0) {
			t.Errorf("поле %s не равно нулю: %v", field, value)
		}
	}
}

// TestEndToEndWithdrawThenReadBalanceAndHistory закрепляет сценарий «Успешное
// списание» целиком: списание уменьшает остаток, увеличивает сумму списаний и
// появляется в истории.
func TestEndToEndWithdrawThenReadBalanceAndHistory(t *testing.T) {
	router, pool := newBalanceRouter(t)
	token := registerOrderUser(t, router, "gopher")

	creditUser(t, pool, "gopher", "1000.55")

	if recorder := withdraw(t, router, token, orderNumberFirst, "249.05"); recorder.Code != http.StatusOK {
		t.Fatalf("неожиданный код ответа списания: got %d, want %d", recorder.Code, http.StatusOK)
	}

	requireBalanceResponse(t, router, token, 751.5, 249.05)

	recorder := listWithdrawals(t, router, token)
	if recorder.Code != http.StatusOK {
		t.Fatalf("неожиданный код ответа истории: got %d, want %d", recorder.Code, http.StatusOK)
	}

	payload := decodeWithdrawals(t, recorder)

	if len(payload) != 1 {
		t.Fatalf("неожиданное число списаний: got %d, want 1", len(payload))
	}

	if payload[0]["order"] != orderNumberFirst {
		t.Errorf("неожиданный номер заказа: got %v, want %s", payload[0]["order"], orderNumberFirst)
	}

	if payload[0]["sum"] != 249.05 {
		t.Errorf("неожиданная сумма списания: got %v, want 249.05", payload[0]["sum"])
	}

	if _, ok := payload[0]["processed_at"]; !ok {
		t.Errorf("время списания отсутствует в ответе: %s", recorder.Body)
	}
}

// TestEndToEndWithdrawRejectsInsufficientFunds закрепляет сценарии
// «Недостаточно баллов» и «Списание ровно на весь остаток».
func TestEndToEndWithdrawRejectsInsufficientFunds(t *testing.T) {
	router, pool := newBalanceRouter(t)
	token := registerOrderUser(t, router, "gopher")

	creditUser(t, pool, "gopher", "100")

	recorder := withdraw(t, router, token, orderNumberFirst, "100.01")
	if recorder.Code != http.StatusPaymentRequired {
		t.Fatalf("неожиданный код ответа: got %d, want %d", recorder.Code, http.StatusPaymentRequired)
	}

	requireBalanceResponse(t, router, token, 100, 0)

	if history := listWithdrawals(t, router, token); history.Code != http.StatusNoContent {
		t.Errorf("отклонённое списание попало в историю: код %d", history.Code)
	}

	// Граничное значение проходит: остаток становится нулевым.
	if recorder = withdraw(t, router, token, orderNumberFirst, "100"); recorder.Code != http.StatusOK {
		t.Fatalf("списание всего остатка отклонено: got %d, want %d", recorder.Code, http.StatusOK)
	}

	requireBalanceResponse(t, router, token, 0, 100)
}

// TestEndToEndConcurrentWithdrawalsKeepBalanceNonNegative закрепляет сценарий
// «Конкурентные списания, когда баллов хватает только на одно».
func TestEndToEndConcurrentWithdrawalsKeepBalanceNonNegative(t *testing.T) {
	router, pool := newBalanceRouter(t)
	token := registerOrderUser(t, router, "gopher")

	creditUser(t, pool, "gopher", "100")

	numbers := []string{orderNumberFirst, orderNumberSecond}
	codes := make([]int, len(numbers))

	var wg sync.WaitGroup

	for i, number := range numbers {
		wg.Go(func() {
			codes[i] = withdraw(t, router, token, number, "100").Code
		})
	}

	wg.Wait()

	var succeeded, rejected int

	for i, code := range codes {
		switch code {
		case http.StatusOK:
			succeeded++
		case http.StatusPaymentRequired:
			rejected++
		default:
			t.Errorf("списание %s завершилось неожиданным кодом %d", numbers[i], code)
		}
	}

	if succeeded != 1 || rejected != 1 {
		t.Errorf("неожиданный исход конкурентных списаний: успешных %d, отказов %d", succeeded, rejected)
	}

	requireBalanceResponse(t, router, token, 0, 100)

	history := listWithdrawals(t, router, token)
	if history.Code != http.StatusOK {
		t.Fatalf("неожиданный код ответа истории: got %d, want %d", history.Code, http.StatusOK)
	}

	if payload := decodeWithdrawals(t, history); len(payload) != 1 {
		t.Errorf("неожиданное число списаний в истории: got %d, want 1", len(payload))
	}
}

// TestEndToEndWithdrawIsIdempotent закрепляет требование «Идемпотентность
// списания по номеру заказа», включая повтор с иной суммой и конкурентный
// повтор.
func TestEndToEndWithdrawIsIdempotent(t *testing.T) {
	router, pool := newBalanceRouter(t)
	token := registerOrderUser(t, router, "gopher")

	creditUser(t, pool, "gopher", "1000")

	if recorder := withdraw(t, router, token, orderNumberFirst, "200"); recorder.Code != http.StatusOK {
		t.Fatalf("первое списание: got %d, want %d", recorder.Code, http.StatusOK)
	}

	// Повтор с иной суммой: код тот же, сохранённой остаётся первая сумма.
	if recorder := withdraw(t, router, token, orderNumberFirst, "50"); recorder.Code != http.StatusOK {
		t.Fatalf("повтор с иной суммой: got %d, want %d", recorder.Code, http.StatusOK)
	}

	const attempts = 4

	var wg sync.WaitGroup

	codes := make([]int, attempts)

	for i := range attempts {
		wg.Go(func() {
			codes[i] = withdraw(t, router, token, orderNumberFirst, "200").Code
		})
	}

	wg.Wait()

	for i, code := range codes {
		if code != http.StatusOK {
			t.Errorf("конкурентный повтор %d завершился кодом %d, ожидался %d", i, code, http.StatusOK)
		}
	}

	requireBalanceResponse(t, router, token, 800, 200)

	history := listWithdrawals(t, router, token)
	payload := decodeWithdrawals(t, history)

	if len(payload) != 1 {
		t.Fatalf("повторы создали лишние записи: got %d, want 1", len(payload))
	}

	if payload[0]["sum"] != float64(200) {
		t.Errorf("повтор подменил сумму первого списания: got %v, want 200", payload[0]["sum"])
	}
}

// TestEndToEndWithdrawAcceptsHypotheticalOrderNumber закрепляет сценарии
// «Номер заказа не загружен ни одним пользователем» и «Номер заказа
// принадлежит другому пользователю».
func TestEndToEndWithdrawAcceptsHypotheticalOrderNumber(t *testing.T) {
	router, pool := newBalanceRouter(t)
	owner := registerOrderUser(t, router, "gopher")
	stranger := registerOrderUser(t, router, "stranger")

	creditUser(t, pool, "gopher", "1000")

	// Номер, который не загружал никто.
	if recorder := withdraw(t, router, owner, orderNumberFirst, "100"); recorder.Code != http.StatusOK {
		t.Fatalf("списание по незагруженному номеру: got %d, want %d", recorder.Code, http.StatusOK)
	}

	if countOrders(t, pool) != 0 {
		t.Error("списание создало заказ")
	}

	// Номер, загруженный другим пользователем.
	if recorder := uploadOrder(t, router, stranger, orderNumberSecond); recorder.Code != http.StatusAccepted {
		t.Fatalf("загрузка заказа другим пользователем: got %d, want %d", recorder.Code, http.StatusAccepted)
	}

	if recorder := withdraw(t, router, owner, orderNumberSecond, "100"); recorder.Code != http.StatusOK {
		t.Fatalf("списание по чужому номеру: got %d, want %d", recorder.Code, http.StatusOK)
	}

	requireBalanceResponse(t, router, owner, 800, 200)

	// Владелец и состояние расчёта существующего заказа не изменились.
	orders := listOrders(t, router, stranger)
	if orders.Code != http.StatusOK {
		t.Fatalf("неожиданный код ответа списка заказов: got %d, want %d", orders.Code, http.StatusOK)
	}

	payload := decodeOrders(t, orders)

	if len(payload) != 1 || payload[0]["number"] != orderNumberSecond {
		t.Fatalf("заказ другого пользователя изменился: %s", orders.Body)
	}

	if payload[0]["status"] != "NEW" {
		t.Errorf("состояние расчёта изменилось: %v", payload[0]["status"])
	}

	if _, ok := payload[0]["accrual"]; ok {
		t.Errorf("у заказа появилось начисление: %s", orders.Body)
	}
}

// TestEndToEndWithdrawalsAreIndependentBetweenUsers закрепляет сценарий
// «Списание другого пользователя по тому же номеру»: уникальность действует в
// пределах пользователя.
func TestEndToEndWithdrawalsAreIndependentBetweenUsers(t *testing.T) {
	router, pool := newBalanceRouter(t)
	first := registerOrderUser(t, router, "gopher")
	second := registerOrderUser(t, router, "stranger")

	creditUser(t, pool, "gopher", "500")
	creditUser(t, pool, "stranger", "500")

	for _, token := range []string{first, second} {
		if recorder := withdraw(t, router, token, orderNumberFirst, "100"); recorder.Code != http.StatusOK {
			t.Fatalf("списание отклонено: got %d, want %d", recorder.Code, http.StatusOK)
		}
	}

	requireBalanceResponse(t, router, first, 400, 100)
	requireBalanceResponse(t, router, second, 400, 100)

	for _, token := range []string{first, second} {
		payload := decodeWithdrawals(t, listWithdrawals(t, router, token))

		if len(payload) != 1 {
			t.Errorf("неожиданное число списаний в истории: got %d, want 1", len(payload))
		}
	}
}

// TestEndToEndWithdrawalsHistoryIsIsolated закрепляет требование «История
// списаний»: порядок от новых к старым, изоляция и 204 без тела.
func TestEndToEndWithdrawalsHistoryIsIsolated(t *testing.T) {
	router, pool := newBalanceRouter(t)
	owner := registerOrderUser(t, router, "gopher")
	stranger := registerOrderUser(t, router, "stranger")

	creditUser(t, pool, "gopher", "500")

	numbers := []string{orderNumberFirst, orderNumberSecond, orderNumberThird}
	for _, number := range numbers {
		if recorder := withdraw(t, router, owner, number, "100"); recorder.Code != http.StatusOK {
			t.Fatalf("списание %s отклонено: got %d, want %d", number, recorder.Code, http.StatusOK)
		}
	}

	payload := decodeWithdrawals(t, listWithdrawals(t, router, owner))

	if len(payload) != len(numbers) {
		t.Fatalf("неожиданное число списаний: got %d, want %d", len(payload), len(numbers))
	}

	// История упорядочена от новых к старым, поэтому последний загруженный
	// номер идёт первым.
	if payload[0]["order"] != orderNumberThird {
		t.Errorf("нарушен порядок от новых к старым: первым идёт %v", payload[0]["order"])
	}

	for i := 1; i < len(payload); i++ {
		previous, _ := payload[i-1]["processed_at"].(string)
		current, _ := payload[i]["processed_at"].(string)

		if previous < current {
			t.Errorf("нарушен порядок времени списания: %s предшествует %s", previous, current)
		}
	}

	recorder := listWithdrawals(t, router, stranger)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("неожиданный код ответа для пользователя без списаний: got %d, want %d",
			recorder.Code, http.StatusNoContent)
	}

	if body := recorder.Body.String(); body != "" {
		t.Errorf("ответ 204 содержит тело: %q", body)
	}
}

// TestEndToEndWithdrawRejectsExcessivePrecision закрепляет отказ по суммам,
// которые колонка NUMERIC(18,2) округлила бы молча.
//
// Каждое значение подобрано под свой способ навредить, если проверки нет:
// 0.001 округляется до нуля и нарушает ограничение положительности уже внутри
// транзакции, превращая ошибку клиента в код 500; 0.005 округляется в разные
// стороны в двух слагаемых одного оператора, из-за чего остаток остаётся
// прежним, а сумма списаний растёт на копейку; 1.999 молча превращается в
// 2.00, и клиент видит в истории не ту сумму, которую отправил.
//
// Тест проверяет не только код ответа, но и то, что счёт после серии отказов
// сошёлся: сумма остатка и суммы списаний равна начисленной.
func TestEndToEndWithdrawRejectsExcessivePrecision(t *testing.T) {
	router, pool := newBalanceRouter(t)
	token := registerOrderUser(t, router, "gopher")

	creditUser(t, pool, "gopher", "100")

	sums := []string{"0.001", "0.005", "1.999", "100.123", "0.0000001"}

	for _, sum := range sums {
		t.Run(sum, func(t *testing.T) {
			recorder := withdraw(t, router, token, orderNumberFirst, sum)

			if recorder.Code != http.StatusBadRequest {
				t.Errorf("неожиданный код ответа: got %d, want %d", recorder.Code, http.StatusBadRequest)
			}
		})
	}

	t.Run("quoted sum", func(t *testing.T) {
		recorder := withdrawRawJSON(t, router, token,
			`{"order":"`+orderNumberSecond+`","sum":"100.123"}`)

		if recorder.Code != http.StatusBadRequest {
			t.Errorf("неожиданный код ответа: got %d, want %d", recorder.Code, http.StatusBadRequest)
		}
	})

	// Ни один отказ не изменил счёт и не оставил записи в истории.
	requireBalanceResponse(t, router, token, 100, 0)

	if history := listWithdrawals(t, router, token); history.Code != http.StatusNoContent {
		t.Errorf("отклонённое списание попало в историю: код %d", history.Code)
	}
}

// TestEndToEndWithdrawAcceptsRepresentableSums закрепляет, что проверка
// точности не отвергает суммы, точно представимые двумя знаками после
// запятой, включая запись с избыточными нулями в дробной части.
func TestEndToEndWithdrawAcceptsRepresentableSums(t *testing.T) {
	router, pool := newBalanceRouter(t)
	token := registerOrderUser(t, router, "gopher")

	creditUser(t, pool, "gopher", "100")

	numbers := []string{orderNumberFirst, orderNumberSecond, orderNumberThird}
	sums := []string{"0.01", "1.000", "2.5"}

	for i, sum := range sums {
		if recorder := withdraw(t, router, token, numbers[i], sum); recorder.Code != http.StatusOK {
			t.Fatalf("сумма %s отвергнута: got %d, want %d", sum, recorder.Code, http.StatusOK)
		}
	}

	// 0.01 + 1.000 + 2.5 = 3.51, и счёт обязан сойтись до копейки.
	requireBalanceResponse(t, router, token, 96.49, 3.51)
}
