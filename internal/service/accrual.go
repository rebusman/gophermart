package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"gophermart/internal/domain"
	"gophermart/internal/logging"
	"gophermart/internal/repository"
)

// AccrualClient узнаёт результат расчёта во внешней системе.
type AccrualClient interface {
	// OrderAccrual возвращает результат расчёта по номеру заказа.
	OrderAccrual(ctx context.Context, number domain.OrderNumber) (domain.AccrualResult, error)
}

// AccrualsConfig задаёт параметры планирования повторных проверок.
type AccrualsConfig struct {
	// BackoffBase — начальная отсрочка повторной проверки после неудачи.
	BackoffBase time.Duration

	// BackoffCap — потолок отсрочки повторной проверки.
	BackoffCap time.Duration

	// PollInterval — отсрочка следующей проверки после успешного ответа,
	PollInterval time.Duration
}

// Accruals реализует сценарий получения и применения результата расчёта.
type Accruals struct {
	orders repository.AccrualRepository
	client AccrualClient
	cfg    AccrualsConfig
}

// NewAccruals создаёт сервис фонового расчёта начислений.
func NewAccruals(orders repository.AccrualRepository, client AccrualClient, cfg AccrualsConfig) *Accruals {
	return &Accruals{orders: orders, client: client, cfg: cfg}
}

// Claim выбирает задания фонового расчёта и занимает их на срок lease.
func (s *Accruals) Claim(ctx context.Context, batchSize int, lease time.Duration) ([]domain.AccrualJob, error) {
	jobs, err := s.orders.ClaimAccrualJobs(ctx, batchSize, lease)
	if err != nil {
		return nil, fmt.Errorf("выборка заданий расчёта: %w", err)
	}

	return jobs, nil
}

// Process узнаёт результат расчёта по заданию и применяет его.
func (s *Accruals) Process(ctx context.Context, job domain.AccrualJob) error {
	result, err := s.client.OrderAccrual(ctx, job.Number)
	if err != nil {
		return s.handleFailure(ctx, job, err)
	}

	if result.Status == domain.OrderStatusProcessing {
		if err = s.orders.MarkAccrualProcessing(ctx, job.Number, s.cfg.PollInterval); err != nil {
			return fmt.Errorf("отметка выполняющегося расчёта: %w", err)
		}

		return nil
	}

	applied, err := s.orders.ApplyAccrual(ctx, job, result.Status, result.Accrual)
	if err != nil {
		return fmt.Errorf("применение результата расчёта: %w", err)
	}

	if !applied {
		logging.FromContext(ctx).DebugContext(ctx,
			"результат расчёта уже применён другим экземпляром",
			slog.String("order", job.Number.String()))
	}

	return nil
}

// handleFailure переносит проверку заказа после неудачного обращения.
func (s *Accruals) handleFailure(ctx context.Context, job domain.AccrualJob, cause error) error {
	if errors.Is(cause, domain.ErrAccrualRateLimited) {
		return cause
	}

	if ctx.Err() != nil {
		return cause
	}

	delay := s.backoff(job.Attempts + 1)

	if err := s.orders.RescheduleAccrualJob(ctx, job.Number, delay); err != nil {
		return errors.Join(cause, fmt.Errorf("перенос проверки заказа: %w", err))
	}

	logging.FromContext(ctx).DebugContext(ctx,
		"проверка заказа перенесена",
		slog.String("order", job.Number.String()),
		slog.Int("attempts", job.Attempts+1),
		slog.Duration("delay", delay),
		logging.ErrorAttr(cause))

	return nil
}

// Release освобождает занятые задания, назначая им проверку через delay.
func (s *Accruals) Release(ctx context.Context, jobs []domain.AccrualJob, delay time.Duration) error {
	if len(jobs) == 0 {
		return nil
	}

	numbers := make([]domain.OrderNumber, 0, len(jobs))
	for _, job := range jobs {
		numbers = append(numbers, job.Number)
	}

	if err := s.orders.ReleaseAccrualJobs(ctx, numbers, delay); err != nil {
		return fmt.Errorf("освобождение заданий расчёта: %w", err)
	}

	return nil
}

// backoff вычисляет отсрочку повторной проверки по числу выполненных попыток.
func (s *Accruals) backoff(attempts int) time.Duration {
	delay := s.cfg.BackoffBase

	if attempts < 1 {
		attempts = 1
	}

	for range attempts - 1 {
		if delay >= s.cfg.BackoffCap {
			break
		}

		delay *= 2
	}

	if delay > s.cfg.BackoffCap {
		return s.cfg.BackoffCap
	}

	return delay
}
