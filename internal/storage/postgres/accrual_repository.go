package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/shopspring/decimal"

	"gophermart/internal/domain"
)

// SQL-запросы фонового расчёта начислений.
const (
	// claimAccrualJobsQuery выбирает задания и занимает их одним оператором.
	claimAccrualJobsQuery = `
		WITH picked AS (
			SELECT number, next_attempt_at AS due_at
			FROM orders
			WHERE status IN ('NEW', 'PROCESSING')
			  AND next_attempt_at <= now()
			ORDER BY next_attempt_at, number
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		),
		claimed AS (
			UPDATE orders o
			SET next_attempt_at = now() + make_interval(secs => $2)
			FROM picked p
			WHERE o.number = p.number
			RETURNING o.number, o.user_id, o.attempts, p.due_at
		)
		SELECT number, user_id, attempts
		FROM claimed
		ORDER BY due_at, number`

	// finalizeOrderQuery переводит заказ в окончательное состояние.
	finalizeOrderQuery = `
		UPDATE orders
		SET status = $2,
		    accrual = $3
		WHERE number = $1
		  AND status NOT IN ('PROCESSED', 'INVALID')`

	// increaseBalanceQuery увеличивает текущую сумму баллов на счёте.
	increaseBalanceQuery = `
		UPDATE balances
		SET current = current + $2,
		    updated_at = now()
		WHERE user_id = $1`

	// markAccrualProcessingQuery отмечает выполняющийся расчёт и обнуляет
	markAccrualProcessingQuery = `
		UPDATE orders
		SET status = 'PROCESSING',
		    attempts = 0,
		    next_attempt_at = now() + make_interval(secs => $2)
		WHERE number = $1
		  AND status NOT IN ('PROCESSED', 'INVALID')`

	// rescheduleAccrualJobQuery переносит проверку заказа после неудачи.
	rescheduleAccrualJobQuery = `
		UPDATE orders
		SET attempts = attempts + 1,
		    next_attempt_at = now() + make_interval(secs => $2)
		WHERE number = $1
		  AND status NOT IN ('PROCESSED', 'INVALID')`

	// releaseAccrualJobsQuery освобождает занятые задания, не трогая счётчик
	releaseAccrualJobsQuery = `
		UPDATE orders
		SET next_attempt_at = now() + make_interval(secs => $2)
		WHERE number = ANY($1)
		  AND status NOT IN ('PROCESSED', 'INVALID')`
)

// ClaimAccrualJobs выбирает задания фонового расчёта и занимает их на срок
func (r *OrderRepository) ClaimAccrualJobs(
	ctx context.Context,
	batchSize int,
	lease time.Duration,
) ([]domain.AccrualJob, error) {
	rows, err := r.pool.Query(ctx, claimAccrualJobsQuery, batchSize, lease.Seconds())
	if err != nil {
		return nil, fmt.Errorf("выборка заданий расчёта: %w", err)
	}

	defer rows.Close()

	var jobs []domain.AccrualJob

	for rows.Next() {
		job, scanErr := scanAccrualJob(rows)
		if scanErr != nil {
			return nil, scanErr
		}

		jobs = append(jobs, job)
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("обход заданий расчёта: %w", err)
	}

	return jobs, nil
}

// ApplyAccrual переводит заказ в окончательное состояние и применяет
func (r *OrderRepository) ApplyAccrual(
	ctx context.Context,
	job domain.AccrualJob,
	status domain.OrderStatus,
	accrual *decimal.Decimal,
) (bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("начало транзакции применения начисления: %w", err)
	}

	defer func() {
		// Откат уже зафиксированной транзакции безвреден и возвращает
		_ = tx.Rollback(ctx)
	}()

	tag, err := tx.Exec(ctx, finalizeOrderQuery,
		job.Number.String(),
		status.String(),
		NumericFromDecimalPtr(accrual),
	)
	if err != nil {
		return false, fmt.Errorf("перевод заказа в окончательное состояние: %w", err)
	}

	if tag.RowsAffected() == 0 {
		return false, nil
	}

	if err = increaseBalance(ctx, tx, job.UserID, accrual); err != nil {
		return false, err
	}

	if err = tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("фиксация транзакции применения начисления: %w", err)
	}

	return true, nil
}

// MarkAccrualProcessing отмечает выполняющийся расчёт и назначает следующую
func (r *OrderRepository) MarkAccrualProcessing(
	ctx context.Context,
	number domain.OrderNumber,
	delay time.Duration,
) error {
	if _, err := r.pool.Exec(ctx, markAccrualProcessingQuery, number.String(), delay.Seconds()); err != nil {
		return fmt.Errorf("отметка выполняющегося расчёта: %w", err)
	}

	return nil
}

// RescheduleAccrualJob переносит проверку заказа после неудачи, увеличивая
func (r *OrderRepository) RescheduleAccrualJob(
	ctx context.Context,
	number domain.OrderNumber,
	delay time.Duration,
) error {
	if _, err := r.pool.Exec(ctx, rescheduleAccrualJobQuery, number.String(), delay.Seconds()); err != nil {
		return fmt.Errorf("перенос проверки заказа: %w", err)
	}

	return nil
}

// ReleaseAccrualJobs освобождает занятые задания, не изменяя счётчик попыток.
func (r *OrderRepository) ReleaseAccrualJobs(
	ctx context.Context,
	numbers []domain.OrderNumber,
	delay time.Duration,
) error {
	if len(numbers) == 0 {
		return nil
	}

	raw := make([]string, 0, len(numbers))
	for _, number := range numbers {
		raw = append(raw, number.String())
	}

	if _, err := r.pool.Exec(ctx, releaseAccrualJobsQuery, raw, delay.Seconds()); err != nil {
		return fmt.Errorf("освобождение заданий расчёта: %w", err)
	}

	return nil
}

// increaseBalance увеличивает текущую сумму баллов на счёте владельца заказа.
func increaseBalance(ctx context.Context, tx pgx.Tx, userID domain.UserID, accrual *decimal.Decimal) error {
	if accrual == nil || accrual.IsZero() {
		return nil
	}

	tag, err := tx.Exec(ctx, increaseBalanceQuery,
		UUIDFromGoogle(uuid.UUID(userID)),
		NumericFromDecimal(*accrual),
	)
	if err != nil {
		return fmt.Errorf("увеличение счёта лояльности: %w", err)
	}

	if tag.RowsAffected() == 0 {
		return fmt.Errorf("увеличение счёта лояльности: %w", domain.ErrBalanceNotFound)
	}

	return nil
}

// scanAccrualJob собирает задание расчёта из текущей строки результата.
func scanAccrualJob(rows pgx.Rows) (domain.AccrualJob, error) {
	var (
		number   string
		owner    pgtype.UUID
		attempts int
	)

	if err := rows.Scan(&number, &owner, &attempts); err != nil {
		return domain.AccrualJob{}, fmt.Errorf("чтение задания расчёта: %w", err)
	}

	parsedNumber, err := domain.ParseOrderNumber(number)
	if err != nil {
		return domain.AccrualJob{}, fmt.Errorf("чтение номера заказа задания: %w", err)
	}

	parsedOwner, err := GoogleFromUUID(owner)
	if err != nil {
		return domain.AccrualJob{}, fmt.Errorf("чтение владельца заказа задания: %w", err)
	}

	return domain.AccrualJob{
		Number:   parsedNumber,
		UserID:   domain.UserID(parsedOwner),
		Attempts: attempts,
	}, nil
}
