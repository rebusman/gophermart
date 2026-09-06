package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"gophermart/internal/domain"
	"gophermart/internal/transport/http/handlers"
	"gophermart/internal/transport/http/middleware"
)

// balancePath — путь маршрута состояния счёта.
//
// Значение используется всеми тестами обработчиков счёта: маршрутизацию
// проверяют тесты маршрутизатора, а обработчик вызывается напрямую, поэтому
// конкретный путь на его поведение не влияет.
const balancePath = "/api/user/balance"

// errBalanceRepository — произвольная внутренняя ошибка, приходящая из
// сервисного слоя.
//
// Её текст намеренно похож на сообщение PostgreSQL: тест убеждается, что
// подобные подробности не попадают в тело ответа.
var errBalanceRepository = errors.New(
	`ERROR: duplicate key value violates unique constraint "withdrawals_pkey" (SQLSTATE 23505)`)

// balanceServiceStub подменяет прикладной сервис счёта в тестах обработчиков.
type balanceServiceStub struct {
	balance    domain.Balance
	balanceErr error

	withdrawErr error

	withdrawals []domain.Withdrawal
	listErr     error

	withdrawCalls int
	gotNumber     domain.OrderNumber
	gotSum        decimal.Decimal
	gotWithdrawer domain.UserID
	gotBalanceFor domain.UserID
	gotListFor    domain.UserID
}

func (s *balanceServiceStub) Balance(_ context.Context, userID domain.UserID) (domain.Balance, error) {
	s.gotBalanceFor = userID

	if s.balanceErr != nil {
		return domain.Balance{}, s.balanceErr
	}

	return s.balance, nil
}

func (s *balanceServiceStub) Withdraw(
	_ context.Context,
	number domain.OrderNumber,
	sum decimal.Decimal,
	userID domain.UserID,
) error {
	s.withdrawCalls++
	s.gotNumber = number
	s.gotSum = sum
	s.gotWithdrawer = userID

	return s.withdrawErr
}

func (s *balanceServiceStub) Withdrawals(
	_ context.Context,
	userID domain.UserID,
) ([]domain.Withdrawal, error) {
	s.gotListFor = userID

	if s.listErr != nil {
		return nil, s.listErr
	}

	return s.withdrawals, nil
}

// doBalanceRequest выполняет запрос к обработчику от имени аутентифицированного
// пользователя.
//
// Идентификатор кладётся в контекст тем же способом, каким его кладёт сквозной
// обработчик проверки токена: другого источника у обработчика нет.
func doBalanceRequest(
	t *testing.T,
	handler http.HandlerFunc,
	method, body string,
	userID domain.UserID,
) *httptest.ResponseRecorder {
	t.Helper()

	request := httptest.NewRequest(method, balancePath, strings.NewReader(body))
	request = request.WithContext(middleware.ContextWithUserID(request.Context(), userID))

	recorder := httptest.NewRecorder()
	handler(recorder, request)

	return recorder
}

// newBalanceHandler собирает обработчик поверх подставного сервиса.
func newBalanceHandler(service *balanceServiceStub) *handlers.Balance {
	return handlers.NewBalance(service)
}

// mustMoney разбирает денежное значение из десятичной строки.
func mustMoney(t *testing.T, value string) decimal.Decimal {
	t.Helper()

	parsed, err := decimal.NewFromString(value)
	if err != nil {
		t.Fatalf("разбор денежного значения %s: %v", value, err)
	}

	return parsed
}

// TestBalanceGetReturnsBothSums закрепляет сценарий «Счёт после списания»:
// ответ 200 содержит обе суммы JSON-числами.
func TestBalanceGetReturnsBothSums(t *testing.T) {
	service := &balanceServiceStub{
		balance: domain.Balance{
			Current:   mustMoney(t, "500.5"),
			Withdrawn: mustMoney(t, "42"),
		},
	}

	recorder := doBalanceRequest(t, newBalanceHandler(service).Get,
		http.MethodGet, "", domain.NewUserID())

	if recorder.Code != http.StatusOK {
		t.Fatalf("неожиданный код ответа: got %d, want %d", recorder.Code, http.StatusOK)
	}

	if got := recorder.Body.String(); got != `{"current":500.5,"withdrawn":42}` {
		t.Errorf("неожиданное тело ответа: %s", got)
	}

	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("неожиданный тип содержимого: %q", got)
	}
}

