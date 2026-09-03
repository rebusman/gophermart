package handlers_test

import (
	"context"
	"encoding/json"
	"fmt"
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

// Параметры, общие для тестов обработчиков заказов.
const (
	// validOrderNumber — номер заказа, проходящий проверку алгоритмом Луна.
	validOrderNumber = "9278923470"

	// ordersPath — путь обоих маршрутов заказов.
	ordersPath = "/api/user/orders"
)

// orderServiceStub подменяет прикладной сервис заказов в тестах обработчиков.
type orderServiceStub struct {
	outcome   domain.OrderUpload
	uploadErr error

	orders  []domain.Order
	listErr error

	uploadCalls  int
	gotNumber    domain.OrderNumber
	gotUploadFor domain.UserID
	gotListFor   domain.UserID
}

func (s *orderServiceStub) Upload(
	_ context.Context,
	number domain.OrderNumber,
	userID domain.UserID,
) (domain.OrderUpload, error) {
	s.uploadCalls++
	s.gotNumber = number
	s.gotUploadFor = userID

	if s.uploadErr != nil {
		return domain.OrderUploadUnknown, s.uploadErr
	}

	return s.outcome, nil
}

func (s *orderServiceStub) List(_ context.Context, userID domain.UserID) ([]domain.Order, error) {
	s.gotListFor = userID

	if s.listErr != nil {
		return nil, s.listErr
	}

	return s.orders, nil
}

// doOrderRequest выполняет запрос к обработчику от имени аутентифицированного
// пользователя.
//
// Идентификатор кладётся в контекст тем же способом, каким его кладёт сквозной
// обработчик проверки токена: другого источника у обработчика нет.
func doOrderRequest(
	t *testing.T,
	handler http.HandlerFunc,
	method, body string,
	userID domain.UserID,
) *httptest.ResponseRecorder {
	t.Helper()

	request := httptest.NewRequest(method, ordersPath, strings.NewReader(body))
	request = request.WithContext(middleware.ContextWithUserID(request.Context(), userID))

	recorder := httptest.NewRecorder()
	handler(recorder, request)

	return recorder
}

// TestOrdersUploadAcceptsNewNumber закрепляет сценарий «Новый номер принят в
// обработку»: ответ 202, а сервис получил разобранный номер.
func TestOrdersUploadAcceptsNewNumber(t *testing.T) {
	service := &orderServiceStub{outcome: domain.OrderUploadAccepted}
	userID := domain.NewUserID()

	recorder := doOrderRequest(t, handlers.NewOrders(service).Upload,
		http.MethodPost, validOrderNumber+"\n", userID)

	if recorder.Code != http.StatusAccepted {
		t.Errorf("неожиданный код ответа: got %d, want %d", recorder.Code, http.StatusAccepted)
	}

	if s := service.gotNumber.String(); s != validOrderNumber {
		t.Errorf("сервис получил неожиданный номер: got %q, want %q", s, validOrderNumber)
	}
}

// TestOrdersUploadReturnsOKForOwnNumber закрепляет сценарий «Повторная
// загрузка своего номера»: ответ 200 вместо 202.
func TestOrdersUploadReturnsOKForOwnNumber(t *testing.T) {
	service := &orderServiceStub{outcome: domain.OrderUploadDuplicate}

	recorder := doOrderRequest(t, handlers.NewOrders(service).Upload,
		http.MethodPost, validOrderNumber, domain.NewUserID())

	if recorder.Code != http.StatusOK {
		t.Errorf("неожиданный код ответа: got %d, want %d", recorder.Code, http.StatusOK)
	}
}

// TestOrdersUploadReturnsConflictForForeignNumber закрепляет сценарий
// «Загрузка чужого номера» и сценарий «Отказ по чужому номеру»: ответ 409, и
// он не раскрывает владельца.
func TestOrdersUploadReturnsConflictForForeignNumber(t *testing.T) {
	owner := domain.NewUserID()

	// Ошибка обёрнута контекстом: обработчик обязан распознавать её через
	// errors.Is, а не сравнением значений.
	service := &orderServiceStub{
		uploadErr: fmt.Errorf("загрузка занятого номера: %w", domain.ErrOrderBelongsToAnotherUser),
	}

	recorder := doOrderRequest(t, handlers.NewOrders(service).Upload,
		http.MethodPost, validOrderNumber, domain.NewUserID())

	if recorder.Code != http.StatusConflict {
		t.Errorf("неожиданный код ответа: got %d, want %d", recorder.Code, http.StatusConflict)
	}

	body := recorder.Body.String()

	if strings.Contains(body, owner.String()) || strings.Contains(body, validOrderNumber) {
		t.Errorf("ответ раскрывает владельца или номер: %s", body)
	}
}

// TestOrdersUploadRejectsUnknownOutcome закрепляет, что исход, которого
// сервис возвращать не должен, не превращается в успешный ответ:
// рассогласование слоёв даёт 500, а не 200 или 202.
func TestOrdersUploadRejectsUnknownOutcome(t *testing.T) {
	for _, outcome := range []domain.OrderUpload{domain.OrderUploadUnknown, domain.OrderUpload(42)} {
		service := &orderServiceStub{outcome: outcome}

		recorder := doOrderRequest(t, handlers.NewOrders(service).Upload,
			http.MethodPost, validOrderNumber, domain.NewUserID())

		if recorder.Code != http.StatusInternalServerError {
			t.Errorf("исход %d: неожиданный код ответа: got %d, want %d",
				int(outcome), recorder.Code, http.StatusInternalServerError)
		}
	}
}

// TestOrdersUploadRejectsMalformedNumber закрепляет сценарии «Номер не
// проходит алгоритм Луна» и «Номер содержит нецифровые символы»: ответ 422, а
// сервис не вызывается.
func TestOrdersUploadRejectsMalformedNumber(t *testing.T) {
	tests := map[string]string{
		"не проходит алгоритм Луна": "9278923471",
		"буквы вместо цифр":         "заказ",
		"цифры с буквой":            "12345678a03",
		"внутренний пробел":         "1234 5678903",
		"дробное значение":          "1234567890.3",
	}

	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			service := &orderServiceStub{outcome: domain.OrderUploadAccepted}

			recorder := doOrderRequest(t, handlers.NewOrders(service).Upload,
				http.MethodPost, body, domain.NewUserID())

			if recorder.Code != http.StatusUnprocessableEntity {
				t.Errorf("неожиданный код ответа: got %d, want %d", recorder.Code, http.StatusUnprocessableEntity)
			}

			if service.uploadCalls != 0 {
				t.Errorf("непроверенный номер дошёл до сервиса: %d вызовов", service.uploadCalls)
			}
		})
	}
}

