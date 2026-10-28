package service_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"gophermart/internal/domain"
	"gophermart/internal/repository"
	"gophermart/internal/service"
)

// Проверка на этапе компиляции: конструктор сервиса заказов принимает
// единственную зависимость — хранилище. Клиента внешней системы расчёта
// начислений среди аргументов нет, поэтому обратиться к ней синхронно сервису
// нечем.
var _ func(repository.OrderRepository) *service.Orders = service.NewOrders

// testOrderNumber — номер заказа, проходящий проверку алгоритмом Луна.
const testOrderNumber = "9278923470"

// newOrderNumber разбирает номер заказа для передачи в сервис.
func newOrderNumber(t *testing.T) domain.OrderNumber {
	t.Helper()

	number, err := domain.ParseOrderNumber(testOrderNumber)
	if err != nil {
		t.Fatalf("разбор номера заказа: %v", err)
	}

	return number
}

// TestOrdersUploadAcceptsNewNumber закрепляет исход «создан»: ранее
// неизвестный номер принимается в обработку, а заказ уходит в хранилище в
// состоянии NEW и с владельцем из аргумента.
func TestOrdersUploadAcceptsNewNumber(t *testing.T) {
	repo := &orderRepositoryStub{created: true}
	userID := domain.NewUserID()

	outcome, err := service.NewOrders(repo).Upload(t.Context(), newOrderNumber(t), userID)
	if err != nil {
		t.Fatalf("загрузка номера заказа: %v", err)
	}

	if outcome != domain.OrderUploadAccepted {
		t.Errorf("неожиданный исход: got %s, want %s", outcome, domain.OrderUploadAccepted)
	}

	if len(repo.createdOrders) != 1 {
		t.Fatalf("неожиданное число обращений к хранилищу: got %d, want 1", len(repo.createdOrders))
	}

	order := repo.createdOrders[0]

	if order.Status != domain.OrderStatusNew {
		t.Errorf("заказ создан не в начальном состоянии: got %s, want %s", order.Status, domain.OrderStatusNew)
	}

	if order.UserID != userID {
		t.Errorf("заказ закреплён за чужим пользователем: got %s, want %s", order.UserID, userID)
	}

	if repo.ownerCalls != 0 {
		t.Errorf("владелец выяснялся при создании нового заказа: %d обращений", repo.ownerCalls)
	}
}

// TestOrdersUploadReportsDuplicateForOwnNumber закрепляет исход «уже
// принадлежит этому пользователю»: повторная загрузка своего номера не
// является отказом.
func TestOrdersUploadReportsDuplicateForOwnNumber(t *testing.T) {
	userID := domain.NewUserID()
	repo := &orderRepositoryStub{owner: userID}

	outcome, err := service.NewOrders(repo).Upload(t.Context(), newOrderNumber(t), userID)
	if err != nil {
		t.Fatalf("повторная загрузка своего номера: %v", err)
	}

	if outcome != domain.OrderUploadDuplicate {
		t.Errorf("неожиданный исход: got %s, want %s", outcome, domain.OrderUploadDuplicate)
	}

	if repo.ownerCalls != 1 {
		t.Errorf("владелец занятого номера не выяснялся: %d обращений", repo.ownerCalls)
	}
}

// TestOrdersUploadRejectsForeignNumber закрепляет исход «принадлежит другому
// пользователю»: отказ выражен доменной ошибкой, распознаваемой через
// errors.Is, и не раскрывает владельца.
func TestOrdersUploadRejectsForeignNumber(t *testing.T) {
	repo := &orderRepositoryStub{owner: domain.NewUserID()}

	outcome, err := service.NewOrders(repo).Upload(t.Context(), newOrderNumber(t), domain.NewUserID())
	if !errors.Is(err, domain.ErrOrderBelongsToAnotherUser) {
		t.Fatalf("неожиданная ошибка при загрузке чужого номера: %v", err)
	}

	if outcome != domain.OrderUploadUnknown {
		t.Errorf("при отказе возвращён осмысленный исход: %s", outcome)
	}

	if got := err.Error(); strings.Contains(got, repo.owner.String()) {
		t.Errorf("ошибка раскрывает владельца номера: %s", got)
	}
}

// TestOrdersUploadPropagatesRepositoryFailure закрепляет, что внутренний сбой
// хранилища не подменяется исходом, понятным клиенту.
func TestOrdersUploadPropagatesRepositoryFailure(t *testing.T) {
	tests := map[string]*orderRepositoryStub{
		"сбой при создании заказа":             {createErr: errRepository},
		"сбой при определении владельца":       {ownerErr: errRepository},
		"владелец исчез после занятого номера": {ownerErr: domain.ErrOrderNotFound},
	}

	for name, repo := range tests {
		t.Run(name, func(t *testing.T) {
			outcome, err := service.NewOrders(repo).Upload(t.Context(), newOrderNumber(t), domain.NewUserID())
			if err == nil {
				t.Fatal("сбой хранилища не привёл к ошибке")
			}

			if errors.Is(err, domain.ErrOrderBelongsToAnotherUser) {
				t.Errorf("сбой хранилища подменён отказом по чужому номеру: %v", err)
			}

			if outcome != domain.OrderUploadUnknown {
				t.Errorf("при сбое возвращён осмысленный исход: %s", outcome)
			}
		})
	}
}

// TestOrdersListReturnsUserOrders закрепляет непустой список: заказы
// возвращаются в том порядке, в котором их отдало хранилище.
func TestOrdersListReturnsUserOrders(t *testing.T) {
	userID := domain.NewUserID()
	number := newOrderNumber(t)
	repo := &orderRepositoryStub{
		orders: []domain.Order{{
			Number:     number,
			UserID:     userID,
			Status:     domain.OrderStatusNew,
			UploadedAt: time.Now().UTC(),
		}},
	}

	orders, err := service.NewOrders(repo).List(t.Context(), userID)
	if err != nil {
		t.Fatalf("чтение списка заказов: %v", err)
	}

	if len(orders) != 1 || orders[0].Number != number {
		t.Errorf("неожиданный список заказов: %+v", orders)
	}

	if repo.listedUser != userID {
		t.Errorf("список запрошен для чужого пользователя: got %s, want %s", repo.listedUser, userID)
	}
}

// TestOrdersListDistinguishesEmptyListFromFailure закрепляет, что отсутствие
// заказов не является ошибкой и отличимо от сбоя хранилища.
func TestOrdersListDistinguishesEmptyListFromFailure(t *testing.T) {
	orders, err := service.NewOrders(&orderRepositoryStub{}).List(t.Context(), domain.NewUserID())
	if err != nil {
		t.Fatalf("чтение пустого списка: %v", err)
	}

	if len(orders) != 0 {
		t.Errorf("пустой список оказался непустым: %+v", orders)
	}

	if _, err = service.NewOrders(&orderRepositoryStub{listErr: errRepository}).
		List(t.Context(), domain.NewUserID()); !errors.Is(err, errRepository) {
		t.Errorf("сбой хранилища не дошёл до вызывающей стороны: %v", err)
	}
}
