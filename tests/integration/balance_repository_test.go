package integration_test

import (
	"errors"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"gophermart/internal/domain"
	"gophermart/internal/storage/postgres"
	"gophermart/migrations"
	"gophermart/tests/testutil"
)

// newBalanceRepository поднимает пустую базу, применяет к ней миграции и
// возвращает репозитории счёта и пользователей поверх неё вместе с пулом,
// который закрывается по завершении теста.
//
// Репозиторий пользователей нужен потому, что счёт заводится вместе с учётной
// записью: счёта без существующего пользователя в схеме не бывает.
func newBalanceRepository(t *testing.T) (*postgres.BalanceRepository, *postgres.UserRepository, *pgxpool.Pool) {
	t.Helper()

	dsn := testutil.NewDatabase(t)

	if err := postgres.Migrate(t.Context(), dsn, migrations.FS); err != nil {
		t.Fatalf("применение миграций: %v", err)
	}

	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("создание пула подключений: %v", err)
	}

	t.Cleanup(pool.Close)

	return postgres.NewBalanceRepository(pool), postgres.NewUserRepository(pool), pool
}

// creditBalance начисляет пользователю баллы напрямую в базу.
//
// Фонового расчёта начислений в этом изменении ещё нет, а списывать нечего,
// пока счёт пуст: помощник заменяет собой будущего второго писателя счёта.
func creditBalance(t *testing.T, pool *pgxpool.Pool, userID domain.UserID, amount string) {
	t.Helper()

	const credit = `UPDATE balances SET current = current + $2 WHERE user_id = $1`

	if _, err := pool.Exec(t.Context(), credit, userID.String(), amount); err != nil {
		t.Fatalf("начисление баллов: %v", err)
	}
}

// newWithdrawal собирает списание с разобранным номером заказа и суммой,
// заданной десятичной строкой.
func newWithdrawal(t *testing.T, userID domain.UserID, number, sum string) domain.Withdrawal {
	t.Helper()

	parsed, err := domain.ParseOrderNumber(number)
	if err != nil {
		t.Fatalf("разбор номера заказа %s: %v", number, err)
	}

	amount, err := decimal.NewFromString(sum)
	if err != nil {
		t.Fatalf("разбор суммы %s: %v", sum, err)
	}

	return domain.Withdrawal{UserID: userID, OrderNumber: parsed, Sum: amount}
}

// countWithdrawals возвращает число списаний пользователя.
func countWithdrawals(t *testing.T, pool *pgxpool.Pool, userID domain.UserID) int {
	t.Helper()

	var count int

	const query = `SELECT count(*) FROM withdrawals WHERE user_id = $1`

	if err := pool.QueryRow(t.Context(), query, userID.String()).Scan(&count); err != nil {
		t.Fatalf("подсчёт списаний: %v", err)
	}

	return count
}

// requireBalance читает счёт и сверяет обе суммы с ожидаемыми.
func requireBalance(t *testing.T, repo *postgres.BalanceRepository, userID domain.UserID, current, withdrawn string) {
	t.Helper()

	balance, err := repo.Balance(t.Context(), userID)
	if err != nil {
		t.Fatalf("чтение счёта: %v", err)
	}

	if got := balance.Current.String(); got != current {
		t.Errorf("неожиданная текущая сумма баллов: got %s, want %s", got, current)
	}

	if got := balance.Withdrawn.String(); got != withdrawn {
		t.Errorf("неожиданная сумма списаний: got %s, want %s", got, withdrawn)
	}
}

// TestBalanceRepositoryReadsZeroBalanceAfterRegistration закрепляет сценарий
// «Счёт без операций»: сразу после регистрации счёт существует и содержит
// нули, а не отсутствует.
func TestBalanceRepositoryReadsZeroBalanceAfterRegistration(t *testing.T) {
	repo, users, _ := newBalanceRepository(t)
	userID := createOrderOwner(t, users, "gopher")

	requireBalance(t, repo, userID, "0", "0")
}

