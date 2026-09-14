package accrual_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"gophermart/internal/accrual"
	"gophermart/internal/domain"
)

// Параметры цикла, используемые тестами воркера.
//
// Тесты выполняются в пузыре testing/synctest на фиктивных часах, поэтому
// величина интервалов не влияет на длительность прогона и выбрана близкой к
// рабочей: проверки опираются на точные значения времени, а не на пороги.
// Предел одновременных обращений меньше размера порции, чтобы тесты видели и
// одновременную обработку, и ожидание свободного слота.
const (
	workerPollInterval = time.Second
	workerLease        = time.Minute
	workerBatchSize    = 4
	workerConcurrency  = 2
)

// errService — произвольный сбой прикладного сценария.
var errService = errors.New("сбой обработки")

// processingServiceStub подменяет прикладной сценарий расчёта в тестах
// воркера.
//
// Поведение каждого метода задаётся функцией: тест описывает ровно тот случай,
// который проверяет. Обращения защищены мьютексом, потому что задания порции
// обрабатываются в нескольких goroutine.
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
		Concurrency:   workerConcurrency,
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

// claimOnce возвращает выборку, отдающую jobs при первом обращении и пустой
// результат при последующих.
//
// Выборку вызывает только goroutine воркера, поэтому состояние замыкания
// синхронизации не требует.
func claimOnce(jobs []domain.AccrualJob) func(context.Context, int, time.Duration) ([]domain.AccrualJob, error) {
	var handed bool

	return func(context.Context, int, time.Duration) ([]domain.AccrualJob, error) {
		if handed {
			return nil, nil
		}

		handed = true

		return jobs, nil
	}
}

// numbersOf возвращает номера заданий.
func numbersOf(jobs []domain.AccrualJob) []domain.OrderNumber {
	numbers := make([]domain.OrderNumber, 0, len(jobs))

	for _, job := range jobs {
		numbers = append(numbers, job.Number)
	}

	return numbers
}

// sameNumbers сообщает, совпадают ли наборы номеров без учёта порядка:
// порядок обработки заданий внутри порции не гарантирован.
func sameNumbers(got, want []domain.OrderNumber) bool {
	sorted := func(numbers []domain.OrderNumber) []string {
		out := make([]string, 0, len(numbers))

		for _, number := range numbers {
			out = append(out, number.String())
		}

		slices.Sort(out)

		return out
	}

	return slices.Equal(sorted(got), sorted(want))
}

// runWorker запускает воркер с параметрами цикла по умолчанию и логгером,
// пишущим в logs, и возвращает функцию остановки.
//
// Вызывается только внутри synctest.Test с его *testing.T.
func runWorker(t *testing.T, service accrual.ProcessingService, logs *bytes.Buffer) func() {
	t.Helper()

	handler := slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})

	return startWorker(t, service, slog.New(handler))
}

// startWorker запускает воркер в отдельной goroutine пузыря и возвращает
// функцию остановки.
//
// Контекст и канал завершения создаются от t пузыря: таймеры и каналы вне
// пузыря в нём использовать нельзя. Функция остановки отменяет контекст и
// дожидается, пока все goroutine пузыря заблокируются. Воркер, реагирующий на
// отмену, к этому моменту уже вернул результат; отсутствие результата означает,
// что воркер продолжает ждать, не заметив отмены.
func startWorker(t *testing.T, service accrual.ProcessingService, logger *slog.Logger) func() {
	t.Helper()

	worker := accrual.NewWorker(service, logger, newWorkerConfig())

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)

	go func() {
		done <- worker.Run(ctx)
	}()

	return func() {
		t.Helper()

		cancel()
		synctest.Wait()

		select {
		case err := <-done:
			if err != nil {
				t.Errorf("воркер завершился ошибкой: %v", err)
			}
		default:
			t.Error("воркер не завершился после отмены контекста")
		}
	}
}

