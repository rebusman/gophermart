package accrual_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"gophermart/internal/accrual"
	"gophermart/internal/domain"
)

// Параметры цикла, используемые тестами воркера.
//
// Интервалы намеренно малы: тесты проверяют реакцию на события, а не
// выдержку пауз, и не должны замедлять прогон.
const (
	workerPollInterval = 10 * time.Millisecond
	workerLease        = time.Minute
	workerBatchSize    = 4
)

// errService — произвольный сбой прикладного сценария.
var errService = errors.New("сбой обработки")

// processingServiceStub подменяет прикладной сценарий расчёта в тестах
// воркера.
//
// Поведение каждого метода задаётся функцией: тест описывает ровно тот случай,
// который проверяет. Обращения защищены мьютексом, потому что воркер работает
// в отдельной goroutine.
type processingServiceStub struct {
	mu sync.Mutex

	claim   func(ctx context.Context, batchSize int, lease time.Duration) ([]domain.AccrualJob, error)
	process func(ctx context.Context, job domain.AccrualJob) error

	claimCalls   int
	processed    []domain.OrderNumber
	releaseCalls int
	released     []domain.OrderNumber
	releaseDelay time.Duration
}

func (s *processingServiceStub) Claim(
	ctx context.Context,
	batchSize int,
	lease time.Duration,
) ([]domain.AccrualJob, error) {
	s.mu.Lock()
	s.claimCalls++
	claim := s.claim
	s.mu.Unlock()

	if claim == nil {
		return nil, nil
	}

	return claim(ctx, batchSize, lease)
}

func (s *processingServiceStub) Process(ctx context.Context, job domain.AccrualJob) error {
	s.mu.Lock()
	s.processed = append(s.processed, job.Number)
	process := s.process
	s.mu.Unlock()

	if process == nil {
		return nil
	}

	return process(ctx, job)
}

func (s *processingServiceStub) Release(_ context.Context, jobs []domain.AccrualJob, delay time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.releaseCalls++
	s.releaseDelay = delay

	for _, job := range jobs {
		s.released = append(s.released, job.Number)
	}

	return nil
}

// snapshot возвращает согласованный снимок счётчиков.
func (s *processingServiceStub) snapshot() (int, []domain.OrderNumber, int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.claimCalls, append([]domain.OrderNumber(nil), s.processed...), s.releaseCalls
}

// newWorkerConfig возвращает параметры цикла для тестов.
func newWorkerConfig() accrual.WorkerConfig {
	return accrual.WorkerConfig{
		PollInterval:  workerPollInterval,
		BatchSize:     workerBatchSize,
		LeaseDuration: workerLease,
	}
}

// newJobs собирает задания с указанными номерами заказов.
func newJobs(t *testing.T, numbers ...string) []domain.AccrualJob {
	t.Helper()

	jobs := make([]domain.AccrualJob, 0, len(numbers))

	for _, raw := range numbers {
		number, err := domain.ParseOrderNumber(raw)
		if err != nil {
			t.Fatalf("разбор номера заказа %s: %v", raw, err)
		}

		jobs = append(jobs, domain.AccrualJob{Number: number, UserID: domain.NewUserID()})
	}

	return jobs
}

