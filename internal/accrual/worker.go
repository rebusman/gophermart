package accrual

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"gophermart/internal/domain"
	"gophermart/internal/logging"
)

// ProcessingService реализует прикладной сценарий фонового расчёта.
type ProcessingService interface {
	// Claim выбирает задания и занимает их на срок lease.
	Claim(ctx context.Context, batchSize int, lease time.Duration) ([]domain.AccrualJob, error)

	// Process узнаёт результат расчёта по заданию и применяет его.
	Process(ctx context.Context, job domain.AccrualJob) error

	// Release освобождает занятые задания, назначая им проверку через delay.
	Release(ctx context.Context, jobs []domain.AccrualJob, delay time.Duration) error
}

// WorkerConfig задаёт параметры цикла опроса.
type WorkerConfig struct {
	// PollInterval — пауза между циклами опроса.
	PollInterval time.Duration

	// BatchSize — число заданий, выбираемых за один цикл.
	BatchSize int

	// Concurrency — предел одновременно обрабатываемых заданий порции.
	Concurrency int

	// LeaseDuration — срок аренды выбранного задания.
	LeaseDuration time.Duration
}

// Worker опрашивает внешнюю систему расчёта по расписанию.
type Worker struct {
	service ProcessingService
	logger  *slog.Logger
	cfg     WorkerConfig
}

// NewWorker создаёт фоновый воркер расчёта начислений.
func NewWorker(service ProcessingService, logger *slog.Logger, cfg WorkerConfig) *Worker {
	return &Worker{service: service, logger: logger, cfg: cfg}
}

// Run выполняет цикл опроса до отмены ctx.
func (w *Worker) Run(ctx context.Context) error {
	w.logger.InfoContext(ctx, "фоновый расчёт начислений запущен",
		slog.Duration("poll_interval", w.cfg.PollInterval),
		slog.Int("batch_size", w.cfg.BatchSize),
		slog.Int("concurrency", w.cfg.Concurrency),
		slog.Duration("lease", w.cfg.LeaseDuration))

	for {
		select {
		case <-ctx.Done():
			w.logger.InfoContext(ctx, "фоновый расчёт начислений остановлен")

			return nil
		default:
		}

		pause := w.cycle(ctx)

		if !sleep(ctx, pause) {
			w.logger.InfoContext(ctx, "фоновый расчёт начислений остановлен")

			return nil
		}
	}
}

// cycle выполняет один цикл опроса и возвращает паузу до следующего.
func (w *Worker) cycle(ctx context.Context) time.Duration {
	jobs, err := w.service.Claim(ctx, w.cfg.BatchSize, w.cfg.LeaseDuration)
	if err != nil {
		if ctx.Err() == nil {
			w.logger.ErrorContext(ctx, "выборка заданий расчёта не выполнена", logging.ErrorAttr(err))
		}

		return w.cfg.PollInterval
	}

	if len(jobs) == 0 {
		return w.cfg.PollInterval
	}

	batch := &batchState{}

	// Контекст группы не используется: его отмена при первой ошибке прервала
	var group errgroup.Group

	group.SetLimit(w.cfg.Concurrency)

	for _, job := range jobs {
		if ctx.Err() != nil {
			break
		}

		if batch.paused() {
			batch.skip(job)

			continue
		}

		// Go блокируется до освобождения слота, поэтому цикл запуска сам
		group.Go(func() error {
			w.process(ctx, job, batch)

			return nil
		})
	}

	// Ни одна функция группы не возвращает ошибку: сбой одного заказа не
	_ = group.Wait()

	if ctx.Err() != nil {
		return w.cfg.PollInterval
	}

	if retryAfter, remaining, paused := batch.outcome(); paused {
		return w.pause(ctx, remaining, retryAfter)
	}

	return w.cfg.PollInterval
}

