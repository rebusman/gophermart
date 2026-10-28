package integration_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"gophermart/internal/domain"
	"gophermart/internal/repository"
	"gophermart/internal/storage/postgres"
)

// Проверка на этапе компиляции: репозиторий заказов реализует и
// пользовательские операции, и операции фонового расчёта. Интерфейсы раздельны
// ради узких моков, реализация одна — обе группы работают с таблицей orders.
var (
	_ repository.OrderRepository   = (*postgres.OrderRepository)(nil)
	_ repository.AccrualRepository = (*postgres.OrderRepository)(nil)
)

// testLease — срок аренды задания в тестах репозитория.
const testLease = time.Hour

// newAccrualRepository поднимает пустую базу и возвращает репозитории заказов и
// пользователей поверх неё вместе с пулом.
func newAccrualRepository(t *testing.T) (*postgres.OrderRepository, *postgres.UserRepository, *pgxpool.Pool) {
	t.Helper()

	return newOrderRepository(t)
}

// seedOrder создаёт заказ владельца в состоянии NEW, доступный для проверки
// немедленно.
func seedOrder(t *testing.T, repo *postgres.OrderRepository, number string, userID domain.UserID) domain.OrderNumber {
	t.Helper()

	order := newOrder(t, number, userID)

	created, err := repo.CreateOrder(t.Context(), order)
	if err != nil {
		t.Fatalf("создание заказа %s: %v", number, err)
	}

	if !created {
		t.Fatalf("заказ %s не создан", number)
	}

	return order.Number
}

// orderState читает состояние расчёта, начисление и счётчик попыток заказа.
func orderState(t *testing.T, pool *pgxpool.Pool, number string) (string, string, int) {
	t.Helper()

	var (
		status   string
		accrual  string
		attempts int
	)

	const query = `
		SELECT status, coalesce(accrual::text, ''), attempts
		FROM orders WHERE number = $1`

	if err := pool.QueryRow(t.Context(), query, number).Scan(&status, &accrual, &attempts); err != nil {
		t.Fatalf("чтение состояния заказа %s: %v", number, err)
	}

	return status, accrual, attempts
}

// dueNow сообщает, доступен ли заказ для проверки прямо сейчас.
func dueNow(t *testing.T, pool *pgxpool.Pool, number string) bool {
	t.Helper()

	var due bool

	const query = `SELECT next_attempt_at <= now() FROM orders WHERE number = $1`

	if err := pool.QueryRow(t.Context(), query, number).Scan(&due); err != nil {
		t.Fatalf("чтение времени проверки заказа %s: %v", number, err)
	}

	return due
}

// makeDue возвращает заказ в выборку, сбрасывая время следующей проверки в
// прошлое. Помощник заменяет ожидание истечения аренды в тестах.
func makeDue(t *testing.T, pool *pgxpool.Pool, number string) {
	t.Helper()

	const query = `UPDATE orders SET next_attempt_at = now() - interval '1 second' WHERE number = $1`

	if _, err := pool.Exec(t.Context(), query, number); err != nil {
		t.Fatalf("возврат заказа %s в выборку: %v", number, err)
	}
}

// balanceOf читает обе суммы счёта пользователя.
func balanceOf(t *testing.T, pool *pgxpool.Pool, userID domain.UserID) (string, string) {
	t.Helper()

	var current, withdrawn string

	const query = `SELECT current::text, withdrawn_total::text FROM balances WHERE user_id = $1`

	if err := pool.QueryRow(t.Context(), query, userID.String()).Scan(&current, &withdrawn); err != nil {
		t.Fatalf("чтение счёта: %v", err)
	}

	return current, withdrawn
}

// money разбирает денежное значение из десятичной строки.
func money(t *testing.T, value string) *decimal.Decimal {
	t.Helper()

	parsed, err := decimal.NewFromString(value)
	if err != nil {
		t.Fatalf("разбор суммы %s: %v", value, err)
	}

	return &parsed
}

