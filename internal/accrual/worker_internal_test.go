package accrual

import (
	"sync"
	"testing"
	"time"

	"gophermart/internal/domain"
)

// newStateJobs собирает задания с указанными номерами заказов.
func newStateJobs(t *testing.T, numbers ...string) []domain.AccrualJob {
	t.Helper()

	jobs := make([]domain.AccrualJob, 0, len(numbers))

	for _, raw := range numbers {
		number, err := domain.ParseOrderNumber(raw)
		if err != nil {
			t.Fatalf("разбор номера заказа %s: %v", raw, err)
		}

		jobs = append(jobs, domain.AccrualJob{Number: number})
	}

	return jobs
}

// TestBatchStateNotPausedInitially закрепляет, что порция без отказов по
// лимиту не приостановлена и ничего не откладывает.
func TestBatchStateNotPausedInitially(t *testing.T) {
	var batch batchState

	if batch.paused() {
		t.Error("новая порция приостановлена")
	}

	retryAfter, remaining, paused := batch.outcome()
	if paused || retryAfter != 0 || len(remaining) != 0 {
		t.Errorf("исход новой порции: пауза %s, отложено %d, приостановлена %t", retryAfter, len(remaining), paused)
	}
}

// TestBatchStateKeepsLongestRetryAfterAndCollectsRemaining закрепляет выбор
// наибольшего времени ожидания и накопление отложенных заданий: и
// отклонённых по лимиту, и не начатых из-за приостановки.
func TestBatchStateKeepsLongestRetryAfterAndCollectsRemaining(t *testing.T) {
	jobs := newStateJobs(t, "9278923470", "12345678903", "346436439", "4561261212345467")

	var batch batchState

	batch.pause(jobs[0], 10*time.Second)
	batch.skip(jobs[1])
	batch.pause(jobs[2], 30*time.Second)
	batch.pause(jobs[3], 20*time.Second)

	if !batch.paused() {
		t.Fatal("порция не приостановлена после отказа по лимиту")
	}

	retryAfter, remaining, paused := batch.outcome()

	if !paused {
		t.Error("исход не сообщает о приостановке")
	}

	if retryAfter != 30*time.Second {
		t.Errorf("пауза порции: got %s, want 30s", retryAfter)
	}

	if len(remaining) != len(jobs) {
		t.Fatalf("отложено заданий: %d, ожидалось %d", len(remaining), len(jobs))
	}

	for i, job := range jobs {
		if remaining[i].Number != job.Number {
			t.Errorf("отложенное задание %d: got %s, want %s", i, remaining[i].Number, job.Number)
		}
	}
}

// TestBatchStateIsSafeForConcurrentUse закрепляет безопасность состояния при
// одновременных отказах из goroutine порции; проверяется запуском с -race.
func TestBatchStateIsSafeForConcurrentUse(t *testing.T) {
	jobs := newStateJobs(t, "9278923470", "12345678903", "346436439", "4561261212345467")

	var (
		batch batchState
		wg    sync.WaitGroup
	)

	for i, job := range jobs {
		wg.Go(func() {
			batch.pause(job, time.Duration(i+1)*time.Second)
		})
	}

	wg.Wait()

	retryAfter, remaining, _ := batch.outcome()

	if retryAfter != time.Duration(len(jobs))*time.Second {
		t.Errorf("пауза порции: got %s, want %s", retryAfter, time.Duration(len(jobs))*time.Second)
	}

	if len(remaining) != len(jobs) {
		t.Errorf("отложено заданий: %d, ожидалось %d", len(remaining), len(jobs))
	}
}
