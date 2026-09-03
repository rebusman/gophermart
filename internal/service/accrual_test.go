package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"gophermart/internal/domain"
	"gophermart/internal/repository"
	"gophermart/internal/service"
)

// Проверка на этапе компиляции: конструктор сервиса расчёта принимает
// хранилище, клиента и параметры отсрочки явными аргументами. Глобального
// состояния и пакетных синглтонов в сборке нет.
var _ func(repository.AccrualRepository, service.AccrualClient, service.AccrualsConfig) *service.Accruals = service.NewAccruals

// Параметры отсрочки, используемые тестами сервиса расчёта.
const (
	testBackoffBase  = time.Second
	testBackoffCap   = 8 * time.Second
	testPollInterval = 100 * time.Millisecond
)

// newAccruals собирает сервис расчёта с тестовыми параметрами отсрочки.
func newAccruals(orders repository.AccrualRepository, client service.AccrualClient) *service.Accruals {
	return service.NewAccruals(orders, client, service.AccrualsConfig{
		BackoffBase:  testBackoffBase,
		BackoffCap:   testBackoffCap,
		PollInterval: testPollInterval,
	})
}

// newJob собирает задание расчёта с указанным числом накопленных попыток.
func newJob(t *testing.T, attempts int) domain.AccrualJob {
	t.Helper()

	return domain.AccrualJob{
		Number:   newOrderNumber(t),
		UserID:   domain.NewUserID(),
		Attempts: attempts,
	}
}

// TestAccrualsProcessAppliesProcessedResult закрепляет применение завершённого
// расчёта: заказ финализируется вместе с суммой начисления.
func TestAccrualsProcessAppliesProcessedResult(t *testing.T) {
	sum := decimal.RequireFromString("729.98")
	client := &accrualClientStub{result: domain.AccrualResult{
		Status:  domain.OrderStatusProcessed,
		Accrual: &sum,
	}}
	repo := &accrualRepositoryStub{applied: true}
	job := newJob(t, 0)

	if err := newAccruals(repo, client).Process(t.Context(), job); err != nil {
		t.Fatalf("обработка задания: %v", err)
	}

	if len(repo.appliedResults) != 1 {
		t.Fatalf("неожиданное число применений: got %d, want 1", len(repo.appliedResults))
	}

	got := repo.appliedResults[0]

	if got.status != domain.OrderStatusProcessed {
		t.Errorf("неожиданное состояние: got %s, want %s", got.status, domain.OrderStatusProcessed)
	}

	if got.accrual == nil || got.accrual.String() != "729.98" {
		t.Errorf("неожиданная сумма начисления: %v", got.accrual)
	}

	if got.job.Number != job.Number {
		t.Errorf("применение отнесено к другому заказу: got %s, want %s", got.job.Number, job.Number)
	}

	if repo.rescheduleCalls != 0 || repo.processingCalls != 0 {
		t.Error("окончательный результат сопровождён переносом проверки")
	}
}

// TestAccrualsProcessAppliesFinalResultWithoutAccrual закрепляет сценарий
// «Расчёт завершён без вознаграждения» и отказ в начислении: сумма остаётся
// отсутствующей, а не нулевой.
func TestAccrualsProcessAppliesFinalResultWithoutAccrual(t *testing.T) {
	tests := []struct {
		name   string
		status domain.OrderStatus
	}{
		{name: "завершён без вознаграждения", status: domain.OrderStatusProcessed},
		{name: "отказ в начислении", status: domain.OrderStatusInvalid},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &accrualClientStub{result: domain.AccrualResult{Status: test.status}}
			repo := &accrualRepositoryStub{applied: true}

			if err := newAccruals(repo, client).Process(t.Context(), newJob(t, 0)); err != nil {
				t.Fatalf("обработка задания: %v", err)
			}

			if len(repo.appliedResults) != 1 {
				t.Fatalf("неожиданное число применений: got %d, want 1", len(repo.appliedResults))
			}

			got := repo.appliedResults[0]

			if got.status != test.status {
				t.Errorf("неожиданное состояние: got %s, want %s", got.status, test.status)
			}

			if got.accrual != nil {
				t.Errorf("сумма подставлена вместо отсутствия: %s", got.accrual)
			}
		})
	}
}