// TestBalanceGetReturnsZeroBalance закрепляет сценарий «Счёт без операций»:
// пустой счёт даёт 200 с нулями, а не 204.
func TestBalanceGetReturnsZeroBalance(t *testing.T) {
	recorder := doBalanceRequest(t, newBalanceHandler(&balanceServiceStub{}).Get,
		http.MethodGet, "", domain.NewUserID())

	if recorder.Code != http.StatusOK {
		t.Fatalf("неожиданный код ответа: got %d, want %d", recorder.Code, http.StatusOK)
	}

	if got := recorder.Body.String(); got != `{"current":0,"withdrawn":0}` {
		t.Errorf("неожиданное тело ответа: %s", got)
	}
}

// TestBalanceGetReadsOwnerFromContext закрепляет сценарий «Попытка прочитать
// чужой баланс»: владелец берётся из проверенного токена, а не из запроса.
func TestBalanceGetReadsOwnerFromContext(t *testing.T) {
	service := &balanceServiceStub{}
	owner := domain.NewUserID()
	stranger := domain.NewUserID()

	request := httptest.NewRequest(http.MethodGet, balancePath+"?user_id="+stranger.String(), nil)
	request.Header.Set("X-User-Id", stranger.String())
	request = request.WithContext(middleware.ContextWithUserID(request.Context(), owner))

	recorder := httptest.NewRecorder()
	newBalanceHandler(service).Get(recorder, request)

	if service.gotBalanceFor != owner {
		t.Errorf("счёт прочитан для чужого пользователя: got %s, want %s", service.gotBalanceFor, owner)
	}
}

// TestBalanceGetRejectsRequestWithoutAuthenticatedUser закрепляет отказ на
// маршруте, зарегистрированном вне группы защищённых: это ошибка сборки, а не
// запроса, поэтому клиент получает 500.
func TestBalanceGetRejectsRequestWithoutAuthenticatedUser(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, balancePath, nil)
	recorder := httptest.NewRecorder()

	newBalanceHandler(&balanceServiceStub{}).Get(recorder, request)

	if recorder.Code != http.StatusInternalServerError {
		t.Errorf("неожиданный код ответа: got %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
}

// TestBalanceGetHidesInternalDetails закрепляет сценарий «Внутренняя ошибка
// при списании» для чтения счёта: тело ответа не раскрывает сообщение базы.
func TestBalanceGetHidesInternalDetails(t *testing.T) {
	service := &balanceServiceStub{balanceErr: errBalanceRepository}

	recorder := doBalanceRequest(t, newBalanceHandler(service).Get,
		http.MethodGet, "", domain.NewUserID())

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("неожиданный код ответа: got %d, want %d", recorder.Code, http.StatusInternalServerError)
	}

	requireNoInternalDetails(t, recorder.Body.String())
}