// advance продвигает фиктивные часы пузыря на d и дожидается, пока воркер
// выполнит всё, что стало возможным к этому моменту, и снова заблокируется.
func advance(d time.Duration) {
	time.Sleep(d)
	synctest.Wait()
}

// expectProgress сообщает об ошибке, если число выборок и начатых обработок
// в указанный момент отличается от ожидаемого.
//
// Ошибка не прерывает тест: воркер ещё работает, и тест обязан его остановить,
// иначе пузырь не завершится.
func expectProgress(t *testing.T, service *processingServiceStub, claims, processed int, moment string) {
	t.Helper()

	gotClaims, gotProcessed, _ := service.snapshot()
	if gotClaims != claims || len(gotProcessed) != processed {
		t.Errorf("%s: выборок %d, обработок %d; ожидалось %d и %d",
			moment, gotClaims, len(gotProcessed), claims, processed)
	}
}

// expectReleased сообщает об ошибке, если освобождение выполнено не одним
// вызовом, не для указанного набора заказов или с другой отсрочкой.
func expectReleased(t *testing.T, service *processingServiceStub, delay time.Duration, want ...domain.OrderNumber) {
	t.Helper()

	service.mu.Lock()
	defer service.mu.Unlock()

	if service.releaseCalls != 1 {
		t.Errorf("освобождений: %d, ожидалось 1", service.releaseCalls)
	}

	if service.releaseDelay != delay {
		t.Errorf("неожиданная отсрочка освобождения: got %s, want %s", service.releaseDelay, delay)
	}

	if !sameNumbers(service.released, want) {
		t.Errorf("освобождён неожиданный остаток порции: got %v, want %v", service.released, want)
	}
}

// findLogRecord возвращает первую запись JSON-журнала с указанным сообщением.
func findLogRecord(t *testing.T, logs *bytes.Buffer, message string) map[string]any {
	t.Helper()

	scanner := bufio.NewScanner(bytes.NewReader(logs.Bytes()))
	scanner.Buffer(nil, 1<<20)

	for scanner.Scan() {
		var record map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatalf("запись журнала не является JSON: %v (%s)", err, scanner.Text())
		}

		if record["msg"] == message {
			return record
		}
	}

	if err := scanner.Err(); err != nil {
		t.Fatalf("чтение журнала: %v", err)
	}

	t.Fatalf("в журнале нет записи %q: %s", message, logs.String())

	return nil
}

// TestWorkerProcessesEachJobOnce закрепляет, что каждое задание порции
// обрабатывается ровно один раз, при произвольном порядке обработки.
func TestWorkerProcessesEachJobOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		jobs := newJobs(t, "9278923470", "12345678903", "346436439")

		service := &processingServiceStub{claim: claimOnce(jobs)}

		stop := runWorker(t, service, &bytes.Buffer{})

		// Второй цикл получает пустую выборку и не должен повторить обработку.
		advance(workerPollInterval)

		claims, processed, _ := service.snapshot()

		stop()

		if claims != 2 {
			t.Errorf("выборок за два цикла: %d, ожидалось 2", claims)
		}

		if !sameNumbers(processed, numbersOf(jobs)) {
			t.Errorf("обработаны не все задания порции либо не по одному разу: got %v, want %v",
				processed, numbersOf(jobs))
		}
	})
}

