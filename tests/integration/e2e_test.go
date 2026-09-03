package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"gophermart/internal/app"
	"gophermart/internal/auth"
	"gophermart/tests/testutil"
)

// doJSONRequest выполняет HTTP-запрос с JSON-телом и заголовком авторизации.
func doJSONRequest(t *testing.T, method, targetURL, token string, body any) *http.Response {
	t.Helper()

	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("сериализация тела запроса %s %s: %v", method, targetURL, err)
		}

		reader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(t.Context(), method, targetURL, reader)
	if err != nil {
		t.Fatalf("создание запроса %s %s: %v", method, targetURL, err)
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("выполнение запроса %s %s: %v", method, targetURL, err)
	}

	t.Cleanup(func() {
		_ = resp.Body.Close()
	})

	return resp
}

// postTextRequest выполняет HTTP POST-запрос с текстовым телом (text/plain).
func postTextRequest(t *testing.T, targetURL, token, text string) *http.Response {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, targetURL, strings.NewReader(text))
	if err != nil {
		t.Fatalf("создание запроса POST %s: %v", targetURL, err)
	}

	req.Header.Set("Content-Type", "text/plain")

	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("выполнение запроса POST %s: %v", targetURL, err)
	}

	t.Cleanup(func() {
		_ = resp.Body.Close()
	})

	return resp
}

// extractBearerToken извлекает токен авторизации из заголовка ответа.
func extractBearerToken(t *testing.T, resp *http.Response) string {
	t.Helper()

	authHeader := resp.Header.Get("Authorization")
	if token, ok := strings.CutPrefix(authHeader, "Bearer "); ok && strings.TrimSpace(token) != "" {
		return strings.TrimSpace(token)
	}

	for _, cookie := range resp.Cookies() {
		if cookie.Name == "token" && cookie.Value != "" {
			return cookie.Value
		}
	}

	t.Fatalf("заголовок Authorization или cookie с токеном отсутствуют в ответе: %v", resp.Header)

	return ""
}

// readJSONBody считывает и разбирает JSON-ответ в произвольную структуру или карту.
func readJSONBody[T any](t *testing.T, resp *http.Response) T {
	t.Helper()

	var result T
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("чтение тела ответа: %v", err)
	}

	if err = json.Unmarshal(data, &result); err != nil {
		t.Fatalf("разбор JSON-ответа (%s): %v", string(data), err)
	}

	return result
}

// checkOrderProcessed проверяет, появился ли заказ со статусом PROCESSED в списке.
func checkOrderProcessed(orders []map[string]any, orderNumber string) (map[string]any, bool) {
	for _, o := range orders {
		if o["number"] == orderNumber && o["status"] == "PROCESSED" {
			return o, true
		}
	}

	return nil, false
}

// checkOrderInvalid проверяет, появился ли заказ со статусом INVALID в списке.
func checkOrderInvalid(orders []map[string]any, orderNumber string) (map[string]any, bool) {
	for _, o := range orders {
		if o["number"] == orderNumber && o["status"] == "INVALID" {
			return o, true
		}
	}

	return nil, false
}

// awaitOrderInState ожидает появление заказа в требуемом состоянии через опрос GET /api/user/orders.
func awaitOrderInState(
	t *testing.T,
	baseURL, token, orderNumber string,
	checker func([]map[string]any, string) (map[string]any, bool),
) map[string]any {
	t.Helper()

	pollCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	for {
		if pollCtx.Err() != nil {
			t.Fatalf("таймаут ожидания перехода заказа %s в целевой статус", orderNumber)
		}

		resp := doJSONRequest(t, http.MethodGet, baseURL+"/api/user/orders", token, nil)
		if resp.StatusCode == http.StatusOK {
			orders := readJSONBody[[]map[string]any](t, resp)
			if found, ok := checker(orders, orderNumber); ok {
				return found
			}
		}

		time.Sleep(20 * time.Millisecond)
	}
}

