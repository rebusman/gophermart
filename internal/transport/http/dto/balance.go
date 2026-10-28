package dto

import (
	"time"

	"github.com/shopspring/decimal"
)

// Balance — состояние счёта баллов лояльности в теле ответа.
type Balance struct {
	// Current — текущая сумма баллов, доступная к списанию.
	Current Money `json:"current"`

	// Withdrawn — сумма баллов, списанных за весь период существования
	Withdrawn Money `json:"withdrawn"`
}

// NewBalance собирает DTO состояния счёта из доменных сумм.
func NewBalance(current, withdrawn decimal.Decimal) Balance {
	return Balance{
		Current:   NewMoney(current),
		Withdrawn: NewMoney(withdrawn),
	}
}

// WithdrawRequest — тело запроса на списание баллов.
type WithdrawRequest struct {
	// Order — номер заказа, в счёт оплаты которого списываются баллы.
	Order string `json:"order"`

	// Sum — сумма баллов к списанию. Должна быть положительной.
	Sum Money `json:"sum"`
}

// Withdrawal — списание в теле ответа на запрос истории списаний.
type Withdrawal struct {
	// Order — номер заказа, в счёт оплаты которого выполнено списание.
	Order string `json:"order"`

	// Sum — списанная сумма баллов.
	Sum Money `json:"sum"`

	// ProcessedAt — момент списания в UTC.
	ProcessedAt time.Time `json:"processed_at"`
}

// NewWithdrawal собирает DTO списания из его составляющих.
func NewWithdrawal(order string, sum decimal.Decimal, processedAt time.Time) Withdrawal {
	return Withdrawal{
		Order:       order,
		Sum:         NewMoney(sum),
		ProcessedAt: processedAt.UTC(),
	}
}