// TestWorkerRespectsConcurrencyLimit закрепляет сценарий «Предел
// одновременных обращений соблюдается»: одновременно выполняется ровно столько
// обработок, сколько разрешает предел, а следующая начинается только после
// завершения одной из выполняющихся.
func TestWorkerRespectsConcurrencyLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		jobs := newJobs(t, "9278923470", "12345678903", "346436439")

		const processDelay = 100 * time.Millisecond

		var (
			mu       sync.Mutex
			inFlight int
			maxSeen  int
		)

		service := &processingServiceStub{
			claim: claimOnce(jobs),
			process: func(context.Context, domain.AccrualJob) error {
				mu.Lock()
				inFlight++
				maxSeen = max(maxSeen, inFlight)
				mu.Unlock()

				time.Sleep(processDelay)

				mu.Lock()
				inFlight--
				mu.Unlock()

				return nil
			},
		}

		stop := runWorker(t, service, &bytes.Buffer{})

		synctest.Wait()
		expectProgress(t, service, 1, workerConcurrency, "сразу после выборки")

		advance(processDelay - time.Nanosecond)
		expectProgress(t, service, 1, workerConcurrency, "до завершения первых обработок")

		advance(time.Nanosecond)
		expectProgress(t, service, 1, len(jobs), "после завершения первых обработок")

		advance(processDelay)

		stop()

		mu.Lock()
		defer mu.Unlock()

		if maxSeen != workerConcurrency {
			t.Errorf("одновременно выполнялось обработок: %d, ожидалось ровно %d", maxSeen, workerConcurrency)
		}
	})
}

// TestWorkerRespectsBatchSize закрепляет, что за один цикл запрашивается не
// больше заданий, чем задано размером порции.
func TestWorkerRespectsBatchSize(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var gotBatch, gotLease = 0, time.Duration(0)

		service := &processingServiceStub{
			claim: func(_ context.Context, batchSize int, lease time.Duration) ([]domain.AccrualJob, error) {
				gotBatch = batchSize
				gotLease = lease

				return nil, nil
			},
		}

		stop := runWorker(t, service, &bytes.Buffer{})

		synctest.Wait()

		claims, _, _ := service.snapshot()

		stop()

		if claims != 1 {
			t.Fatalf("выборок заданий в первом цикле: %d, ожидалась 1", claims)
		}

		if gotBatch != workerBatchSize {
			t.Errorf("неожиданный размер порции: got %d, want %d", gotBatch, workerBatchSize)
		}

		if gotLease != workerLease {
			t.Errorf("неожиданный срок аренды: got %s, want %s", gotLease, workerLease)
		}
	})
}

// TestWorkerWaitsPollIntervalBetweenCycles закрепляет интервал опроса: после
// пустой выборки следующая выполняется ровно через интервал, не раньше.
func TestWorkerWaitsPollIntervalBetweenCycles(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service := &processingServiceStub{}

		stop := runWorker(t, service, &bytes.Buffer{})

		synctest.Wait()
		expectProgress(t, service, 1, 0, "сразу после запуска")

		advance(workerPollInterval - time.Nanosecond)
		expectProgress(t, service, 1, 0, "до истечения интервала опроса")

		advance(time.Nanosecond)
		expectProgress(t, service, 2, 0, "по истечении интервала опроса")

		stop()
	})
}