// TestClaimAccrualJobsSelectsOnlyPendingOrders закрепляет требование
// «Окончательные состояния расчёта не опрашиваются повторно» на уровне
// выборки: финализированные заказы в задания не попадают.
func TestClaimAccrualJobsSelectsOnlyPendingOrders(t *testing.T) {
	repo, users, _ := newAccrualRepository(t)
	userID := createOrderOwner(t, users, "gopher")

	pending := seedOrder(t, repo, orderNumberFirst, userID)
	processed := seedOrder(t, repo, orderNumberSecond, userID)
	invalid := seedOrder(t, repo, orderNumberThird, userID)

	job := domain.AccrualJob{Number: processed, UserID: userID}
	if _, err := repo.ApplyAccrual(t.Context(), job, domain.OrderStatusProcessed, money(t, "10")); err != nil {
		t.Fatalf("финализация заказа: %v", err)
	}

	job = domain.AccrualJob{Number: invalid, UserID: userID}
	if _, err := repo.ApplyAccrual(t.Context(), job, domain.OrderStatusInvalid, nil); err != nil {
		t.Fatalf("финализация заказа: %v", err)
	}

	jobs, err := repo.ClaimAccrualJobs(t.Context(), 10, testLease)
	if err != nil {
		t.Fatalf("выборка заданий: %v", err)
	}

	if len(jobs) != 1 {
		t.Fatalf("неожиданное число заданий: got %d, want 1", len(jobs))
	}

	if jobs[0].Number != pending {
		t.Errorf("выбран неожиданный заказ: got %s, want %s", jobs[0].Number, pending)
	}

	if jobs[0].UserID != userID {
		t.Errorf("неожиданный владелец задания: got %s, want %s", jobs[0].UserID, userID)
	}
}

// TestClaimAccrualJobsLeasesSelectedOrders закрепляет аренду: занятое задание
// немедленно выходит из выборки, в том числе для другого экземпляра.
func TestClaimAccrualJobsLeasesSelectedOrders(t *testing.T) {
	repo, users, pool := newAccrualRepository(t)
	userID := createOrderOwner(t, users, "gopher")

	seedOrder(t, repo, orderNumberFirst, userID)

	first, err := repo.ClaimAccrualJobs(t.Context(), 10, testLease)
	if err != nil {
		t.Fatalf("первая выборка: %v", err)
	}

	if len(first) != 1 {
		t.Fatalf("неожиданное число заданий в первой выборке: got %d, want 1", len(first))
	}

	second, err := repo.ClaimAccrualJobs(t.Context(), 10, testLease)
	if err != nil {
		t.Fatalf("вторая выборка: %v", err)
	}

	if len(second) != 0 {
		t.Errorf("занятое задание выбрано повторно: %d заданий", len(second))
	}

	if dueNow(t, pool, orderNumberFirst) {
		t.Error("время следующей проверки не сдвинуто вперёд арендой")
	}

	// По истечении аренды заказ возвращается в работу сам, без отдельной
	// процедуры уборки зависших заданий.
	makeDue(t, pool, orderNumberFirst)

	third, err := repo.ClaimAccrualJobs(t.Context(), 10, testLease)
	if err != nil {
		t.Fatalf("выборка после истечения аренды: %v", err)
	}

	if len(third) != 1 {
		t.Errorf("заказ не вернулся в выборку после истечения аренды: %d заданий", len(third))
	}
}

// TestClaimAccrualJobsRespectsBatchSize закрепляет ограничение порции: за один
// цикл занимается не больше заданий, чем задано размером.
func TestClaimAccrualJobsRespectsBatchSize(t *testing.T) {
	repo, users, _ := newAccrualRepository(t)
	userID := createOrderOwner(t, users, "gopher")

	seedOrder(t, repo, orderNumberFirst, userID)
	seedOrder(t, repo, orderNumberSecond, userID)
	seedOrder(t, repo, orderNumberThird, userID)

	jobs, err := repo.ClaimAccrualJobs(t.Context(), 2, testLease)
	if err != nil {
		t.Fatalf("выборка заданий: %v", err)
	}

	if len(jobs) != 2 {
		t.Errorf("порция не ограничена размером: got %d, want 2", len(jobs))
	}
}

