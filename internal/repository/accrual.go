package repository

import (
	"context"
	"time"

	"github.com/shopspring/decimal"

	"gophermart/internal/domain"
)

// AccrualRepository хранит состояние планировщика повторных проверок расчёта и
type AccrualRepository interface {
	// ClaimAccrualJobs выбирает до batchSize заданий фонового расчёта и
	ClaimAccrualJobs(ctx context.Context, batchSize int, lease time.Duration) ([]domain.AccrualJob, error)

	ApplyAccrual(
		ctx context.Context,
		job domain.AccrualJob,
		status domain.OrderStatus,
		accrual *decimal.Decimal,
	) (bool, error)

	// MarkAccrualProcessing переводит заказ в состояние PROCESSING, обнуляет
	MarkAccrualProcessing(ctx context.Context, number domain.OrderNumber, delay time.Duration) error

	// RescheduleAccrualJob увеличивает счётчик попыток и назначает следующую
	RescheduleAccrualJob(ctx context.Context, number domain.OrderNumber, delay time.Duration) error

	// ReleaseAccrualJobs освобождает занятые задания, назначая им следующую
	ReleaseAccrualJobs(ctx context.Context, numbers []domain.OrderNumber, delay time.Duration) error
}