// TestWorkerPausesOnRateLimitAndReleasesRemainder закрепляет сценарии
// «Превышен лимит запросов» и «Лимит превышен при одновременной обработке
// порции»: выполняющееся обращение завершается, не начатые заказы не
// обрабатываются, не начатые и отклонённые освобождаются, а новых обращений
// нет ровно до истечения указанного времени.
func TestWorkerPausesOnRateLimitAndReleasesRemainder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		jobs := newJobs(t, "9278923470", "12345678903", "346436439", "4561261212345467")

		// Обе обработки начинаются одновременно, отказ по лимиту приходит
		// раньше, чем завершается медленное обращение. Пауза отличается от
		// интервала опроса: иначе тест не отличил бы приостановку от
		// обычного ожидания следующего цикла.
		const (
			rejectDelay  = 10 * time.Millisecond
			processDelay = 100 * time.Millisecond
			pause        = 30 * time.Second
		)

		var completed atomic.Bool

		service := &processingServiceStub{
			claim: claimOnce(jobs),
			process: func(ctx context.Context, job domain.AccrualJob) error {
				if job.Number != jobs[0].Number {
					time.Sleep(rejectDelay)

					return &domain.RateLimitError{RetryAfter: pause}
				}

				time.Sleep(processDelay)
				completed.Store(ctx.Err() == nil)

				return nil
			},
		}

		stop := runWorker(t, service, &bytes.Buffer{})

		synctest.Wait()
		expectProgress(t, service, 1, workerConcurrency, "сразу после выборки")

		advance(rejectDelay)
		expectProgress(t, service, 1, workerConcurrency, "после отказа по лимиту")

		if _, _, releases := service.snapshot(); releases != 0 {
			t.Errorf("освобождение выполнено до завершения выполняющегося обращения: освобождений %d", releases)
		}

		advance(processDelay - rejectDelay)
		expectProgress(t, service, 1, workerConcurrency, "после завершения выполняющегося обращения")
		expectReleased(t, service, pause, jobs[1].Number, jobs[2].Number, jobs[3].Number)

		if !completed.Load() {
			t.Error("выполняющееся обращение прервано приостановкой")
		}

		advance(pause - time.Nanosecond)
		expectProgress(t, service, 1, workerConcurrency, "до истечения паузы")

		advance(time.Nanosecond)
		expectProgress(t, service, 2, workerConcurrency, "по истечении паузы")

		stop()
	})
}

// TestWorkerPausesForLongestRetryAfter закрепляет сценарий «Несколько отказов
// по лимиту в одной порции»: обращения возобновляются не раньше истечения
// наибольшего из указанных времён, даже если он пришёл первым.
func TestWorkerPausesForLongestRetryAfter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		jobs := newJobs(t, "9278923470", "12345678903")

		const (
			longPause  = 30 * time.Second
			shortPause = 10 * time.Second
			firstReply = 100 * time.Millisecond
			lastReply  = 200 * time.Millisecond
		)

		service := &processingServiceStub{
			claim: claimOnce(jobs),
			process: func(_ context.Context, job domain.AccrualJob) error {
				if job.Number == jobs[0].Number {
					time.Sleep(firstReply)

					return &domain.RateLimitError{RetryAfter: longPause}
				}

				time.Sleep(lastReply)

				return &domain.RateLimitError{RetryAfter: shortPause}
			},
		}

		stop := runWorker(t, service, &bytes.Buffer{})

		advance(lastReply)
		expectProgress(t, service, 1, len(jobs), "после обоих отказов")
		expectReleased(t, service, longPause, numbersOf(jobs)...)

		advance(longPause - time.Nanosecond)
		expectProgress(t, service, 1, len(jobs), "до истечения наибольшей паузы")

		advance(time.Nanosecond)
		expectProgress(t, service, 2, len(jobs), "по истечении наибольшей паузы")

		stop()
	})
}

// TestWorkerStopsPromptlyWhileWaiting закрепляет сценарий «Остановка во время
// ожидания следующего цикла»: воркер возвращает управление, не досиживая паузу.
func TestWorkerStopsPromptlyWhileWaiting(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		stop := startWorker(t, &processingServiceStub{}, slog.New(slog.DiscardHandler))

		// Воркер выполнил первую выборку и ждёт следующего цикла.
		synctest.Wait()

		start := time.Now()

		stop()

		// Часы пузыря продвигаются только ожиданием таймеров: ненулевое время
		// означало бы, что остановка дождалась окончания интервала опроса.
		if elapsed := time.Since(start); elapsed != 0 {
			t.Errorf("остановка заняла %s: ожидание не прерывается отменой", elapsed)
		}
	})
}