// TestClaimAccrualJobsOrdersByWaitingTime закрепляет приоритет заказов,
// ожидающих дольше всех, — и в том, какие заказы попадают в порцию, и в том,
// в каком порядке они выдаются.
//
// Порядок выдачи проверяется целиком, а не по первому элементу: RETURNING сам
// по себе не упорядочен, и проверка одной позиции проходила бы по совпадению
// с алфавитным порядком номеров, который планировщик выбирает при записи через
// полусоединение.
//
// Времена проверки задаются в порядке, обратном алфавитному порядку номеров:
// так совпадение двух порядков исключено, и тест отличает сортировку по
// времени ожидания от сортировки по номеру.
func TestClaimAccrualJobsOrdersByWaitingTime(t *testing.T) {
	repo, users, pool := newAccrualRepository(t)
	userID := createOrderOwner(t, users, "gopher")

	// Номера, упорядоченные по возрастанию как строки.
	ascending := []string{orderNumberSecond, orderNumberThird, orderNumberFirst}

	for _, number := range ascending {
		seedOrder(t, repo, number, userID)
	}

	const reschedule = `UPDATE orders SET next_attempt_at = now() - make_interval(mins => $2) WHERE number = $1`

	// Чем позже номер в алфавитном порядке, тем дольше он ждёт.
	for i, number := range ascending {
		if _, err := pool.Exec(t.Context(), reschedule, number, i+1); err != nil {
			t.Fatalf("сдвиг времени проверки %s: %v", number, err)
		}
	}

	want := []string{orderNumberFirst, orderNumberThird, orderNumberSecond}

	jobs, err := repo.ClaimAccrualJobs(t.Context(), len(want), testLease)
	if err != nil {
		t.Fatalf("выборка заданий: %v", err)
	}

	if len(jobs) != len(want) {
		t.Fatalf("неожиданное число заданий: got %d, want %d", len(jobs), len(want))
	}

	for i, number := range want {
		if jobs[i].Number.String() != number {
			t.Errorf("нарушен порядок выдачи: позиция %d содержит %s, ожидался %s",
				i, jobs[i].Number, number)
		}
	}
}

// TestClaimAccrualJobsSelectsOldestWithinBatchSize закрепляет, что при порции
// меньше числа ожидающих заказов выбираются именно ожидающие дольше всех.
func TestClaimAccrualJobsSelectsOldestWithinBatchSize(t *testing.T) {
	repo, users, pool := newAccrualRepository(t)
	userID := createOrderOwner(t, users, "gopher")

	// Заказ, ожидающий дольше всех, выбран алфавитно последним: сортировка по
	// номеру дала бы другой ответ.
	seedOrder(t, repo, orderNumberSecond, userID)
	seedOrder(t, repo, orderNumberThird, userID)
	seedOrder(t, repo, orderNumberFirst, userID)

	const older = `UPDATE orders SET next_attempt_at = now() - interval '1 hour' WHERE number = $1`
	if _, err := pool.Exec(t.Context(), older, orderNumberFirst); err != nil {
		t.Fatalf("сдвиг времени проверки: %v", err)
	}

	jobs, err := repo.ClaimAccrualJobs(t.Context(), 1, testLease)
	if err != nil {
		t.Fatalf("выборка заданий: %v", err)
	}

	if len(jobs) != 1 {
		t.Fatalf("неожиданное число заданий: got %d, want 1", len(jobs))
	}

	if jobs[0].Number.String() != orderNumberFirst {
		t.Errorf("в порцию попал не самый долго ожидающий заказ: got %s, want %s",
			jobs[0].Number, orderNumberFirst)
	}
}