// TestBalanceRepositoryReadsFractionalAmounts закрепляет требование
// «Представление сумм счёта в ответе» на уровне хранилища: дробные суммы
// читаются без потери точности.
func TestBalanceRepositoryReadsFractionalAmounts(t *testing.T) {
	repo, users, pool := newBalanceRepository(t)
	userID := createOrderOwner(t, users, "gopher")

	creditBalance(t, pool, userID, "1000.55")

	if _, err := repo.Withdraw(t.Context(), newWithdrawal(t, userID, orderNumberFirst, "249.05")); err != nil {
		t.Fatalf("списание: %v", err)
	}

	requireBalance(t, repo, userID, "751.5", "249.05")
}

// TestBalanceRepositoryReportsMissingBalance закрепляет решение «Отсутствие
// строки счёта — внутренняя ошибка»: несуществующий счёт не подменяется
// нулями.
func TestBalanceRepositoryReportsMissingBalance(t *testing.T) {
	repo, _, _ := newBalanceRepository(t)

	_, err := repo.Balance(t.Context(), domain.NewUserID())
	if !errors.Is(err, domain.ErrBalanceNotFound) {
		t.Errorf("ожидалась ошибка отсутствия счёта, получено: %v", err)
	}
}

// TestBalanceRepositoryWithdrawChangesBothSums закрепляет сценарий «Успешное
// списание»: остаток уменьшается, а сумма списаний увеличивается ровно на
// списанную сумму.
func TestBalanceRepositoryWithdrawChangesBothSums(t *testing.T) {
	repo, users, pool := newBalanceRepository(t)
	userID := createOrderOwner(t, users, "gopher")

	creditBalance(t, pool, userID, "500")

	created, err := repo.Withdraw(t.Context(), newWithdrawal(t, userID, orderNumberFirst, "200"))
	if err != nil {
		t.Fatalf("списание: %v", err)
	}

	if !created {
		t.Error("первое списание отмечено как повтор")
	}

	requireBalance(t, repo, userID, "300", "200")

	if got := countWithdrawals(t, pool, userID); got != 1 {
		t.Errorf("неожиданное число списаний: got %d, want 1", got)
	}
}

// TestBalanceRepositoryWithdrawIsIdempotent закрепляет сценарии «Повторное
// списание с тем же номером заказа» и «Повтор с иной суммой»: счёт
// уменьшается один раз, а сохранённой остаётся сумма первого списания.
func TestBalanceRepositoryWithdrawIsIdempotent(t *testing.T) {
	repo, users, pool := newBalanceRepository(t)
	userID := createOrderOwner(t, users, "gopher")

	creditBalance(t, pool, userID, "500")

	if _, err := repo.Withdraw(t.Context(), newWithdrawal(t, userID, orderNumberFirst, "200")); err != nil {
		t.Fatalf("первое списание: %v", err)
	}

	created, err := repo.Withdraw(t.Context(), newWithdrawal(t, userID, orderNumberFirst, "50"))
	if err != nil {
		t.Fatalf("повторное списание: %v", err)
	}

	if created {
		t.Error("повтор отмечен как созданное списание")
	}

	requireBalance(t, repo, userID, "300", "200")

	if got := countWithdrawals(t, pool, userID); got != 1 {
		t.Errorf("повтор создал вторую запись: got %d, want 1", got)
	}

	history, err := repo.WithdrawalsByUser(t.Context(), userID)
	if err != nil {
		t.Fatalf("чтение истории: %v", err)
	}

	if got := history[0].Sum.String(); got != "200" {
		t.Errorf("повтор подменил сумму первого списания: got %s, want 200", got)
	}
}

// TestBalanceRepositoryWithdrawRejectsInsufficientFunds закрепляет сценарий
// «Недостаточно баллов»: отказ не оставляет ни изменения счёта, ни записи
// истории.
func TestBalanceRepositoryWithdrawRejectsInsufficientFunds(t *testing.T) {
	repo, users, pool := newBalanceRepository(t)
	userID := createOrderOwner(t, users, "gopher")

	creditBalance(t, pool, userID, "100")

	_, err := repo.Withdraw(t.Context(), newWithdrawal(t, userID, orderNumberFirst, "100.01"))
	if !errors.Is(err, domain.ErrInsufficientFunds) {
		t.Fatalf("ожидалась ошибка недостатка баллов, получено: %v", err)
	}

	requireBalance(t, repo, userID, "100", "0")

	if got := countWithdrawals(t, pool, userID); got != 0 {
		t.Errorf("отклонённое списание оставило запись: got %d, want 0", got)
	}
}