// TestWorkerStopsPromptlyDuringPause закрепляет прерывание паузы по лимиту
// запросов: остановка сервиса не должна ждать её окончания.
func TestWorkerStopsPromptlyDuringPause(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		jobs := newJobs(t, "9278923470")

		service := &processingServiceStub{
			claim: func(context.Context, int, time.Duration) ([]domain.AccrualJob, error) {
				return jobs, nil
			},
			process: func(context.Context, domain.AccrualJob) error {
				return &domain.RateLimitError{RetryAfter: 30 * time.Second}
			},
		}

		stop := startWorker(t, service, slog.New(slog.DiscardHandler))

		synctest.Wait()

		if _, _, releases := service.snapshot(); releases != 1 {
			t.Fatalf("воркер не дошёл до паузы: освобождений %d", releases)
		}

		start := time.Now()

		stop()

		if elapsed := time.Since(start); elapsed != 0 {
			t.Errorf("остановка заняла %s: пауза не прерывается отменой", elapsed)
		}
	})
}

// TestWorkerStopsDuringConcurrentProcessing закрепляет сценарий «Остановка во
// время обращения к внешней системе» при одновременной обработке: отмена
// прерывает все выполняющиеся обработки, остаток порции не освобождается —
// его вернёт истечение аренды, — а воркер завершается без ошибки.
func TestWorkerStopsDuringConcurrentProcessing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		jobs := newJobs(t, "9278923470", "12345678903", "346436439")

		var interrupted atomic.Int32

		service := &processingServiceStub{
			claim: claimOnce(jobs),
			process: func(ctx context.Context, _ domain.AccrualJob) error {
				<-ctx.Done()
				interrupted.Add(1)

				return ctx.Err()
			},
		}

		stop := startWorker(t, service, slog.New(slog.DiscardHandler))

		synctest.Wait()
		expectProgress(t, service, 1, workerConcurrency, "до остановки")

		start := time.Now()

		stop()

		if elapsed := time.Since(start); elapsed != 0 {
			t.Errorf("остановка заняла %s: выполняющиеся обработки не прерываются", elapsed)
		}

		if got := interrupted.Load(); got != workerConcurrency {
			t.Errorf("прервано обработок: %d, ожидалось %d", got, workerConcurrency)
		}

		expectProgress(t, service, 1, workerConcurrency, "после остановки")

		if _, _, releases := service.snapshot(); releases != 0 {
			t.Errorf("остановка освободила задания: освобождений %d", releases)
		}
	})
}

// TestWorkerSurvivesClaimFailure закрепляет устойчивость цикла: сбой выборки
// не завершает воркер, потому что база может быть временно недоступна.
func TestWorkerSurvivesClaimFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
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

		// Первая выборка — при запуске, затем по одной на каждый интервал:
		// за три интервала выполняются четыре выборки, из них две неудачные.
		advance(3 * workerPollInterval)

		claims, _, _ := service.snapshot()

		stop()

		if claims != 4 {
			t.Errorf("цикл прервался после сбоя выборки: выборок %d, ожидалось 4", claims)
		}

		if !strings.Contains(logs.String(), "выборка заданий расчёта не выполнена") {
			t.Error("сбой выборки не записан в журнал")
		}
	})
}

// TestWorkerSurvivesProcessFailure закрепляет, что сбой обработки одного
// заказа не прерывает обработку остальных.
func TestWorkerSurvivesProcessFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		jobs := newJobs(t, "9278923470", "12345678903", "346436439")

		service := &processingServiceStub{
			claim: claimOnce(jobs),
			process: func(_ context.Context, job domain.AccrualJob) error {
				if job.Number == jobs[0].Number {
					return errService
				}

				return nil
			},
		}

		logs := &bytes.Buffer{}
		stop := runWorker(t, service, logs)

		synctest.Wait()

		_, processed, _ := service.snapshot()

		stop()

		if len(processed) != len(jobs) {
			t.Errorf("обработка порции прервалась после сбоя одного заказа: обработано %d из %d",
				len(processed), len(jobs))
		}

		if !strings.Contains(logs.String(), "обработка заказа не выполнена") {
			t.Error("сбой обработки не записан в журнал")
		}
	})
}