// TestClaimAccrualJobsOrdersDeterministicallyOnEqualTimestamps закрепляет
// второй ключ сортировки: при совпадающих метках времени порядок определён
// номером заказа, а не планом запроса.
func TestClaimAccrualJobsOrdersDeterministicallyOnEqualTimestamps(t *testing.T) {
	repo, users, pool := newAccrualRepository(t)
	userID := createOrderOwner(t, users, "gopher")

	numbers := []string{orderNumberFirst, orderNumberSecond, orderNumberThird}
	for _, number := range numbers {
		seedOrder(t, repo, number, userID)
	}

	const sameTime = `UPDATE orders SET next_attempt_at = '2020-12-09T16:09:57Z'`
	if _, err := pool.Exec(t.Context(), sameTime); err != nil {
		t.Fatalf("выравнивание времени проверки: %v", err)
	}

	jobs, err := repo.ClaimAccrualJobs(t.Context(), len(numbers), testLease)
	if err != nil {
		t.Fatalf("выборка заданий: %v", err)
	}

	if len(jobs) != len(numbers) {
		t.Fatalf("неожиданное число заданий: got %d, want %d", len(jobs), len(numbers))
	}

	for i := 1; i < len(jobs); i++ {
		if jobs[i-1].Number.String() > jobs[i].Number.String() {
			t.Errorf("порядок при совпадающих метках не определён номером: %s предшествует %s",
				jobs[i-1].Number, jobs[i].Number)
		}
	}
}

// TestClaimAccrualJobsIsSafeForConcurrentInstances закрепляет сценарий «Два
// экземпляра выбирают задания одновременно»: ни один заказ не достаётся обоим.
func TestClaimAccrualJobsIsSafeForConcurrentInstances(t *testing.T) {
	repo, users, _ := newAccrualRepository(t)
	userID := createOrderOwner(t, users, "gopher")

	numbers := []string{orderNumberFirst, orderNumberSecond, orderNumberThird}
	for _, number := range numbers {
		seedOrder(t, repo, number, userID)
	}

	const instances = 4

	claimed := make([][]domain.AccrualJob, instances)
	errs := make([]error, instances)

	var wg sync.WaitGroup

	for i := range instances {
		wg.Go(func() {
			claimed[i], errs[i] = repo.ClaimAccrualJobs(t.Context(), len(numbers), testLease)
		})
	}

	wg.Wait()

	seen := make(map[domain.OrderNumber]int)

	for i, err := range errs {
		if err != nil {
			t.Errorf("выборка экземпляра %d завершилась ошибкой: %v", i, err)
		}

		for _, job := range claimed[i] {
			seen[job.Number]++
		}
	}

	for number, count := range seen {
		if count != 1 {
			t.Errorf("заказ %s выбран %d раз, ожидался ровно один", number, count)
		}
	}

	if len(seen) != len(numbers) {
		t.Errorf("выбрано заказов: %d, ожидалось %d", len(seen), len(numbers))
	}
}

// TestApplyAccrualCreditsBalanceOnce закрепляет сценарий «Успешное применение
// результата»: заказ финализирован, счёт вырос ровно на сумму начисления,
// сумма списаний не тронута.
func TestApplyAccrualCreditsBalanceOnce(t *testing.T) {
	repo, users, pool := newAccrualRepository(t)
	userID := createOrderOwner(t, users, "gopher")
	number := seedOrder(t, repo, orderNumberFirst, userID)

	job := domain.AccrualJob{Number: number, UserID: userID}

	applied, err := repo.ApplyAccrual(t.Context(), job, domain.OrderStatusProcessed, money(t, "729.98"))
	if err != nil {
		t.Fatalf("применение начисления: %v", err)
	}

	if !applied {
		t.Error("первое применение отмечено как повторное")
	}

	status, accrual, _ := orderState(t, pool, orderNumberFirst)
	if status != "PROCESSED" {
		t.Errorf("неожиданное состояние заказа: got %s, want PROCESSED", status)
	}

	if accrual != "729.98" {
		t.Errorf("неожиданная сумма начисления: got %s, want 729.98", accrual)
	}

	current, withdrawn := balanceOf(t, pool, userID)
	if current != "729.98" {
		t.Errorf("неожиданный остаток: got %s, want 729.98", current)
	}

	if withdrawn != "0.00" {
		t.Errorf("начисление изменило сумму списаний: got %s, want 0.00", withdrawn)
	}
}