// TestOrdersUploadRejectsEmptyBody закрепляет сценарий «Пустое тело запроса»:
// ответ 400, заказ не создаётся.
func TestOrdersUploadRejectsEmptyBody(t *testing.T) {
	for _, body := range []string{"", "\n", "   \t\r\n"} {
		service := &orderServiceStub{outcome: domain.OrderUploadAccepted}

		recorder := doOrderRequest(t, handlers.NewOrders(service).Upload,
			http.MethodPost, body, domain.NewUserID())

		if recorder.Code != http.StatusBadRequest {
			t.Errorf("тело %q: неожиданный код ответа: got %d, want %d",
				body, recorder.Code, http.StatusBadRequest)
		}

		if service.uploadCalls != 0 {
			t.Errorf("тело %q дошло до сервиса: %d вызовов", body, service.uploadCalls)
		}
	}
}

// TestOrdersUploadRejectsOversizedBody закрепляет сценарий «Тело запроса
// превышает предел»: ответ 413, заказ не создаётся.
func TestOrdersUploadRejectsOversizedBody(t *testing.T) {
	const limit = 8

	service := &orderServiceStub{outcome: domain.OrderUploadAccepted}
	handler := middleware.BodyLimit(limit)(http.HandlerFunc(handlers.NewOrders(service).Upload))

	request := httptest.NewRequest(http.MethodPost, ordersPath, strings.NewReader(validOrderNumber))
	request = request.WithContext(middleware.ContextWithUserID(request.Context(), domain.NewUserID()))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("неожиданный код ответа: got %d, want %d", recorder.Code, http.StatusRequestEntityTooLarge)
	}

	if service.uploadCalls != 0 {
		t.Errorf("слишком большое тело дошло до сервиса: %d вызовов", service.uploadCalls)
	}
}

