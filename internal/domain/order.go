package domain

import (
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"gophermart/internal/validator"
)

// OrderStatus — состояние расчёта начисления по заказу.
type OrderStatus string

// Состояния расчёта начисления.
const (
	// OrderStatusNew — заказ загружен, но ещё не взят в расчёт.
	OrderStatusNew OrderStatus = "NEW"

	// OrderStatusProcessing — расчёт начисления выполняется.
	OrderStatusProcessing OrderStatus = "PROCESSING"

	// OrderStatusInvalid — система расчёта отказала в начислении.
	OrderStatusInvalid OrderStatus = "INVALID"

	// OrderStatusProcessed — расчёт завершён. Состояние окончательное;
	OrderStatusProcessed OrderStatus = "PROCESSED"
)

// OrderNumber — номер заказа.
type OrderNumber string

// ParseOrderStatus разбирает состояние расчёта из строкового представления.
func ParseOrderStatus(raw string) (OrderStatus, error) {
	switch status := OrderStatus(raw); status {
	case OrderStatusNew, OrderStatusProcessing, OrderStatusInvalid, OrderStatusProcessed:
		return status, nil
	default:
		return "", ErrUnknownOrderStatus
	}
}

// String возвращает строковое представление состояния расчёта.
func (s OrderStatus) String() string {
	return string(s)
}

// ParseOrderNumber разбирает номер заказа из строкового представления.
func ParseOrderNumber(raw string) (OrderNumber, error) {
	trimmed := strings.TrimSpace(raw)

	if !validator.Luhn(trimmed) {
		return "", ErrInvalidOrderNumber
	}

	return OrderNumber(trimmed), nil
}

// String возвращает строковое представление номера заказа.
func (n OrderNumber) String() string {
	return string(n)
}

// Order — заказ, загруженный пользователем для расчёта начисления.
type Order struct {
	// Number — номер заказа. Уникален в пределах всей системы: один номер
	Number OrderNumber

	// UserID — владелец заказа, определённый при первой загрузке номера.
	UserID UserID

	// Status — текущее состояние расчёта начисления.
	Status OrderStatus

	Accrual *decimal.Decimal

	// UploadedAt — момент загрузки номера заказа в UTC.
	UploadedAt time.Time
}