// TestApplyAccrualIsIdempotent закрепляет сценарий «Результат расчёта получен
// повторно»: повтор не изменяет ни счёт, ни сумму начисления.
func TestApplyAccrualIsIdempotent(t *testing.T) {
	repo, users, pool := newAccrualRepository(t)
	userID := createOrderOwner(t, users, "gopher")
	number := seedOrder(t, repo, orderNumberFirst, userID)

	job := domain.AccrualJob{Number: number, UserID: userID}

	if _, err := repo.ApplyAccrual(t.Context(), job, domain.OrderStatusProcessed, money(t, "100")); err != nil {
		t.Fatalf("первое применение: %v", err)
	}

	applied, err := repo.ApplyAccrual(t.Context(), job, domain.OrderStatusProcessed, money(t, "500"))
	if err != nil {
		t.Fatalf("повторное применение: %v", err)
	}

	if applied {
		t.Error("повтор отмечен как выполненный переход")
	}

	_, accrual, _ := orderState(t, pool, orderNumberFirst)
	if accrual != "100.00" {
		t.Errorf("повтор подменил сумму начисления: got %s, want 100.00", accrual)
	}

	current, _ := balanceOf(t, pool, userID)
	if current != "100.00" {
		t.Errorf("повтор изменил счёт: got %s, want 100.00", current)
	}
}

// TestApplyAccrualIsSingleCreditUnderConcurrency закрепляет сценарий «Один
// заказ обработан двумя экземплярами одновременно»: счёт растёт ровно один раз.
func TestApplyAccrualIsSingleCreditUnderConcurrency(t *testing.T) {
	repo, users, pool := newAccrualRepository(t)
	userID := createOrderOwner(t, users, "gopher")
	number := seedOrder(t, repo, orderNumberFirst, userID)

	job := domain.AccrualJob{Number: number, UserID: userID}

	const instances = 4

	applied := make([]bool, instances)
	errs := make([]error, instances)

	var wg sync.WaitGroup

	for i := range instances {
		wg.Go(func() {
			applied[i], errs[i] = repo.ApplyAccrual(t.Context(), job, domain.OrderStatusProcessed, money(t, "250"))
		})
	}

	wg.Wait()

	var transitions int

	for i, err := range errs {
		if err != nil {
			t.Errorf("применение экземпляра %d завершилось ошибкой: %v", i, err)
		}

		if applied[i] {
			transitions++
		}
	}

	if transitions != 1 {
		t.Errorf("переход в окончательное состояние выполнен %d раз, ожидался один", transitions)
	}

	current, _ := balanceOf(t, pool, userID)
	if current != "250.00" {
		t.Errorf("двойной кредит: остаток %s, ожидалось 250.00", current)
	}
}

// TestApplyAccrualKeepsAbsentAccrualForFinalStatus закрепляет сценарий
// «Расчёт завершён без вознаграждения»: сумма остаётся NULL, счёт не меняется.
func TestApplyAccrualKeepsAbsentAccrualForFinalStatus(t *testing.T) {
	repo, users, pool := newAccrualRepository(t)
	userID := createOrderOwner(t, users, "gopher")

	tests := []struct {
		name   string
		number string
		status domain.OrderStatus
	}{
		{name: "завершён без вознаграждения", number: orderNumberFirst, status: domain.OrderStatusProcessed},
		{name: "отказ в начислении", number: orderNumberSecond, status: domain.OrderStatusInvalid},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			number := seedOrder(t, repo, test.number, userID)
			job := domain.AccrualJob{Number: number, UserID: userID}

			if _, err := repo.ApplyAccrual(t.Context(), job, test.status, nil); err != nil {
				t.Fatalf("применение результата: %v", err)
			}

			status, accrual, _ := orderState(t, pool, test.number)
			if status != test.status.String() {
				t.Errorf("неожиданное состояние: got %s, want %s", status, test.status)
			}

			if accrual != "" {
				t.Errorf("сумма подставлена вместо отсутствия: %q", accrual)
			}
		})
	}

	current, withdrawn := balanceOf(t, pool, userID)
	if current != "0.00" || withdrawn != "0.00" {
		t.Errorf("счёт изменён финализацией без вознаграждения: current %s, withdrawn %s", current, withdrawn)
	}
}