// runWorker запускает воркер в отдельной goroutine и возвращает функцию
// остановки, дожидающуюся его завершения.
func runWorker(t *testing.T, service accrual.ProcessingService, logs *bytes.Buffer) func() {
	t.Helper()

	handler := slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})
	worker := accrual.NewWorker(service, slog.New(handler), newWorkerConfig())

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)

	go func() {
		done <- worker.Run(ctx)
	}()

	return func() {
		cancel()

		select {
		case err := <-done:
			if err != nil {
				t.Errorf("воркер завершился ошибкой: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("воркер не завершился после отмены контекста")
		}
	}
}

// waitTimeout ограничивает ожидание условия в тестах воркера.
const waitTimeout = 2 * time.Second

// waitFor ожидает выполнения условия, опрашивая его до истечения срока.
func waitFor(t *testing.T, condition func() bool, message string) {
	t.Helper()

	deadline := time.Now().Add(waitTimeout)

	for time.Now().Before(deadline) {
		if condition() {
			return
		}

		time.Sleep(time.Millisecond)
	}

	t.Fatalf("условие не выполнилось за %s: %s", waitTimeout, message)
}

// TestWorkerProcessesClaimedBatchSequentially закрепляет последовательную
// обработку порции: каждый заказ обрабатывается ровно один раз, отдельная
// goroutine на заказ не создаётся.
func TestWorkerProcessesClaimedBatchSequentially(t *testing.T) {
	jobs := newJobs(t, "9278923470", "12345678903", "346436439")

	var (
		mu       sync.Mutex
		inFlight int
		maxSeen  int
		handed   bool
	)

	service := &processingServiceStub{
		claim: func(context.Context, int, time.Duration) ([]domain.AccrualJob, error) {
			mu.Lock()
			defer mu.Unlock()

			if handed {
				return nil, nil
			}

			handed = true

			return jobs, nil
		},
		process: func(context.Context, domain.AccrualJob) error {
			mu.Lock()
			inFlight++

			if inFlight > maxSeen {
				maxSeen = inFlight
			}

			mu.Unlock()

			time.Sleep(2 * time.Millisecond)

			mu.Lock()
			inFlight--
			mu.Unlock()

			return nil
		},
	}

	stop := runWorker(t, service, &bytes.Buffer{})

	waitFor(t, func() bool {
		_, processed, _ := service.snapshot()

		return len(processed) == len(jobs)
	}, "порция не обработана целиком")

	stop()

	_, processed, _ := service.snapshot()

	if len(processed) != len(jobs) {
		t.Fatalf("обработано заказов: %d, ожидалось %d", len(processed), len(jobs))
	}

	for i, job := range jobs {
		if processed[i] != job.Number {
			t.Errorf("нарушен порядок обработки: позиция %d содержит %s, ожидался %s",
				i, processed[i], job.Number)
		}
	}

	mu.Lock()
	defer mu.Unlock()

	if maxSeen > 1 {
		t.Errorf("порция обработана параллельно: одновременно в работе %d заказов", maxSeen)
	}
}

// TestWorkerRespectsBatchSize закрепляет, что за один цикл запрашивается не
// больше заданий, чем задано размером порции.
func TestWorkerRespectsBatchSize(t *testing.T) {
	var gotBatch, gotLease = 0, time.Duration(0)

	service := &processingServiceStub{
		claim: func(_ context.Context, batchSize int, lease time.Duration) ([]domain.AccrualJob, error) {
			gotBatch = batchSize
			gotLease = lease

			return nil, nil
		},
	}

	stop := runWorker(t, service, &bytes.Buffer{})

	waitFor(t, func() bool {
		claims, _, _ := service.snapshot()

		return claims > 0
	}, "выборка заданий не выполнялась")

	stop()

	if gotBatch != workerBatchSize {
		t.Errorf("неожиданный размер порции: got %d, want %d", gotBatch, workerBatchSize)
	}

	if gotLease != workerLease {
		t.Errorf("неожиданный срок аренды: got %s, want %s", gotLease, workerLease)
	}
}

// TestWorkerPausesOnRateLimitAndReleasesRemainder закрепляет требование
// «Превышение лимита запросов приостанавливает опрос целиком»: после отказа
// новых обращений нет, а остаток порции освобождён.
func TestWorkerPausesOnRateLimitAndReleasesRemainder(t *testing.T) {
	jobs := newJobs(t, "9278923470", "12345678903", "346436439")

	const pause = 300 * time.Millisecond

	var handed bool

	service := &processingServiceStub{
		claim: func(context.Context, int, time.Duration) ([]domain.AccrualJob, error) {
			if handed {
				return nil, nil
			}

			handed = true

			return jobs, nil
		},
		process: func(_ context.Context, job domain.AccrualJob) error {
			if job.Number == jobs[0].Number {
				return nil
			}

			return &domain.RateLimitError{RetryAfter: pause}
		},
	}

	stop := runWorker(t, service, &bytes.Buffer{})

	waitFor(t, func() bool {
		_, _, releases := service.snapshot()

		return releases > 0
	}, "остаток порции не освобождён")

	// Во время паузы обработка не продолжается: третий заказ не тронут.
	time.Sleep(pause / 3)

	_, processed, _ := service.snapshot()

	stop()

	if len(processed) != 2 {
		t.Errorf("обработка продолжилась во время паузы: обработано %d заказов", len(processed))
	}

	service.mu.Lock()
	defer service.mu.Unlock()

	if len(service.released) != 2 {
		t.Fatalf("освобождено заданий: %d, ожидалось 2", len(service.released))
	}

	if service.released[0] != jobs[1].Number || service.released[1] != jobs[2].Number {
		t.Errorf("освобождён неожиданный остаток порции: %v", service.released)
	}

	if service.releaseDelay != pause {
		t.Errorf("неожиданная отсрочка освобождения: got %s, want %s", service.releaseDelay, pause)
	}
}

// TestWorkerStopsPromptlyWhileWaiting закрепляет сценарий «Остановка во время
// ожидания следующего цикла»: воркер возвращает управление, не досиживая паузу.
func TestWorkerStopsPromptlyWhileWaiting(t *testing.T) {
	// Интервал заведомо больше отведённого на остановку времени: если бы
	// ожидание не прерывалось, тест не уложился бы в срок.
	worker := accrual.NewWorker(&processingServiceStub{}, slog.New(slog.DiscardHandler), accrual.WorkerConfig{
		PollInterval:  30 * time.Second,
		BatchSize:     workerBatchSize,
		LeaseDuration: workerLease,
	})

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)

	go func() {
		done <- worker.Run(ctx)
	}()

	// Даём воркеру дойти до ожидания.
	time.Sleep(20 * time.Millisecond)

	start := time.Now()

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("воркер завершился ошибкой: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("воркер не прервал ожидание следующего цикла")
	}

	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("остановка заняла %s: ожидание не прерывается отменой", elapsed)
	}
}

