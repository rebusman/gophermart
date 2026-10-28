package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"gophermart/internal/domain"
)

// SQL-запросы репозитория счёта лояльности и списаний.
const (
	// selectBalanceQuery читает состояние счёта пользователя.
	selectBalanceQuery = `
		SELECT current, withdrawn_total, updated_at
		FROM balances
		WHERE user_id = $1`

	// insertWithdrawalQuery создаёт списание и сообщает об исходе самим
	insertWithdrawalQuery = `
		INSERT INTO withdrawals (user_id, order_number, sum)
		VALUES ($1, $2, $3)
		ON CONFLICT (user_id, order_number) DO NOTHING
		RETURNING processed_at`

	// debitBalanceQuery уменьшает остаток и увеличивает сумму списаний одним
	debitBalanceQuery = `
		UPDATE balances
		SET current = current - $2,
		    withdrawn_total = withdrawn_total + $2,
		    updated_at = now()
		WHERE user_id = $1
		  AND current >= $2`

	// selectWithdrawalsByUserQuery выбирает списания пользователя от новых к
	selectWithdrawalsByUserQuery = `
		SELECT order_number, sum, processed_at
		FROM withdrawals
		WHERE user_id = $1
		ORDER BY processed_at DESC, order_number DESC`
)

// BalanceRepository хранит счета лояльности и списания пользователей в
type BalanceRepository struct {
	pool *pgxpool.Pool
}

// NewBalanceRepository создаёт репозиторий поверх пула подключений.
func NewBalanceRepository(pool *pgxpool.Pool) *BalanceRepository {
	return &BalanceRepository{pool: pool}
}

// Balance возвращает счёт лояльности пользователя.
func (r *BalanceRepository) Balance(ctx context.Context, userID domain.UserID) (domain.Balance, error) {
	var (
		current   pgtype.Numeric
		withdrawn pgtype.Numeric
		updatedAt pgtype.Timestamptz
	)

	err := r.pool.QueryRow(ctx, selectBalanceQuery, UUIDFromGoogle(uuid.UUID(userID))).
		Scan(&current, &withdrawn, &updatedAt)

	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return domain.Balance{}, domain.ErrBalanceNotFound
	case err != nil:
		return domain.Balance{}, fmt.Errorf("чтение счёта лояльности: %w", err)
	}

	parsedCurrent, err := DecimalFromNumeric(current)
	if err != nil {
		return domain.Balance{}, fmt.Errorf("чтение текущей суммы баллов: %w", err)
	}

	parsedWithdrawn, err := DecimalFromNumeric(withdrawn)
	if err != nil {
		return domain.Balance{}, fmt.Errorf("чтение суммы списаний: %w", err)
	}

	return domain.Balance{
		UserID:    userID,
		Current:   parsedCurrent,
		Withdrawn: parsedWithdrawn,
		UpdatedAt: updatedAt.Time.UTC(),
	}, nil
}

// Withdraw списывает сумму со счёта и создаёт запись в истории списаний одной
func (r *BalanceRepository) Withdraw(ctx context.Context, withdrawal domain.Withdrawal) (bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("начало транзакции списания: %w", err)
	}

	defer func() {
		// Откат уже зафиксированной транзакции безвреден и возвращает
		_ = tx.Rollback(ctx)
	}()

	userID := UUIDFromGoogle(uuid.UUID(withdrawal.UserID))
	sum := NumericFromDecimal(withdrawal.Sum)

	var processedAt pgtype.Timestamptz

	err = tx.QueryRow(ctx, insertWithdrawalQuery,
		userID,
		withdrawal.OrderNumber.String(),
		sum,
	).Scan(&processedAt)

	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("создание списания: %w", err)
	}

	tag, err := tx.Exec(ctx, debitBalanceQuery, userID, sum)
	if err != nil {
		return false, fmt.Errorf("изменение счёта лояльности: %w", err)
	}

	if tag.RowsAffected() == 0 {
		return false, domain.ErrInsufficientFunds
	}

	if err = tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("фиксация транзакции списания: %w", err)
	}

	return true, nil
}

// WithdrawalsByUser возвращает списания пользователя от самых новых к самым
func (r *BalanceRepository) WithdrawalsByUser(
	ctx context.Context,
	userID domain.UserID,
) ([]domain.Withdrawal, error) {
	rows, err := r.pool.Query(ctx, selectWithdrawalsByUserQuery, UUIDFromGoogle(uuid.UUID(userID)))
	if err != nil {
		return nil, fmt.Errorf("чтение списаний пользователя: %w", err)
	}

	defer rows.Close()

	var withdrawals []domain.Withdrawal

	for rows.Next() {
		withdrawal, scanErr := scanWithdrawal(rows, userID)
		if scanErr != nil {
			return nil, scanErr
		}

		withdrawals = append(withdrawals, withdrawal)
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("обход списаний пользователя: %w", err)
	}

	return withdrawals, nil
}

// scanWithdrawal собирает списание из текущей строки результата.
func scanWithdrawal(rows pgx.Rows, userID domain.UserID) (domain.Withdrawal, error) {
	var (
		orderNumber string
		sum         pgtype.Numeric
		processedAt pgtype.Timestamptz
	)

	if err := rows.Scan(&orderNumber, &sum, &processedAt); err != nil {
		return domain.Withdrawal{}, fmt.Errorf("чтение списания: %w", err)
	}

	parsedNumber, err := domain.ParseOrderNumber(orderNumber)
	if err != nil {
		return domain.Withdrawal{}, fmt.Errorf("чтение номера заказа списания: %w", err)
	}

	parsedSum, err := DecimalFromNumeric(sum)
	if err != nil {
		return domain.Withdrawal{}, fmt.Errorf("чтение суммы списания: %w", err)
	}

	return domain.Withdrawal{
		UserID:      userID,
		OrderNumber: parsedNumber,
		Sum:         parsedSum,
		ProcessedAt: processedAt.Time.UTC(),
	}, nil
}