// process обрабатывает одно задание порции.
func (w *Worker) process(ctx context.Context, job domain.AccrualJob, batch *batchState) {
	defer func() {
		if recovered := recover(); recovered != nil {
			w.logPanic(ctx, job, recovered)
		}
	}()

	if ctx.Err() != nil {
		return
	}

	if batch.paused() {
		batch.skip(job)

		return
	}

	err := w.service.Process(ctx, job)
	if err == nil {
		return
	}

	var rateLimit *domain.RateLimitError
	if errors.As(err, &rateLimit) {
		batch.pause(job, rateLimit.RetryAfter)

		return
	}

	if ctx.Err() == nil {
		w.logger.ErrorContext(ctx, "обработка заказа не выполнена",
			slog.String("order", job.Number.String()), logging.ErrorAttr(err))
	}
}

// logPanic записывает аварийное завершение обработки задания.
func (w *Worker) logPanic(ctx context.Context, job domain.AccrualJob, recovered any) {
	w.logger.ErrorContext(ctx, "panic при обработке заказа",
		slog.Any("panic", recovered),
		slog.String("stack", string(debug.Stack())),
		slog.String("order", job.Number.String()))
}

// pause освобождает необработанный остаток порции и возвращает длительность
func (w *Worker) pause(ctx context.Context, remaining []domain.AccrualJob, retryAfter time.Duration) time.Duration {
	w.logger.WarnContext(ctx, "опрос системы расчёта приостановлен по лимиту запросов",
		slog.Duration("retry_after", retryAfter),
		slog.Int("released", len(remaining)))

	if err := w.service.Release(ctx, remaining, retryAfter); err != nil && ctx.Err() == nil {
		w.logger.ErrorContext(ctx, "освобождение заданий расчёта не выполнено", logging.ErrorAttr(err))
	}

	return retryAfter
}

// batchState — состояние порции, общее для goroutine её заданий.
type batchState struct {
	mu         sync.Mutex
	suspended  bool
	retryAfter time.Duration
	remaining  []domain.AccrualJob
}

// paused сообщает, приостановлена ли порция отказом по лимиту запросов.
func (b *batchState) paused() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.suspended
}

// skip откладывает до освобождения задание, не начатое из-за приостановки.
func (b *batchState) skip(job domain.AccrualJob) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.remaining = append(b.remaining, job)
}

// pause приостанавливает порцию и откладывает до освобождения задание,
func (b *batchState) pause(job domain.AccrualJob, retryAfter time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.suspended = true
	b.retryAfter = max(b.retryAfter, retryAfter)
	b.remaining = append(b.remaining, job)
}

// outcome возвращает паузу, отложенные задания и признак приостановки.
func (b *batchState) outcome() (time.Duration, []domain.AccrualJob, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.retryAfter, b.remaining, b.suspended
}

// sleep ожидает d, прерываясь отменой контекста.
func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// BackgroundTaskName — имя фоновой задачи расчёта в журнале жизненного цикла.
const BackgroundTaskName = "фоновый расчёт начислений"

// Ошибки сборки воркера.
var (
	// ErrMissingWorkerConfig возвращается, когда параметр цикла не задан или
	ErrMissingWorkerConfig = errors.New("недопустимый параметр цикла фонового расчёта")
)

// Validate проверяет параметры цикла.
func (c WorkerConfig) Validate() error {
	if c.PollInterval <= 0 {
		return fmt.Errorf("%w: интервал опроса %s", ErrMissingWorkerConfig, c.PollInterval)
	}

	if c.BatchSize <= 0 {
		return fmt.Errorf("%w: размер порции %d", ErrMissingWorkerConfig, c.BatchSize)
	}

	if c.Concurrency <= 0 {
		return fmt.Errorf("%w: предел одновременных обращений %d", ErrMissingWorkerConfig, c.Concurrency)
	}

	if c.LeaseDuration <= 0 {
		return fmt.Errorf("%w: срок аренды %s", ErrMissingWorkerConfig, c.LeaseDuration)
	}

	return nil
}