// TestBalanceGetReportsMissingBalanceAsInternal закрепляет решение
// «Отсутствие строки счёта — внутренняя ошибка»: ошибка не имеет
// собственного кода ответа.
func TestBalanceGetReportsMissingBalanceAsInternal(t *testing.T) {
	service := &balanceServiceStub{balanceErr: domain.ErrBalanceNotFound}

	recorder := doBalanceRequest(t, newBalanceHandler(service).Get,
		http.MethodGet, "", domain.NewUserID())

	if recorder.Code != http.StatusInternalServerError {
		t.Errorf("неожиданный код ответа: got %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
}

// TestBalanceWithdrawAcceptsValidRequest закрепляет сценарий «Успешное
// списание»: ответ 200, а сервис получил разобранный номер и сумму.
func TestBalanceWithdrawAcceptsValidRequest(t *testing.T) {
	service := &balanceServiceStub{}
	userID := domain.NewUserID()

	recorder := doBalanceRequest(t, newBalanceHandler(service).Withdraw,
		http.MethodPost, `{"order":"`+validOrderNumber+`","sum":751.5}`, userID)

	if recorder.Code != http.StatusOK {
		t.Fatalf("неожиданный код ответа: got %d, want %d", recorder.Code, http.StatusOK)
	}

	if got := service.gotNumber.String(); got != validOrderNumber {
		t.Errorf("сервис получил неожиданный номер: got %q, want %q", got, validOrderNumber)
	}

	if got := service.gotSum.String(); got != "751.5" {
		t.Errorf("сервис получил неожиданную сумму: got %s, want 751.5", got)
	}

	if service.gotWithdrawer != userID {
		t.Errorf("списание отнесено к чужому счёту: got %s, want %s", service.gotWithdrawer, userID)
	}
}

// TestBalanceWithdrawReturnsPaymentRequired закрепляет сценарий «Недостаточно
// баллов»: доменная ошибка отображается в 402.
func TestBalanceWithdrawReturnsPaymentRequired(t *testing.T) {
	service := &balanceServiceStub{withdrawErr: domain.ErrInsufficientFunds}

	recorder := doBalanceRequest(t, newBalanceHandler(service).Withdraw,
		http.MethodPost, `{"order":"`+validOrderNumber+`","sum":100}`, domain.NewUserID())

	if recorder.Code != http.StatusPaymentRequired {
		t.Fatalf("неожиданный код ответа: got %d, want %d", recorder.Code, http.StatusPaymentRequired)
	}

	// Ответ не раскрывает ни остатка, ни недостающей разницы.
	if body := recorder.Body.String(); strings.Contains(body, "100") {
		t.Errorf("ответ раскрывает сумму: %s", body)
	}
}

// TestBalanceWithdrawRejectsInvalidOrderNumber закрепляет сценарии «Номер
// заказа не проходит алгоритм Луна» и «Номер заказа содержит нецифровые
// символы»: оба дают 422, а сервис не вызывается.
func TestBalanceWithdrawRejectsInvalidOrderNumber(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "не проходит Луна", body: `{"order":"9278923471","sum":100}`},
		{name: "нецифровые символы", body: `{"order":"92789a23470","sum":100}`},
		{name: "внутренний пробел", body: `{"order":"92789 23470","sum":100}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &balanceServiceStub{}

			recorder := doBalanceRequest(t, newBalanceHandler(service).Withdraw,
				http.MethodPost, test.body, domain.NewUserID())

			if recorder.Code != http.StatusUnprocessableEntity {
				t.Errorf("неожиданный код ответа: got %d, want %d",
					recorder.Code, http.StatusUnprocessableEntity)
			}

			if service.withdrawCalls != 0 {
				t.Errorf("сервис вызван при неверном номере: %d обращений", service.withdrawCalls)
			}
		})
	}
}

// TestBalanceWithdrawRejectsMalformedRequest закрепляет сценарии «Тело запроса
// не разбирается» и «Сумма списания равна нулю или отрицательна»: оба дают
// 400, а сервис не вызывается.
func TestBalanceWithdrawRejectsMalformedRequest(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "пустое тело", body: ""},
		{name: "не JSON", body: "не json"},
		{name: "номер отсутствует", body: `{"sum":100}`},
		{name: "номер пуст", body: `{"order":"   ","sum":100}`},
		{name: "сумма отсутствует", body: `{"order":"` + validOrderNumber + `"}`},
		{name: "сумма равна нулю", body: `{"order":"` + validOrderNumber + `","sum":0}`},
		{name: "сумма отрицательна", body: `{"order":"` + validOrderNumber + `","sum":-1}`},
		// Суммы точнее двух знаков после запятой: хранилище округлило бы их
		// молча, поэтому они отвергаются как неприемлемые по форме.
		{name: "сумма округляется до нуля", body: `{"order":"` + validOrderNumber + `","sum":0.001}`},
		{name: "сумма рассогласует счёт", body: `{"order":"` + validOrderNumber + `","sum":0.005}`},
		{name: "сумма меняется округлением", body: `{"order":"` + validOrderNumber + `","sum":1.999}`},
		{name: "переточная сумма строкой", body: `{"order":"` + validOrderNumber + `","sum":"100.123"}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &balanceServiceStub{}

			recorder := doBalanceRequest(t, newBalanceHandler(service).Withdraw,
				http.MethodPost, test.body, domain.NewUserID())

			if recorder.Code != http.StatusBadRequest {
				t.Errorf("неожиданный код ответа: got %d, want %d", recorder.Code, http.StatusBadRequest)
			}

			if service.withdrawCalls != 0 {
				t.Errorf("сервис вызван при неприемлемом запросе: %d обращений", service.withdrawCalls)
			}
		})
	}
}