// TestWorkerStopsPromptlyDuringPause закрепляет прерывание паузы по лимиту
// запросов: остановка сервиса не должна ждать её окончания.
func TestWorkerStopsPromptlyDuringPause(t *testing.T) {
	jobs := newJobs(t, "9278923470")

	service := &processingServiceStub{
		claim: func(context.Context, int, time.Duration) ([]domain.AccrualJob, error) {
			return jobs, nil
		},
		process: func(context.Context, domain.AccrualJob) error {
			return &domain.RateLimitError{RetryAfter: 30 * time.Second}
		},
	}

	worker := accrual.NewWorker(service, slog.New(slog.DiscardHandler), newWorkerConfig())

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)

	go func() {
		done <- worker.Run(ctx)
	}()

	waitFor(t, func() bool {
		_, _, releases := service.snapshot()

		return releases > 0
	}, "воркер не дошёл до паузы")

	start := time.Now()

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("воркер завершился ошибкой: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("воркер не прервал паузу по лимиту запросов")
	}

	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("остановка заняла %s: пауза не прерывается отменой", elapsed)
	}
}

// TestWorkerSurvivesClaimFailure закрепляет устойчивость цикла: сбой выборки
// не завершает воркер, потому что база может быть временно недоступна.
func TestWorkerSurvivesClaimFailure(t *testing.T) {
	var attempts int

	service := &processingServiceStub{
		claim: func(context.Context, int, time.Duration) ([]domain.AccrualJob, error) {
			attempts++

			if attempts < 3 {
				return nil, errService
			}

			return nil, nil
		},
	}

	logs := &bytes.Buffer{}
	stop := runWorker(t, service, logs)

	waitFor(t, func() bool {
		claims, _, _ := service.snapshot()

		return claims >= 4
	}, "цикл прервался после сбоя выборки")

	stop()

	if !strings.Contains(logs.String(), "выборка заданий расчёта не выполнена") {
		t.Error("сбой выборки не записан в журнал")
	}
}

// TestWorkerSurvivesProcessFailure закрепляет, что сбой обработки одного
// заказа не прерывает обработку остальных.
func TestWorkerSurvivesProcessFailure(t *testing.T) {
	jobs := newJobs(t, "9278923470", "12345678903", "346436439")

	var handed bool

	service := &processingServiceStub{
		claim: func(context.Context, int, time.Duration) ([]domain.AccrualJob, error) {
			if handed {
				return nil, nil
			}

			handed = true

			return jobs, nil
		},
		process: func(_ context.Context, job domain.AccrualJob) error {
			if job.Number == jobs[0].Number {
				return errService
			}

			return nil
		},
	}

	logs := &bytes.Buffer{}
	stop := runWorker(t, service, logs)

	waitFor(t, func() bool {
		_, processed, _ := service.snapshot()

		return len(processed) == len(jobs)
	}, "обработка порции прервалась после сбоя одного заказа")

	stop()

	if !strings.Contains(logs.String(), "обработка заказа не выполнена") {
		t.Error("сбой обработки не записан в журнал")
	}
}

// TestWorkerLogsDoNotLeakSecrets закрепляет требование безопасности: журнал
// воркера не содержит адреса внешней системы и строки подключения.
func TestWorkerLogsDoNotLeakSecrets(t *testing.T) {
	jobs := newJobs(t, "9278923470")

	service := &processingServiceStub{
		claim: func(context.Context, int, time.Duration) ([]domain.AccrualJob, error) {
			return jobs, nil
		},
		process: func(context.Context, domain.AccrualJob) error {
			return errService
		},
	}

	logs := &bytes.Buffer{}
	stop := runWorker(t, service, logs)

	waitFor(t, func() bool {
		_, processed, _ := service.snapshot()

		return len(processed) > 0
	}, "заказ не обработан")

	stop()

	for _, secret := range []string{"password", "postgres://", "JWT_SECRET", "Authorization"} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("журнал содержит %q", secret)
		}
	}
}

// TestWorkerConfigValidate закрепляет отказ на параметрах, при которых цикл не
// имеет смысла.
func TestWorkerConfigValidate(t *testing.T) {
	valid := newWorkerConfig()

	if err := valid.Validate(); err != nil {
		t.Fatalf("корректные параметры отвергнуты: %v", err)
	}

	tests := map[string]accrual.WorkerConfig{
		"нулевой интервал":     {PollInterval: 0, BatchSize: 1, LeaseDuration: time.Minute},
		"нулевая порция":       {PollInterval: time.Second, BatchSize: 0, LeaseDuration: time.Minute},
		"нулевая аренда":       {PollInterval: time.Second, BatchSize: 1, LeaseDuration: 0},
		"отрицательный размер": {PollInterval: time.Second, BatchSize: -1, LeaseDuration: time.Minute},
	}

	for name, cfg := range tests {
		t.Run(name, func(t *testing.T) {
			if err := cfg.Validate(); !errors.Is(err, accrual.ErrMissingWorkerConfig) {
				t.Errorf("недопустимые параметры приняты: %v", err)
			}
		})
	}
}