// TestApplyAccrualReportsMissingBalance закрепляет обработку отсутствия счёта
// как нарушения инварианта: транзакция откатывается целиком.
func TestApplyAccrualReportsMissingBalance(t *testing.T) {
	repo, users, pool := newAccrualRepository(t)
	userID := createOrderOwner(t, users, "gopher")
	number := seedOrder(t, repo, orderNumberFirst, userID)

	// Счёт заводится вместе с пользователем, поэтому его отсутствие
	// воспроизводится только удалением строки напрямую.
	if _, err := pool.Exec(t.Context(), `DELETE FROM balances WHERE user_id = $1`, userID.String()); err != nil {
		t.Fatalf("удаление счёта: %v", err)
	}

	job := domain.AccrualJob{Number: number, UserID: userID}

	applied, err := repo.ApplyAccrual(t.Context(), job, domain.OrderStatusProcessed, money(t, "100"))
	if !errors.Is(err, domain.ErrBalanceNotFound) {
		t.Fatalf("ожидалась ошибка отсутствия счёта, получено: %v", err)
	}

	if applied {
		t.Error("применение отмечено успешным при отсутствии счёта")
	}

	status, accrual, _ := orderState(t, pool, orderNumberFirst)
	if status != "NEW" || accrual != "" {
		t.Errorf("транзакция не откатилась: статус %s, начисление %q", status, accrual)
	}
}

// TestMarkAccrualProcessingResetsAttempts закрепляет сброс счётчика попыток на
// успешном неокончательном ответе.
func TestMarkAccrualProcessingResetsAttempts(t *testing.T) {
	repo, users, pool := newAccrualRepository(t)
	userID := createOrderOwner(t, users, "gopher")
	number := seedOrder(t, repo, orderNumberFirst, userID)

	for range 3 {
		if err := repo.RescheduleAccrualJob(t.Context(), number, time.Minute); err != nil {
			t.Fatalf("перенос проверки: %v", err)
		}
	}

	if _, _, attempts := orderState(t, pool, orderNumberFirst); attempts != 3 {
		t.Fatalf("счётчик попыток не рос: got %d, want 3", attempts)
	}

	if err := repo.MarkAccrualProcessing(t.Context(), number, time.Minute); err != nil {
		t.Fatalf("отметка выполняющегося расчёта: %v", err)
	}

	status, _, attempts := orderState(t, pool, orderNumberFirst)
	if status != "PROCESSING" {
		t.Errorf("неожиданное состояние: got %s, want PROCESSING", status)
	}

	if attempts != 0 {
		t.Errorf("счётчик попыток не обнулён: got %d, want 0", attempts)
	}
}

// TestRescheduleAccrualJobKeepsStatus закрепляет требование «Сбой внешней
// системы не является результатом расчёта» на уровне хранилища.
func TestRescheduleAccrualJobKeepsStatus(t *testing.T) {
	repo, users, pool := newAccrualRepository(t)
	userID := createOrderOwner(t, users, "gopher")
	number := seedOrder(t, repo, orderNumberFirst, userID)

	if err := repo.RescheduleAccrualJob(t.Context(), number, time.Hour); err != nil {
		t.Fatalf("перенос проверки: %v", err)
	}

	status, accrual, attempts := orderState(t, pool, orderNumberFirst)

	if status != "NEW" {
		t.Errorf("сбой изменил состояние расчёта: got %s, want NEW", status)
	}

	if accrual != "" {
		t.Errorf("сбой создал начисление: %q", accrual)
	}

	if attempts != 1 {
		t.Errorf("счётчик попыток не увеличен: got %d, want 1", attempts)
	}

	if dueNow(t, pool, orderNumberFirst) {
		t.Error("время следующей проверки не перенесено")
	}
}