// TestBalanceWithdrawAcceptsRepresentableSums закрепляет, что проверка
// точности не отвергает суммы, точно представимые двумя знаками после
// запятой, включая запись с избыточными нулями.
func TestBalanceWithdrawAcceptsRepresentableSums(t *testing.T) {
	for _, sum := range []string{"1", "1.0", "1.000", "0.01", "751.5"} {
		t.Run(sum, func(t *testing.T) {
			service := &balanceServiceStub{}

			recorder := doBalanceRequest(t, newBalanceHandler(service).Withdraw,
				http.MethodPost, `{"order":"`+validOrderNumber+`","sum":`+sum+`}`, domain.NewUserID())

			if recorder.Code != http.StatusOK {
				t.Errorf("сумма %s отвергнута: got %d, want %d", sum, recorder.Code, http.StatusOK)
			}
		})
	}
}

// TestBalanceWithdrawPrefersFormFailureOverNumberFailure закрепляет решение о
// порядке проверок: запрос, в котором неверны и номер, и сумма, даёт 400, а не
// 422.
func TestBalanceWithdrawPrefersFormFailureOverNumberFailure(t *testing.T) {
	service := &balanceServiceStub{}

	recorder := doBalanceRequest(t, newBalanceHandler(service).Withdraw,
		http.MethodPost, `{"order":"не номер","sum":0}`, domain.NewUserID())

	if recorder.Code != http.StatusBadRequest {
		t.Errorf("неожиданный код ответа: got %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}

// TestBalanceWithdrawRejectsTooLargeBody закрепляет сценарий «Тело запроса
// превышает предел»: ответ 413, а не 400.
func TestBalanceWithdrawRejectsTooLargeBody(t *testing.T) {
	service := &balanceServiceStub{}

	request := httptest.NewRequest(http.MethodPost, balancePath,
		strings.NewReader(`{"order":"`+validOrderNumber+`","sum":100}`))
	request.Body = http.MaxBytesReader(httptest.NewRecorder(), request.Body, 4)
	request = request.WithContext(middleware.ContextWithUserID(request.Context(), domain.NewUserID()))

	recorder := httptest.NewRecorder()
	newBalanceHandler(service).Withdraw(recorder, request)

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("неожиданный код ответа: got %d, want %d",
			recorder.Code, http.StatusRequestEntityTooLarge)
	}

	if service.withdrawCalls != 0 {
		t.Errorf("сервис вызван при превышении предела: %d обращений", service.withdrawCalls)
	}
}

// TestBalanceWithdrawIgnoresClientSuppliedOwner закрепляет сценарий «Попытка
// списать с чужого счёта»: владелец берётся из проверенного токена.
func TestBalanceWithdrawIgnoresClientSuppliedOwner(t *testing.T) {
	service := &balanceServiceStub{}
	owner := domain.NewUserID()
	stranger := domain.NewUserID()

	body := `{"order":"` + validOrderNumber + `","sum":100,"user_id":"` + stranger.String() + `"}`

	request := httptest.NewRequest(http.MethodPost, balancePath, strings.NewReader(body))
	request.Header.Set("X-User-Id", stranger.String())
	request = request.WithContext(middleware.ContextWithUserID(request.Context(), owner))

	recorder := httptest.NewRecorder()
	newBalanceHandler(service).Withdraw(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("неожиданный код ответа: got %d, want %d", recorder.Code, http.StatusOK)
	}

	if service.gotWithdrawer != owner {
		t.Errorf("списание отнесено к чужому счёту: got %s, want %s", service.gotWithdrawer, owner)
	}
}

// TestBalanceWithdrawHidesInternalDetails закрепляет сценарий «Внутренняя
// ошибка при списании»: тело ответа не раскрывает сообщение базы данных.
func TestBalanceWithdrawHidesInternalDetails(t *testing.T) {
	service := &balanceServiceStub{withdrawErr: errBalanceRepository}

	recorder := doBalanceRequest(t, newBalanceHandler(service).Withdraw,
		http.MethodPost, `{"order":"`+validOrderNumber+`","sum":100}`, domain.NewUserID())

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("неожиданный код ответа: got %d, want %d", recorder.Code, http.StatusInternalServerError)
	}

	requireNoInternalDetails(t, recorder.Body.String())
}

// TestBalanceWithdrawalsReturnsHistory закрепляет сценарий «У пользователя
// есть списания»: ответ 200 и массив JSON.
func TestBalanceWithdrawalsReturnsHistory(t *testing.T) {
	number, err := domain.ParseOrderNumber(validOrderNumber)
	if err != nil {
		t.Fatalf("разбор номера заказа: %v", err)
	}

	service := &balanceServiceStub{
		withdrawals: []domain.Withdrawal{
			{
				OrderNumber: number,
				Sum:         mustMoney(t, "500"),
				ProcessedAt: time.Date(2020, time.December, 9, 16, 9, 57, 0, time.UTC),
			},
		},
	}

	recorder := doBalanceRequest(t, newBalanceHandler(service).Withdrawals,
		http.MethodGet, "", domain.NewUserID())

	if recorder.Code != http.StatusOK {
		t.Fatalf("неожиданный код ответа: got %d, want %d", recorder.Code, http.StatusOK)
	}

	var payload []map[string]any
	if err = json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("ответ не является массивом JSON: %v (%s)", err, recorder.Body)
	}

	if len(payload) != 1 {
		t.Fatalf("неожиданное число списаний: got %d, want 1", len(payload))
	}

	if payload[0]["order"] != validOrderNumber {
		t.Errorf("неожиданный номер заказа: %v", payload[0]["order"])
	}

	if payload[0]["processed_at"] != "2020-12-09T16:09:57Z" {
		t.Errorf("неожиданное время списания: %v", payload[0]["processed_at"])
	}
}

