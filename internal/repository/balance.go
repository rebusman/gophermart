package repository

import (
	"context"

	"gophermart/internal/domain"
)

// BalanceRepository хранит счета лояльности пользователей и историю списаний.
type BalanceRepository interface {
	// Balance возвращает счёт лояльности пользователя.
	Balance(ctx context.Context, userID domain.UserID) (domain.Balance, error)

	// Withdraw списывает сумму со счёта пользователя и создаёт запись в
	Withdraw(ctx context.Context, withdrawal domain.Withdrawal) (bool, error)

	// WithdrawalsByUser возвращает списания пользователя, упорядоченные от
	WithdrawalsByUser(ctx context.Context, userID domain.UserID) ([]domain.Withdrawal, error)
}
