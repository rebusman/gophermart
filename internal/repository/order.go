package repository

import (
	"context"

	"gophermart/internal/domain"
)

// OrderRepository хранит заказы, загруженные пользователями для расчёта
type OrderRepository interface {
	// CreateOrder создаёт заказ с номером, владельцем и состоянием расчёта,
	CreateOrder(ctx context.Context, order domain.Order) (bool, error)

	// OrderOwner возвращает владельца заказа с указанным номером.
	OrderOwner(ctx context.Context, number domain.OrderNumber) (domain.UserID, error)

	// OrdersByUser возвращает заказы пользователя, упорядоченные от самых
	OrdersByUser(ctx context.Context, userID domain.UserID) ([]domain.Order, error)
}