// TestAccrualUpdatesSkipFinalizedOrders закрепляет окончательность состояний:
// ни перенос проверки, ни отметка выполняющегося расчёта не трогают
// финализированный заказ.
func TestAccrualUpdatesSkipFinalizedOrders(t *testing.T) {
	repo, users, pool := newAccrualRepository(t)
	userID := createOrderOwner(t, users, "gopher")
	number := seedOrder(t, repo, orderNumberFirst, userID)

	job := domain.AccrualJob{Number: number, UserID: userID}
	if _, err := repo.ApplyAccrual(t.Context(), job, domain.OrderStatusInvalid, nil); err != nil {
		t.Fatalf("финализация заказа: %v", err)
	}

	if err := repo.RescheduleAccrualJob(t.Context(), number, time.Minute); err != nil {
		t.Fatalf("перенос проверки: %v", err)
	}

	if err := repo.MarkAccrualProcessing(t.Context(), number, time.Minute); err != nil {
		t.Fatalf("отметка выполняющегося расчёта: %v", err)
	}

	status, _, attempts := orderState(t, pool, orderNumberFirst)

	if status != "INVALID" {
		t.Errorf("окончательное состояние изменено: got %s, want INVALID", status)
	}

	if attempts != 0 {
		t.Errorf("счётчик попыток финализированного заказа изменён: got %d, want 0", attempts)
	}
}

// TestReleaseAccrualJobsKeepsAttempts закрепляет освобождение порции при
// превышении лимита запросов: заказы возвращаются в работу, но их персональная
// отсрочка не растёт.
func TestReleaseAccrualJobsKeepsAttempts(t *testing.T) {
	repo, users, pool := newAccrualRepository(t)
	userID := createOrderOwner(t, users, "gopher")

	numbers := []domain.OrderNumber{
		seedOrder(t, repo, orderNumberFirst, userID),
		seedOrder(t, repo, orderNumberSecond, userID),
	}

	if _, err := repo.ClaimAccrualJobs(t.Context(), len(numbers), testLease); err != nil {
		t.Fatalf("выборка заданий: %v", err)
	}

	// Освобождение задним числом возвращает заказы в выборку немедленно.
	if err := repo.ReleaseAccrualJobs(t.Context(), numbers, -time.Second); err != nil {
		t.Fatalf("освобождение заданий: %v", err)
	}

	for _, number := range numbers {
		if _, _, attempts := orderState(t, pool, number.String()); attempts != 0 {
			t.Errorf("освобождение увеличило счётчик попыток заказа %s: %d", number, attempts)
		}

		if !dueNow(t, pool, number.String()) {
			t.Errorf("заказ %s не вернулся в выборку после освобождения", number)
		}
	}

	jobs, err := repo.ClaimAccrualJobs(t.Context(), len(numbers), testLease)
	if err != nil {
		t.Fatalf("повторная выборка: %v", err)
	}

	if len(jobs) != len(numbers) {
		t.Errorf("освобождённые задания не выбраны повторно: got %d, want %d", len(jobs), len(numbers))
	}
}

// TestReleaseAccrualJobsAcceptsEmptyList закрепляет, что освобождать пустой
// перечень безопасно: воркер вызывает освобождение и когда порция уже пуста.
func TestReleaseAccrualJobsAcceptsEmptyList(t *testing.T) {
	repo, _, _ := newAccrualRepository(t)

	if err := repo.ReleaseAccrualJobs(t.Context(), nil, time.Minute); err != nil {
		t.Errorf("освобождение пустого перечня завершилось ошибкой: %v", err)
	}
}
