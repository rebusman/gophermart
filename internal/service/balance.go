package service

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/shopspring/decimal"

	"gophermart/internal/domain"
	"gophermart/internal/logging"
	"gophermart/internal/repository"
)

// Balances реализует сценарии чтения состояния счёта лояльности, списания
type Balances struct {
	balances repository.BalanceRepository
}

// NewBalances создаёт сервис счёта лояльности.
func NewBalances(balances repository.BalanceRepository) *Balances {
	return &Balances{balances: balances}
}

// Balance возвращает состояние счёта пользователя.
func (s *Balances) Balance(ctx context.Context, userID domain.UserID) (domain.Balance, error) {
	balance, err := s.balances.Balance(ctx, userID)
	if err != nil {
		return domain.Balance{}, fmt.Errorf("чтение счёта лояльности: %w", err)
	}

	return balance, nil
}

// Withdraw списывает сумму со счёта пользователя в счёт оплаты заказа.
func (s *Balances) Withdraw(
	ctx context.Context,
	number domain.OrderNumber,
	sum decimal.Decimal,
	userID domain.UserID,
) error {
	if err := domain.ValidateWithdrawalSum(sum); err != nil {
		return fmt.Errorf("списание суммы %s: %w", sum, err)
	}

	withdrawal := domain.Withdrawal{
		UserID:      userID,
		OrderNumber: number,
		Sum:         sum,
	}

	created, err := s.balances.Withdraw(ctx, withdrawal)
	if err != nil {
		return fmt.Errorf("списание баллов: %w", err)
	}

	if !created {
		logging.FromContext(ctx).DebugContext(ctx,
			"списание по этому номеру заказа уже выполнено",
			slog.String("order", number.String()))
	}

	return nil
}

// Withdrawals возвращает списания пользователя от самых новых к самым старым.
func (s *Balances) Withdrawals(ctx context.Context, userID domain.UserID) ([]domain.Withdrawal, error) {
	withdrawals, err := s.balances.WithdrawalsByUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("чтение списаний пользователя: %w", err)
	}

	return withdrawals, nil
}