// awaitUserBalance ожидает установления ожидаемого баланса пользователя.
func awaitUserBalance(t *testing.T, baseURL, token string, current, withdrawn float64) {
	t.Helper()

	pollCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	for {
		if pollCtx.Err() != nil {
			t.Fatalf("таймаут ожидания баланса current=%v, withdrawn=%v", current, withdrawn)
		}

		resp := doJSONRequest(t, http.MethodGet, baseURL+"/api/user/balance", token, nil)
		if resp.StatusCode == http.StatusOK {
			b := readJSONBody[map[string]any](t, resp)
			if b["current"] == current && b["withdrawn"] == withdrawn {
				return
			}
		}

		time.Sleep(20 * time.Millisecond)
	}
}

// verifyUnauthorizedRoutes проверяет отказ в доступе (401) для всех защищённых эндпоинтов.
func verifyUnauthorizedRoutes(t *testing.T, baseURL string) {
	t.Helper()

	routes := []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/api/user/orders"},
		{method: http.MethodGet, path: "/api/user/balance"},
		{method: http.MethodPost, path: "/api/user/balance/withdraw"},
		{method: http.MethodGet, path: "/api/user/withdrawals"},
		{method: http.MethodPost, path: "/api/user/orders"},
	}

	for _, tc := range routes {
		resp := doJSONRequest(t, tc.method, baseURL+tc.path, "", nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("маршрут %s %s без авторизации: got status %d, want 401",
				tc.method, tc.path, resp.StatusCode)
		}
	}
}

// runRegisterUsers регистрирует пользователей Alice и Bob и проверяет ошибки валидации.
func runRegisterUsers(t *testing.T, baseURL string) (string, string) {
	t.Helper()

	resp := doJSONRequest(t, http.MethodPost, baseURL+"/api/user/register", "", map[string]string{})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("пустая регистрация: got status %d, want 400", resp.StatusCode)
	}

	resp = doJSONRequest(t, http.MethodPost, baseURL+"/api/user/register", "", map[string]string{
		"login":    "alice",
		"password": "alicepassword123",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("регистрация alice: got status %d, want 200", resp.StatusCode)
	}
	aliceToken := extractBearerToken(t, resp)

	resp = doJSONRequest(t, http.MethodPost, baseURL+"/api/user/register", "", map[string]string{
		"login":    "alice",
		"password": "anotherpassword",
	})
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("конфликт регистрации alice: got status %d, want 409", resp.StatusCode)
	}

	resp = doJSONRequest(t, http.MethodPost, baseURL+"/api/user/login", "", map[string]string{
		"login":    "alice",
		"password": "wrongpassword",
	})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("логин с неверным паролем: got status %d, want 401", resp.StatusCode)
	}

	resp = doJSONRequest(t, http.MethodPost, baseURL+"/api/user/register", "", map[string]string{
		"login":    "bob",
		"password": "bobpassword123",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("регистрация bob: got status %d, want 200", resp.StatusCode)
	}
	bobToken := extractBearerToken(t, resp)

	return aliceToken, bobToken
}

// runCheckInitialState проверяет нулевое начальное состояние пользователя.
func runCheckInitialState(t *testing.T, baseURL, token string) {
	t.Helper()

	resp := doJSONRequest(t, http.MethodGet, baseURL+"/api/user/balance", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("получение баланса: got status %d, want 200", resp.StatusCode)
	}
	balanceData := readJSONBody[map[string]any](t, resp)
	if balanceData["current"] != float64(0) || balanceData["withdrawn"] != float64(0) {
		t.Errorf("начальный баланс: got %v, want current=0, withdrawn=0", balanceData)
	}

	resp = doJSONRequest(t, http.MethodGet, baseURL+"/api/user/orders", token, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("начальный список заказов: got status %d, want 204", resp.StatusCode)
	}

	resp = doJSONRequest(t, http.MethodGet, baseURL+"/api/user/withdrawals", token, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("начальный список списаний: got status %d, want 204", resp.StatusCode)
	}
}

