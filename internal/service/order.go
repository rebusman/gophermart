package service

import (
	"context"
	"fmt"

	"gophermart/internal/domain"
	"gophermart/internal/repository"
)

// Orders реализует сценарии загрузки номера заказа и выдачи списка заказов
type Orders struct {
	orders repository.OrderRepository
}

// NewOrders создаёт сервис заказов.
func NewOrders(orders repository.OrderRepository) *Orders {
	return &Orders{orders: orders}
}

// Upload закрепляет номер заказа за пользователем и сообщает исход.
func (s *Orders) Upload(
	ctx context.Context,
	number domain.OrderNumber,
	userID domain.UserID,
) (domain.OrderUpload, error) {
	order := domain.Order{
		Number: number,
		UserID: userID,
		Status: domain.OrderStatusNew,
	}

	created, err := s.orders.CreateOrder(ctx, order)
	if err != nil {
		return domain.OrderUploadUnknown, fmt.Errorf("создание заказа: %w", err)
	}

	if created {
		return domain.OrderUploadAccepted, nil
	}

	owner, err := s.orders.OrderOwner(ctx, number)
	if err != nil {
		return domain.OrderUploadUnknown, fmt.Errorf("определение владельца заказа: %w", err)
	}

	if owner != userID {
		return domain.OrderUploadUnknown, fmt.Errorf(
			"загрузка занятого номера: %w", domain.ErrOrderBelongsToAnotherUser)
	}

	return domain.OrderUploadDuplicate, nil
}

// List возвращает заказы пользователя от самых новых к самым старым.
func (s *Orders) List(ctx context.Context, userID domain.UserID) ([]domain.Order, error) {
	orders, err := s.orders.OrdersByUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("чтение заказов пользователя: %w", err)
	}

	return orders, nil
}