// TestBalanceRepositoryWithdrawAcceptsWholeRemainder закрепляет сценарий
// «Списание ровно на весь остаток»: граничное значение проходит, а остаток
// становится нулевым.
func TestBalanceRepositoryWithdrawAcceptsWholeRemainder(t *testing.T) {
	repo, users, pool := newBalanceRepository(t)
	userID := createOrderOwner(t, users, "gopher")

	creditBalance(t, pool, userID, "42.42")

	if _, err := repo.Withdraw(t.Context(), newWithdrawal(t, userID, orderNumberFirst, "42.42")); err != nil {
		t.Fatalf("списание всего остатка: %v", err)
	}

	requireBalance(t, repo, userID, "0", "42.42")
}

// TestBalanceRepositoryWithdrawIsIndependentBetweenUsers закрепляет сценарий
// «Списание другого пользователя по тому же номеру»: уникальность действует в
// пределах пользователя.
func TestBalanceRepositoryWithdrawIsIndependentBetweenUsers(t *testing.T) {
	repo, users, pool := newBalanceRepository(t)
	first := createOrderOwner(t, users, "gopher")
	second := createOrderOwner(t, users, "stranger")

	creditBalance(t, pool, first, "500")
	creditBalance(t, pool, second, "500")

	for _, userID := range []domain.UserID{first, second} {
		created, err := repo.Withdraw(t.Context(), newWithdrawal(t, userID, orderNumberFirst, "100"))
		if err != nil {
			t.Fatalf("списание пользователя %s: %v", userID, err)
		}

		if !created {
			t.Errorf("списание пользователя %s принято за повтор", userID)
		}
	}

	requireBalance(t, repo, first, "400", "100")
	requireBalance(t, repo, second, "400", "100")
}

// TestBalanceRepositoryConcurrentWithdrawalsKeepBalanceNonNegative закрепляет
// сценарий «Конкурентные списания, когда баллов хватает только на одно»:
// корректность обеспечивается условным изменением, а не проверкой в коде.
func TestBalanceRepositoryConcurrentWithdrawalsKeepBalanceNonNegative(t *testing.T) {
	repo, users, pool := newBalanceRepository(t)
	userID := createOrderOwner(t, users, "gopher")

	creditBalance(t, pool, userID, "100")

	numbers := []string{orderNumberFirst, orderNumberSecond}
	results := make([]error, len(numbers))

	var wg sync.WaitGroup

	for i, number := range numbers {
		wg.Go(func() {
			_, results[i] = repo.Withdraw(t.Context(), newWithdrawal(t, userID, number, "100"))
		})
	}

	wg.Wait()

	var succeeded, insufficient int

	for i, err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, domain.ErrInsufficientFunds):
			insufficient++
		default:
			t.Errorf("списание %s завершилось неожиданной ошибкой: %v", numbers[i], err)
		}
	}

	if succeeded != 1 || insufficient != 1 {
		t.Errorf("неожиданный исход конкурентных списаний: успешных %d, отказов по недостатку %d",
			succeeded, insufficient)
	}

	requireBalance(t, repo, userID, "0", "100")

	if got := countWithdrawals(t, pool, userID); got != 1 {
		t.Errorf("неожиданное число списаний: got %d, want 1", got)
	}
}

// TestBalanceRepositoryConcurrentRepeatWithdrawsOnce закрепляет сценарий
// «Конкурентный повтор одного номера»: счёт уменьшается ровно один раз.
func TestBalanceRepositoryConcurrentRepeatWithdrawsOnce(t *testing.T) {
	repo, users, pool := newBalanceRepository(t)
	userID := createOrderOwner(t, users, "gopher")

	creditBalance(t, pool, userID, "500")

	const attempts = 4

	results := make([]error, attempts)

	var wg sync.WaitGroup

	for i := range attempts {
		wg.Go(func() {
			_, results[i] = repo.Withdraw(t.Context(), newWithdrawal(t, userID, orderNumberFirst, "100"))
		})
	}

	wg.Wait()

	for i, err := range results {
		if err != nil {
			t.Errorf("попытка %d завершилась ошибкой: %v", i, err)
		}
	}

	requireBalance(t, repo, userID, "400", "100")

	if got := countWithdrawals(t, pool, userID); got != 1 {
		t.Errorf("конкурентный повтор создал лишние записи: got %d, want 1", got)
	}
}