// runUploadOrdersAndAccrual проверяет загрузку заказов и асинхронный расчёт начислений.
func runUploadOrdersAndAccrual(
	t *testing.T,
	baseURL, aliceToken, bobToken string,
	fakeAccrual *fakeAccrualSystem,
) {
	t.Helper()

	const order1 = "9278327649"

	resp := postTextRequest(t, baseURL+"/api/user/orders", aliceToken, "9278923471")
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("невалидный номер заказа по Луну: got status %d, want 422", resp.StatusCode)
	}

	resp = postTextRequest(t, baseURL+"/api/user/orders", aliceToken, "   ")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("пустой номер заказа: got status %d, want 400", resp.StatusCode)
	}

	fakeAccrual.respondProcessed(order1, "750.50")

	resp = postTextRequest(t, baseURL+"/api/user/orders", aliceToken, order1)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("загрузка заказа %s: got status %d, want 202", order1, resp.StatusCode)
	}

	resp = postTextRequest(t, baseURL+"/api/user/orders", aliceToken, order1)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("повторная загрузка заказа %s тем же пользователем: got %d", order1, resp.StatusCode)
	}

	resp = postTextRequest(t, baseURL+"/api/user/orders", bobToken, order1)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("загрузка чужого заказа %s: got %d, want 409", order1, resp.StatusCode)
	}

	targetOrder := awaitOrderInState(t, baseURL, aliceToken, order1, checkOrderProcessed)
	if targetOrder["accrual"] != float64(750.5) {
		t.Errorf("начисленные баллы: got %v, want 750.5", targetOrder["accrual"])
	}

	awaitUserBalance(t, baseURL, aliceToken, 750.5, 0)
}

// runWithdrawAndCheckHistory проверяет списание баллов и сохранение в истории.
func runWithdrawAndCheckHistory(t *testing.T, baseURL, aliceToken string) {
	t.Helper()

	const orderWithdrawal = "79927398713"

	resp := doJSONRequest(t, http.MethodPost, baseURL+"/api/user/balance/withdraw", aliceToken, map[string]any{
		"order": "9278923471",
		"sum":   100,
	})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("списание по невалидному номеру заказа: got status %d, want 422", resp.StatusCode)
	}

	resp = doJSONRequest(t, http.MethodPost, baseURL+"/api/user/balance/withdraw", aliceToken, map[string]any{
		"order": orderWithdrawal,
		"sum":   0,
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("списание нулевой суммы: got status %d, want 400", resp.StatusCode)
	}

	resp = doJSONRequest(t, http.MethodPost, baseURL+"/api/user/balance/withdraw", aliceToken, map[string]any{
		"order": orderWithdrawal,
		"sum":   1000,
	})
	if resp.StatusCode != http.StatusPaymentRequired {
		t.Errorf("списание свыше баланса: got status %d, want 402", resp.StatusCode)
	}

	resp = doJSONRequest(t, http.MethodPost, baseURL+"/api/user/balance/withdraw", aliceToken, map[string]any{
		"order": orderWithdrawal,
		"sum":   250.5,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("успешное списание 250.5: got status %d, want 200", resp.StatusCode)
	}

	awaitUserBalance(t, baseURL, aliceToken, 500, 250.5)

	resp = doJSONRequest(t, http.MethodGet, baseURL+"/api/user/withdrawals", aliceToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("получение истории списаний: got status %d, want 200", resp.StatusCode)
	}
	withdrawalsList := readJSONBody[[]map[string]any](t, resp)
	if len(withdrawalsList) != 1 {
		t.Fatalf("число списаний: got %d, want 1", len(withdrawalsList))
	}
	if withdrawalsList[0]["order"] != orderWithdrawal || withdrawalsList[0]["sum"] != float64(250.5) {
		t.Errorf("запись списания: got %v", withdrawalsList[0])
	}
}

// runInvalidOrderAndIsolation проверяет обработку статуса INVALID и изоляцию данных между пользователями.
func runInvalidOrderAndIsolation(
	t *testing.T,
	baseURL, aliceToken, bobToken string,
	fakeAccrual *fakeAccrualSystem,
) {
	t.Helper()

	const orderInvalid = "6011111111111117"
	fakeAccrual.respondStatus(orderInvalid, "INVALID")

	resp := postTextRequest(t, baseURL+"/api/user/orders", aliceToken, orderInvalid)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("загрузка заказа %s: got status %d, want 202", orderInvalid, resp.StatusCode)
	}

	invalidOrder := awaitOrderInState(t, baseURL, aliceToken, orderInvalid, checkOrderInvalid)
	if _, hasAccrual := invalidOrder["accrual"]; hasAccrual {
		t.Errorf("у заказа со статусом INVALID присутствует accrual: %v", invalidOrder)
	}

	const orderBob = "4561261212345467"
	fakeAccrual.respondProcessed(orderBob, "100")

	resp = postTextRequest(t, baseURL+"/api/user/orders", bobToken, orderBob)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("загрузка заказа bob %s: got status %d, want 202", orderBob, resp.StatusCode)
	}

	awaitUserBalance(t, baseURL, bobToken, 100, 0)
	awaitUserBalance(t, baseURL, aliceToken, 500, 250.5)
}

