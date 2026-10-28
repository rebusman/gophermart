package domain

import (
	"time"

	"github.com/shopspring/decimal"
)

// MoneyScale — число знаков после запятой, которым представляются баллы
const MoneyScale = 2

// ValidateWithdrawalSum проверяет, что сумма пригодна для списания.
func ValidateWithdrawalSum(sum decimal.Decimal) error {
	if !sum.IsPositive() {
		return ErrNonPositiveWithdrawalSum
	}

	if !sum.Equal(sum.Truncate(MoneyScale)) {
		return ErrWithdrawalSumTooPrecise
	}

	return nil
}

// Withdrawal — списание баллов в счёт оплаты заказа.
type Withdrawal struct {
	// UserID — пользователь, со счёта которого списаны баллы.
	UserID UserID

	// OrderNumber — номер заказа, в счёт оплаты которого выполнено списание.
	OrderNumber OrderNumber

	// Sum — списанная сумма баллов. Всегда положительна.
	Sum decimal.Decimal

	// ProcessedAt — момент списания в UTC.
	ProcessedAt time.Time
}