// TestBalanceWithdrawalsReturnsNoContentOnEmptyHistory закрепляет сценарий «У
// пользователя нет списаний»: 204 без тела вместо пустого массива.
func TestBalanceWithdrawalsReturnsNoContentOnEmptyHistory(t *testing.T) {
	recorder := doBalanceRequest(t, newBalanceHandler(&balanceServiceStub{}).Withdrawals,
		http.MethodGet, "", domain.NewUserID())

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("неожиданный код ответа: got %d, want %d", recorder.Code, http.StatusNoContent)
	}

	if body := recorder.Body.String(); body != "" {
		t.Errorf("ответ 204 содержит тело: %q", body)
	}
}

// TestBalanceWithdrawalsReadsOwnerFromContext закрепляет требование «Доступ к
// счёту только аутентифицированному пользователю» для истории списаний.
func TestBalanceWithdrawalsReadsOwnerFromContext(t *testing.T) {
	service := &balanceServiceStub{}
	owner := domain.NewUserID()

	doBalanceRequest(t, newBalanceHandler(service).Withdrawals, http.MethodGet, "", owner)

	if service.gotListFor != owner {
		t.Errorf("история прочитана для чужого пользователя: got %s, want %s", service.gotListFor, owner)
	}
}

// TestBalanceWithdrawalsHidesInternalDetails закрепляет требование «Ответы об
// ошибках не раскрывают внутренние детали» для истории списаний.
func TestBalanceWithdrawalsHidesInternalDetails(t *testing.T) {
	service := &balanceServiceStub{listErr: errBalanceRepository}

	recorder := doBalanceRequest(t, newBalanceHandler(service).Withdrawals,
		http.MethodGet, "", domain.NewUserID())

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("неожиданный код ответа: got %d, want %d", recorder.Code, http.StatusInternalServerError)
	}

	requireNoInternalDetails(t, recorder.Body.String())
}

// requireNoInternalDetails проверяет, что тело ответа не содержит внутренних
// подробностей: текста ошибки PostgreSQL, имён таблиц и ограничений.
func requireNoInternalDetails(t *testing.T, body string) {
	t.Helper()

	for _, fragment := range []string{"SQLSTATE", "withdrawals", "balances", "constraint", "ERROR:"} {
		if strings.Contains(body, fragment) {
			t.Errorf("тело ответа раскрывает внутреннюю деталь %q: %s", fragment, body)
		}
	}
}