// TestEndToEndCompleteUserJourney проверяет полный сквозной жизненный цикл
// приложения через реальный сетевой стек: запуск сервиса через app.Main,
// регистрацию и аутентификацию пользователей, валидацию входных данных,
// загрузку заказов по алгоритму Луна, асинхронный расчёт начислений
// фоновым воркером через внешнюю систему, проверку баланса, проведение
// списаний и проверку истории операций.
func TestEndToEndCompleteUserJourney(t *testing.T) {
	dsn := testutil.NewDatabase(t)
	fakeAccrual := newFakeAccrualSystem(t)

	env := map[string]string{
		app.EnvDatabaseURI:           dsn,
		app.EnvAccrualSystemAddress:  fakeAccrual.server.URL,
		app.EnvJWTSecret:             "e2e-super-secret-key-for-test",
		app.EnvPasswordHashCost:      strconv.Itoa(auth.MinCost),
		app.EnvAccrualPollInterval:   "10ms",
		app.EnvAccrualLeaseDuration:  "2s",
		app.EnvAccrualRequestTimeout: "1s",
		app.EnvAccrualBackoffBase:    "10ms",
		app.EnvAccrualBackoffCap:     "50ms",
	}

	baseURL, _, stop := runService(t, env)
	t.Cleanup(func() {
		_ = stop()
	})

	t.Run("404 на неизвестный маршрут", func(t *testing.T) {
		resp := doJSONRequest(t, http.MethodGet, baseURL+"/api/unknown", "", nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("неизвестный маршрут: got status %d, want 404", resp.StatusCode)
		}
	})

	t.Run("Проверка авторизации и доступа", func(t *testing.T) {
		verifyUnauthorizedRoutes(t, baseURL)
	})

	var aliceToken, bobToken string

	t.Run("Регистрация и аутентификация пользователей", func(t *testing.T) {
		aliceToken, bobToken = runRegisterUsers(t, baseURL)
	})

	t.Run("Начальное состояние пользователя", func(t *testing.T) {
		runCheckInitialState(t, baseURL, aliceToken)
	})

	t.Run("Загрузка заказов и начисление баллов", func(t *testing.T) {
		runUploadOrdersAndAccrual(t, baseURL, aliceToken, bobToken, fakeAccrual)
	})

	t.Run("Списание баллов и проверка истории", func(t *testing.T) {
		runWithdrawAndCheckHistory(t, baseURL, aliceToken)
	})

	t.Run("Обработка статуса INVALID и изоляция пользователей", func(t *testing.T) {
		runInvalidOrderAndIsolation(t, baseURL, aliceToken, bobToken, fakeAccrual)
	})
}