// TestAccrualsProcessMarksProcessingAndResetsSchedule закрепляет обработку
// выполняющегося расчёта: заказ не финализируется, а проверка назначается
// через обычный интервал опроса.
func TestAccrualsProcessMarksProcessingAndResetsSchedule(t *testing.T) {
	client := &accrualClientStub{result: domain.AccrualResult{Status: domain.OrderStatusProcessing}}
	repo := &accrualRepositoryStub{}

	if err := newAccruals(repo, client).Process(t.Context(), newJob(t, 5)); err != nil {
		t.Fatalf("обработка задания: %v", err)
	}

	if repo.processingCalls != 1 {
		t.Fatalf("неожиданное число отметок выполняющегося расчёта: got %d, want 1", repo.processingCalls)
	}

	if repo.processingDelay != testPollInterval {
		t.Errorf("неожиданная отсрочка: got %s, want %s", repo.processingDelay, testPollInterval)
	}

	if len(repo.appliedResults) != 0 {
		t.Error("выполняющийся расчёт финализировал заказ")
	}

	if repo.rescheduleCalls != 0 {
		t.Error("успешный ответ учтён как неудача")
	}
}

// TestAccrualsProcessKeepsStatusOnFailure закрепляет требование «Сбой внешней
// системы не является результатом расчёта» и сценарий «Заказ не зарегистрирован
// во внешней системе»: состояние заказа не изменяется ни при одной причине.
func TestAccrualsProcessKeepsStatusOnFailure(t *testing.T) {
	tests := []struct {
		name  string
		cause error
	}{
		{name: "заказ не зарегистрирован", cause: errOrderNotRegistered},
		{name: "ошибка внешней системы", cause: errExternalFailure},
		{name: "сетевой сбой", cause: errNetwork},
		{name: "истечение времени ожидания", cause: context.DeadlineExceeded},
		{name: "неизвестный статус", cause: errUnknownStatus},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &accrualClientStub{err: test.cause}
			repo := &accrualRepositoryStub{}

			if err := newAccruals(repo, client).Process(t.Context(), newJob(t, 0)); err != nil {
				t.Fatalf("неудача обращения признана отказом обработки: %v", err)
			}

			if len(repo.appliedResults) != 0 {
				t.Error("сбой привёл к финализации заказа")
			}

			if repo.processingCalls != 0 {
				t.Error("сбой изменил состояние расчёта")
			}

			if repo.rescheduleCalls != 1 {
				t.Errorf("проверка не перенесена: %d вызовов", repo.rescheduleCalls)
			}
		})
	}
}

// TestAccrualsProcessGrowsBackoffWithAttempts закрепляет требование
// «Планирование повторной проверки заказа»: отсрочка растёт с каждой неудачей
// и не превышает потолок.
func TestAccrualsProcessGrowsBackoffWithAttempts(t *testing.T) {
	tests := []struct {
		attempts int
		want     time.Duration
	}{
		{attempts: 0, want: testBackoffBase},
		{attempts: 1, want: 2 * testBackoffBase},
		{attempts: 2, want: 4 * testBackoffBase},
		{attempts: 3, want: 8 * testBackoffBase},
		{attempts: 4, want: testBackoffCap},
		{attempts: 50, want: testBackoffCap},
		{attempts: 1_000_000, want: testBackoffCap},
	}

	var previous time.Duration

	for _, test := range tests {
		client := &accrualClientStub{err: errExternalFailure}
		repo := &accrualRepositoryStub{}

		if err := newAccruals(repo, client).Process(t.Context(), newJob(t, test.attempts)); err != nil {
			t.Fatalf("обработка задания: %v", err)
		}

		if repo.rescheduleDelay != test.want {
			t.Errorf("накоплено попыток %d: отсрочка got %s, want %s",
				test.attempts, repo.rescheduleDelay, test.want)
		}

		if repo.rescheduleDelay > testBackoffCap {
			t.Errorf("отсрочка превысила потолок: %s", repo.rescheduleDelay)
		}

		if repo.rescheduleDelay <= 0 {
			t.Errorf("отсрочка неположительна: %s", repo.rescheduleDelay)
		}

		if repo.rescheduleDelay < previous {
			t.Errorf("отсрочка уменьшилась при большем числе попыток: %s после %s",
				repo.rescheduleDelay, previous)
		}

		previous = repo.rescheduleDelay
	}
}