// TestBalanceRepositoryListsWithdrawalsNewestFirst закрепляет требование
// «История списаний»: порядок от новых к старым и изоляция от чужих списаний.
func TestBalanceRepositoryListsWithdrawalsNewestFirst(t *testing.T) {
	repo, users, pool := newBalanceRepository(t)
	userID := createOrderOwner(t, users, "gopher")
	stranger := createOrderOwner(t, users, "stranger")

	creditBalance(t, pool, userID, "500")
	creditBalance(t, pool, stranger, "500")

	numbers := []string{orderNumberFirst, orderNumberSecond, orderNumberThird}
	for _, number := range numbers {
		if _, err := repo.Withdraw(t.Context(), newWithdrawal(t, userID, number, "100")); err != nil {
			t.Fatalf("списание %s: %v", number, err)
		}
	}

	if _, err := repo.Withdraw(t.Context(), newWithdrawal(t, stranger, orderNumberFirst, "100")); err != nil {
		t.Fatalf("списание другого пользователя: %v", err)
	}

	history, err := repo.WithdrawalsByUser(t.Context(), userID)
	if err != nil {
		t.Fatalf("чтение истории: %v", err)
	}

	if len(history) != len(numbers) {
		t.Fatalf("неожиданное число списаний в истории: got %d, want %d", len(history), len(numbers))
	}

	for i := 1; i < len(history); i++ {
		if history[i-1].ProcessedAt.Before(history[i].ProcessedAt) {
			t.Errorf("нарушен порядок от новых к старым: %v предшествует %v",
				history[i-1].ProcessedAt, history[i].ProcessedAt)
		}
	}

	for _, withdrawal := range history {
		if withdrawal.UserID != userID {
			t.Errorf("в истории оказалось чужое списание: %s", withdrawal.OrderNumber)
		}
	}
}

// TestBalanceRepositoryListsWithdrawalsDeterministicallyOnEqualTimestamps
// закрепляет второй ключ сортировки: при совпадающих метках времени порядок
// определён номером заказа.
func TestBalanceRepositoryListsWithdrawalsDeterministicallyOnEqualTimestamps(t *testing.T) {
	repo, users, pool := newBalanceRepository(t)
	userID := createOrderOwner(t, users, "gopher")

	creditBalance(t, pool, userID, "500")

	const insert = `INSERT INTO withdrawals (user_id, order_number, sum, processed_at)
		VALUES ($1, $2, $3, '2020-12-09T16:09:57Z')`

	numbers := []string{orderNumberFirst, orderNumberSecond, orderNumberThird}
	for _, number := range numbers {
		if _, err := pool.Exec(t.Context(), insert, userID.String(), number, "100"); err != nil {
			t.Fatalf("создание списания %s: %v", number, err)
		}
	}

	history, err := repo.WithdrawalsByUser(t.Context(), userID)
	if err != nil {
		t.Fatalf("чтение истории: %v", err)
	}

	for i := 1; i < len(history); i++ {
		if history[i-1].OrderNumber.String() < history[i].OrderNumber.String() {
			t.Errorf("нарушен порядок по номеру заказа: %s предшествует %s",
				history[i-1].OrderNumber, history[i].OrderNumber)
		}
	}
}

// TestBalanceRepositoryReturnsEmptyHistory закрепляет сценарий «У пользователя
// нет списаний» на уровне хранилища: пустой результат не является ошибкой.
func TestBalanceRepositoryReturnsEmptyHistory(t *testing.T) {
	repo, users, _ := newBalanceRepository(t)
	userID := createOrderOwner(t, users, "gopher")

	history, err := repo.WithdrawalsByUser(t.Context(), userID)
	if err != nil {
		t.Fatalf("чтение истории: %v", err)
	}

	if len(history) != 0 {
		t.Errorf("неожиданные списания у пользователя без операций: %d", len(history))
	}
}