// TestOrdersUploadHidesInternalFailure закрепляет сценарий «Внутренняя ошибка
// при загрузке заказа»: ответ 500, а текст ошибки базы данных в тело не
// попадает.
func TestOrdersUploadHidesInternalFailure(t *testing.T) {
	service := &orderServiceStub{uploadErr: errInternal}

	recorder := doOrderRequest(t, handlers.NewOrders(service).Upload,
		http.MethodPost, validOrderNumber, domain.NewUserID())

	if recorder.Code != http.StatusInternalServerError {
		t.Errorf("неожиданный код ответа: got %d, want %d", recorder.Code, http.StatusInternalServerError)
	}

	body := recorder.Body.String()

	for _, secret := range []string{"SQLSTATE", "constraint", "orders", "users_login_unique"} {
		if strings.Contains(body, secret) {
			t.Errorf("тело ответа раскрывает внутреннюю деталь %q: %s", secret, body)
		}
	}
}

// TestOrdersUploadTakesOwnerFromContextOnly закрепляет сценарий «Попытка
// подменить владельца»: заказ закрепляется за владельцем токена, а значения из
// тела, строки запроса и заголовка игнорируются.
func TestOrdersUploadTakesOwnerFromContextOnly(t *testing.T) {
	authenticated := domain.NewUserID()
	impostor := domain.NewUserID()
	service := &orderServiceStub{outcome: domain.OrderUploadAccepted}

	request := httptest.NewRequest(http.MethodPost,
		ordersPath+"?user_id="+impostor.String(), strings.NewReader(validOrderNumber))
	request.Header.Set("X-User-Id", impostor.String())
	request = request.WithContext(middleware.ContextWithUserID(request.Context(), authenticated))

	recorder := httptest.NewRecorder()
	handlers.NewOrders(service).Upload(recorder, request)

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("неожиданный код ответа: got %d, want %d", recorder.Code, http.StatusAccepted)
	}

	if service.gotUploadFor != authenticated {
		t.Errorf("заказ закреплён за подставленным пользователем: got %s, want %s",
			service.gotUploadFor, authenticated)
	}
}

// TestOrdersUploadRequiresAuthenticatedContext закрепляет, что обработчик не
// работает вне группы защищённых маршрутов: без проверенного идентификатора
// заказ не создаётся.
func TestOrdersUploadRequiresAuthenticatedContext(t *testing.T) {
	service := &orderServiceStub{outcome: domain.OrderUploadAccepted}

	request := httptest.NewRequest(http.MethodPost, ordersPath, strings.NewReader(validOrderNumber))
	recorder := httptest.NewRecorder()

	handlers.NewOrders(service).Upload(recorder, request)

	if recorder.Code == http.StatusAccepted {
		t.Error("заказ принят без проверенного идентификатора пользователя")
	}

	if service.uploadCalls != 0 {
		t.Errorf("сервис вызван без проверенного идентификатора: %d вызовов", service.uploadCalls)
	}
}

