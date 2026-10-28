package domain

import (
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

// RateLimitError переносит длительность паузы, назначенной внешней системой
type RateLimitError struct {
	// RetryAfter — время, на которое следует приостановить обращения к
	RetryAfter time.Duration
}

// Error возвращает текстовое описание отказа по лимиту запросов.
func (e *RateLimitError) Error() string {
	return fmt.Sprintf("%s: повторить через %s", ErrAccrualRateLimited, e.RetryAfter)
}

// Unwrap возвращает ErrAccrualRateLimited, благодаря чему отказ распознаётся
func (e *RateLimitError) Unwrap() error {
	return ErrAccrualRateLimited
}

// AccrualResult — результат расчёта, полученный от внешней системы по одному
type AccrualResult struct {
	// Status — состояние расчёта заказа, соответствующее ответу.
	Status OrderStatus

	Accrual *decimal.Decimal
}

// AccrualJob — задание фонового расчёта: заказ, взятый в работу для проверки
type AccrualJob struct {
	// Number — номер заказа, результат по которому предстоит узнать.
	Number OrderNumber

	// UserID — владелец заказа, на счёт которого будет применено начисление.
	UserID UserID

	// Attempts — число неудачных проверок, накопленное к этому моменту.
	Attempts int
}