// TestAccrualsProcessReportsRateLimitWithoutTouchingOrder закрепляет требование
// «Превышение лимита запросов приостанавливает опрос целиком»: отказ относится
// ко всему сервису, поэтому заказ не трогается вовсе.
func TestAccrualsProcessReportsRateLimitWithoutTouchingOrder(t *testing.T) {
	const pause = 37 * time.Second

	client := &accrualClientStub{err: &domain.RateLimitError{RetryAfter: pause}}
	repo := &accrualRepositoryStub{}

	err := newAccruals(repo, client).Process(t.Context(), newJob(t, 2))

	if !errors.Is(err, domain.ErrAccrualRateLimited) {
		t.Fatalf("ожидался отказ по лимиту запросов, получено: %v", err)
	}

	var rateLimit *domain.RateLimitError
	if !errors.As(err, &rateLimit) {
		t.Fatalf("длительность паузы не извлекается из ошибки: %v", err)
	}

	if rateLimit.RetryAfter != pause {
		t.Errorf("неожиданная пауза: got %s, want %s", rateLimit.RetryAfter, pause)
	}

	if repo.rescheduleCalls != 0 {
		t.Error("отказ по лимиту увеличил персональную отсрочку заказа")
	}

	if repo.processingCalls != 0 || len(repo.appliedResults) != 0 {
		t.Error("отказ по лимиту изменил состояние заказа")
	}
}

// TestAccrualsProcessReturnsCancellationWithoutRescheduling закрепляет, что
// остановка сервиса не учитывается как неудача заказа.
func TestAccrualsProcessReturnsCancellationWithoutRescheduling(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	client := &accrualClientStub{err: context.Canceled}
	repo := &accrualRepositoryStub{}

	err := newAccruals(repo, client).Process(ctx, newJob(t, 0))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ожидалась отмена контекста, получено: %v", err)
	}

	if repo.rescheduleCalls != 0 {
		t.Error("остановка сервиса учтена как неудача заказа")
	}
}

// TestAccrualsProcessAcceptsAlreadyAppliedResult закрепляет сценарий
// «Результат расчёта получен повторно»: повтор не является отказом обработки.
func TestAccrualsProcessAcceptsAlreadyAppliedResult(t *testing.T) {
	client := &accrualClientStub{result: domain.AccrualResult{Status: domain.OrderStatusInvalid}}
	repo := &accrualRepositoryStub{applied: false}

	if err := newAccruals(repo, client).Process(t.Context(), newJob(t, 0)); err != nil {
		t.Errorf("повторное применение признано отказом: %v", err)
	}
}

// TestAccrualsProcessPropagatesRepositoryFailure закрепляет, что сбой
// хранилища доходит до вызывающей стороны, а не теряется.
func TestAccrualsProcessPropagatesRepositoryFailure(t *testing.T) {
	client := &accrualClientStub{result: domain.AccrualResult{Status: domain.OrderStatusProcessed}}
	repo := &accrualRepositoryStub{applyErr: errRepository}

	err := newAccruals(repo, client).Process(t.Context(), newJob(t, 0))
	if !errors.Is(err, errRepository) {
		t.Errorf("ошибка хранилища подменена: %v", err)
	}
}