// TestWorkerSurvivesPanicInJob закрепляет сценарий «Аварийное завершение
// обработки одного заказа»: остальные заказы порции обработаны, фоновый
// процесс продолжает циклы, в журнал записаны номер заказа и стек, а задание
// не освобождено — его вернёт истечение аренды.
func TestWorkerSurvivesPanicInJob(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		jobs := newJobs(t, "9278923470", "12345678903", "346436439")

		const panicMessage = "падение обработки заказа"

		service := &processingServiceStub{
			claim: claimOnce(jobs),
			process: func(_ context.Context, job domain.AccrualJob) error {
				if job.Number == jobs[0].Number {
					panic(panicMessage)
				}

				return nil
			},
		}

		logs := &bytes.Buffer{}
		stop := runWorker(t, service, logs)

		synctest.Wait()
		expectProgress(t, service, 1, len(jobs), "после паники при обработке одного заказа")

		advance(workerPollInterval)
		expectProgress(t, service, 2, len(jobs), "в следующем цикле после паники")

		stop()

		if _, _, releases := service.snapshot(); releases != 0 {
			t.Errorf("задание после паники освобождено: освобождений %d", releases)
		}

		record := findLogRecord(t, logs, "panic при обработке заказа")

		if record["level"] != slog.LevelError.String() {
			t.Errorf("уровень записи о панике: got %v, want %s", record["level"], slog.LevelError)
		}

		if record["panic"] != panicMessage {
			t.Errorf("в записи нет значения паники: %v", record["panic"])
		}

		if record["order"] != jobs[0].Number.String() {
			t.Errorf("в записи нет номера заказа: got %v, want %s", record["order"], jobs[0].Number)
		}

		if stack, ok := record["stack"].(string); !ok || stack == "" {
			t.Error("в записи нет стека вызовов")
		}
	})
}

// TestWorkerLogsDoNotLeakSecrets закрепляет требование безопасности: журнал
// воркера не содержит адреса внешней системы и строки подключения.
func TestWorkerLogsDoNotLeakSecrets(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
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

		synctest.Wait()

		_, processed, _ := service.snapshot()

		stop()

		if len(processed) != 1 {
			t.Fatalf("обработано заказов: %d, ожидался 1", len(processed))
		}

		for _, secret := range []string{"password", "postgres://", "JWT_SECRET", "Authorization"} {
			if strings.Contains(logs.String(), secret) {
				t.Errorf("журнал содержит %q", secret)
			}
		}
	})
}

// TestWorkerConfigValidate закрепляет отказ на параметрах, при которых цикл не
// имеет смысла.
func TestWorkerConfigValidate(t *testing.T) {
	valid := newWorkerConfig()

	if err := valid.Validate(); err != nil {
		t.Fatalf("корректные параметры отвергнуты: %v", err)
	}

	tests := map[string]accrual.WorkerConfig{
		"нулевой интервал": {PollInterval: 0, BatchSize: 1, Concurrency: 1, LeaseDuration: time.Minute},
		"нулевая порция":   {PollInterval: time.Second, BatchSize: 0, Concurrency: 1, LeaseDuration: time.Minute},
		"нулевая аренда":   {PollInterval: time.Second, BatchSize: 1, Concurrency: 1, LeaseDuration: 0},
		"отрицательный размер": {
			PollInterval: time.Second, BatchSize: -1, Concurrency: 1, LeaseDuration: time.Minute,
		},
		"нулевой предел": {PollInterval: time.Second, BatchSize: 1, Concurrency: 0, LeaseDuration: time.Minute},
		"отрицательный предел": {
			PollInterval: time.Second, BatchSize: 1, Concurrency: -1, LeaseDuration: time.Minute,
		},
	}

	for name, cfg := range tests {
		t.Run(name, func(t *testing.T) {
			if err := cfg.Validate(); !errors.Is(err, accrual.ErrMissingWorkerConfig) {
				t.Errorf("недопустимые параметры приняты: %v", err)
			}
		})
	}
}