// TestOrdersListReturnsOrders закрепляет сценарий «У пользователя есть
// заказы»: ответ 200 с массивом в порядке, полученном от сервиса.
func TestOrdersListReturnsOrders(t *testing.T) {
	userID := domain.NewUserID()
	accrual := decimal.RequireFromString("751.5")
	newest := time.Date(2020, time.December, 10, 15, 15, 45, 0, time.UTC)
	service := &orderServiceStub{orders: []domain.Order{
		{
			Number:     domain.OrderNumber(validOrderNumber),
			UserID:     userID,
			Status:     domain.OrderStatusProcessed,
			Accrual:    &accrual,
			UploadedAt: newest,
		},
		{
			Number:     "12345678903",
			UserID:     userID,
			Status:     domain.OrderStatusNew,
			UploadedAt: newest.Add(-time.Hour),
		},
	}}

	recorder := doOrderRequest(t, handlers.NewOrders(service).List,
		http.MethodGet, "", userID)

	if recorder.Code != http.StatusOK {
		t.Fatalf("неожиданный код ответа: got %d, want %d", recorder.Code, http.StatusOK)
	}

	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("неожиданный тип содержимого: got %q, want %q", got, "application/json")
	}

	var payload []map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("ответ не является массивом JSON: %v (%s)", err, recorder.Body.String())
	}

	if len(payload) != 2 {
		t.Fatalf("неожиданное число заказов: got %d, want 2", len(payload))
	}

	if payload[0]["number"] != validOrderNumber {
		t.Errorf("порядок заказов изменён: got %v", payload[0]["number"])
	}

	if payload[0]["accrual"] != 751.5 {
		t.Errorf("начисление передано неверно: got %v", payload[0]["accrual"])
	}

	if _, ok := payload[1]["accrual"]; ok {
		t.Errorf("поле начисления присутствует у заказа без начисления: %v", payload[1])
	}

	if service.gotListFor != userID {
		t.Errorf("список запрошен для чужого пользователя: got %s, want %s", service.gotListFor, userID)
	}
}

// TestOrdersListReturnsNoContentForEmptyList закрепляет сценарий «У
// пользователя нет заказов»: ответ 204 без тела.
func TestOrdersListReturnsNoContentForEmptyList(t *testing.T) {
	recorder := doOrderRequest(t, handlers.NewOrders(&orderServiceStub{}).List,
		http.MethodGet, "", domain.NewUserID())

	if recorder.Code != http.StatusNoContent {
		t.Errorf("неожиданный код ответа: got %d, want %d", recorder.Code, http.StatusNoContent)
	}

	if recorder.Body.Len() != 0 {
		t.Errorf("ответ 204 содержит тело: %s", recorder.Body.String())
	}
}

// TestOrdersListHidesInternalFailure закрепляет требование «Ответы об ошибках
// не раскрывают внутренние детали» для выдачи списка.
func TestOrdersListHidesInternalFailure(t *testing.T) {
	recorder := doOrderRequest(t, handlers.NewOrders(&orderServiceStub{listErr: errInternal}).List,
		http.MethodGet, "", domain.NewUserID())

	if recorder.Code != http.StatusInternalServerError {
		t.Errorf("неожиданный код ответа: got %d, want %d", recorder.Code, http.StatusInternalServerError)
	}

	if body := recorder.Body.String(); strings.Contains(body, "SQLSTATE") {
		t.Errorf("тело ответа раскрывает ошибку базы данных: %s", body)
	}
}

// TestOrdersListRequiresAuthenticatedContext закрепляет, что список не
// выдаётся без проверенного идентификатора пользователя.
func TestOrdersListRequiresAuthenticatedContext(t *testing.T) {
	service := &orderServiceStub{orders: []domain.Order{{Number: validOrderNumber}}}

	recorder := httptest.NewRecorder()
	handlers.NewOrders(service).List(recorder, httptest.NewRequest(http.MethodGet, ordersPath, nil))

	if recorder.Code == http.StatusOK {
		t.Error("список выдан без проверенного идентификатора пользователя")
	}
}