// TestAccrualsClaimPassesBatchAndLease закрепляет передачу параметров выборки
// в хранилище без изменений.
func TestAccrualsClaimPassesBatchAndLease(t *testing.T) {
	repo := &accrualRepositoryStub{jobs: []domain.AccrualJob{newJob(t, 0)}}

	jobs, err := newAccruals(repo, &accrualClientStub{}).Claim(t.Context(), 7, time.Minute)
	if err != nil {
		t.Fatalf("выборка заданий: %v", err)
	}

	if len(jobs) != 1 {
		t.Errorf("неожиданное число заданий: got %d, want 1", len(jobs))
	}

	if repo.claimedBatch != 7 || repo.claimedLease != time.Minute {
		t.Errorf("хранилище получило неожиданные параметры: порция %d, аренда %s",
			repo.claimedBatch, repo.claimedLease)
	}
}

// TestAccrualsClaimPropagatesFailure закрепляет, что сбой выборки не выглядит
// как отсутствие заданий.
func TestAccrualsClaimPropagatesFailure(t *testing.T) {
	repo := &accrualRepositoryStub{claimErr: errRepository}

	jobs, err := newAccruals(repo, &accrualClientStub{}).Claim(t.Context(), 1, time.Minute)
	if !errors.Is(err, errRepository) {
		t.Fatalf("ошибка хранилища подменена: %v", err)
	}

	if jobs != nil {
		t.Error("при ошибке возвращены задания")
	}
}

// TestAccrualsReleasePassesNumbers закрепляет освобождение занятой порции:
// хранилище получает номера всех заданий и заданную отсрочку.
func TestAccrualsReleasePassesNumbers(t *testing.T) {
	repo := &accrualRepositoryStub{}
	jobs := []domain.AccrualJob{newJob(t, 0), newJob(t, 0)}

	if err := newAccruals(repo, &accrualClientStub{}).Release(t.Context(), jobs, time.Minute); err != nil {
		t.Fatalf("освобождение заданий: %v", err)
	}

	if len(repo.releasedNumbers) != len(jobs) {
		t.Errorf("освобождено номеров: %d, ожидалось %d", len(repo.releasedNumbers), len(jobs))
	}

	if repo.releaseDelay != time.Minute {
		t.Errorf("неожиданная отсрочка освобождения: got %s, want %s", repo.releaseDelay, time.Minute)
	}
}

// TestAccrualsReleaseAcceptsEmptyBatch закрепляет, что освобождение пустой
// порции не обращается к хранилищу: воркер вызывает его и когда освобождать
// нечего.
func TestAccrualsReleaseAcceptsEmptyBatch(t *testing.T) {
	repo := &accrualRepositoryStub{}

	if err := newAccruals(repo, &accrualClientStub{}).Release(t.Context(), nil, time.Minute); err != nil {
		t.Fatalf("освобождение пустой порции: %v", err)
	}

	if repo.releaseCalls != 0 {
		t.Errorf("хранилище вызвано при пустой порции: %d обращений", repo.releaseCalls)
	}
}

// TestAccrualsProcessDoesNotReadBalance закрепляет, что сервис не читает счёт:
// решение об изменении принимает база, а не код по прочитанному значению.
func TestAccrualsProcessDoesNotReadBalance(t *testing.T) {
	sum := decimal.RequireFromString("100")
	client := &accrualClientStub{result: domain.AccrualResult{
		Status:  domain.OrderStatusProcessed,
		Accrual: &sum,
	}}
	repo := &accrualRepositoryStub{applied: true}

	if err := newAccruals(repo, client).Process(t.Context(), newJob(t, 0)); err != nil {
		t.Fatalf("обработка задания: %v", err)
	}

	// Интерфейс хранилища расчёта вовсе не содержит чтения счёта, поэтому
	// достаточно убедиться, что применение выполнено одним вызовом.
	if len(repo.appliedResults) != 1 {
		t.Errorf("начисление применено не одним вызовом: %d", len(repo.appliedResults))
	}
}
