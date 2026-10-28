package service_test

import (
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"gophermart/internal/domain"
	"gophermart/internal/repository"
	"gophermart/internal/service"
)

// Проверка на этапе компиляции: конструктор сервиса счёта принимает
// единственную зависимость — хранилище, переданное аргументом. Глобального
// состояния и пакетных синглтонов в сборке сервиса нет.
var _ func(repository.BalanceRepository) *service.Balances = service.NewBalances

// newMoney разбирает денежное значение из десятичной строки.
func newMoney(t *testing.T, value string) decimal.Decimal {
	t.Helper()

	parsed, err := decimal.NewFromString(value)
	if err != nil {
		t.Fatalf("разбор денежного значения %s: %v", value, err)
	}

	return parsed
}

// TestBalancesReadsAccountState закрепляет сценарий «Счёт после списания»:
// сервис возвращает обе суммы такими, какими их хранит репозиторий.
func TestBalancesReadsAccountState(t *testing.T) {
	userID := domain.NewUserID()
	repo := &balanceRepositoryStub{
		balance: domain.Balance{
			UserID:    userID,
			Current:   newMoney(t, "500.5"),
			Withdrawn: newMoney(t, "42"),
		},
	}

	balance, err := service.NewBalances(repo).Balance(t.Context(), userID)
	if err != nil {
		t.Fatalf("чтение счёта: %v", err)
	}

	if got := balance.Current.String(); got != "500.5" {
		t.Errorf("неожиданная текущая сумма баллов: got %s, want 500.5", got)
	}

	if got := balance.Withdrawn.String(); got != "42" {
		t.Errorf("неожиданная сумма списаний: got %s, want 42", got)
	}

	if repo.balanceFor != userID {
		t.Errorf("счёт прочитан для чужого пользователя: got %s, want %s", repo.balanceFor, userID)
	}
}

// TestBalancesReadsZeroAccountState закрепляет сценарий «Счёт без операций»:
// нулевой счёт — обычный результат, а не особый случай.
func TestBalancesReadsZeroAccountState(t *testing.T) {
	repo := &balanceRepositoryStub{}

	balance, err := service.NewBalances(repo).Balance(t.Context(), domain.NewUserID())
	if err != nil {
		t.Fatalf("чтение счёта: %v", err)
	}

	if !balance.Current.IsZero() || !balance.Withdrawn.IsZero() {
		t.Errorf("нулевой счёт прочитан неверно: current %s, withdrawn %s",
			balance.Current, balance.Withdrawn)
	}
}

// TestBalancesPropagatesMissingBalance закрепляет решение «Отсутствие строки
// счёта — внутренняя ошибка»: сервис не подменяет её нулевыми суммами.
func TestBalancesPropagatesMissingBalance(t *testing.T) {
	repo := &balanceRepositoryStub{balanceErr: domain.ErrBalanceNotFound}

	balance, err := service.NewBalances(repo).Balance(t.Context(), domain.NewUserID())
	if !errors.Is(err, domain.ErrBalanceNotFound) {
		t.Fatalf("ожидалась ошибка отсутствия счёта, получено: %v", err)
	}

	if !balance.Current.IsZero() || !balance.Withdrawn.IsZero() {
		t.Error("при ошибке возвращён непустой счёт")
	}
}

// TestBalancesPropagatesRepositoryFailureOnRead закрепляет, что внутренняя
// ошибка хранилища доходит до вызывающей стороны как есть.
func TestBalancesPropagatesRepositoryFailureOnRead(t *testing.T) {
	repo := &balanceRepositoryStub{balanceErr: errRepository}

	_, err := service.NewBalances(repo).Balance(t.Context(), domain.NewUserID())
	if !errors.Is(err, errRepository) {
		t.Errorf("ошибка хранилища подменена: %v", err)
	}
}

// TestBalancesWithdrawRejectsNonPositiveSum закрепляет сценарий «Сумма
// списания равна нулю или отрицательна»: отказ происходит до обращения к
// хранилищу, а не по отказу базы данных.
func TestBalancesWithdrawRejectsNonPositiveSum(t *testing.T) {
	tests := []struct {
		name string
		sum  string
	}{
		{name: "нулевая сумма", sum: "0"},
		{name: "отрицательная сумма", sum: "-0.01"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := &balanceRepositoryStub{}

			err := service.NewBalances(repo).Withdraw(t.Context(),
				newOrderNumber(t), newMoney(t, test.sum), domain.NewUserID())

			if !errors.Is(err, domain.ErrNonPositiveWithdrawalSum) {
				t.Fatalf("ожидалась ошибка неположительной суммы, получено: %v", err)
			}

			if repo.withdrawCalls != 0 {
				t.Errorf("хранилище вызвано при неположительной сумме: %d обращений", repo.withdrawCalls)
			}
		})
	}
}

// TestBalancesWithdrawRejectsExcessivePrecision закрепляет отклонение суммы,
// которую колонка NUMERIC(18,2) округлила бы молча: отказ происходит до
// обращения к хранилищу, а не по отказу базы данных.
func TestBalancesWithdrawRejectsExcessivePrecision(t *testing.T) {
	tests := []struct {
		name string
		sum  string
	}{
		{name: "округляется до нуля", sum: "0.001"},
		{name: "рассогласует счёт", sum: "0.005"},
		{name: "меняет сумму списания", sum: "1.999"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := &balanceRepositoryStub{}

			err := service.NewBalances(repo).Withdraw(t.Context(),
				newOrderNumber(t), newMoney(t, test.sum), domain.NewUserID())

			if !errors.Is(err, domain.ErrWithdrawalSumTooPrecise) {
				t.Fatalf("ожидалась ошибка избыточной точности, получено: %v", err)
			}

			if repo.withdrawCalls != 0 {
				t.Errorf("хранилище вызвано при переточной сумме: %d обращений", repo.withdrawCalls)
			}
		})
	}
}

// TestBalancesWithdrawPassesWithdrawalToRepository закрепляет сценарий
// «Успешное списание»: сервис передаёт хранилищу владельца, номер и сумму и не
// читает остаток перед этим.
func TestBalancesWithdrawPassesWithdrawalToRepository(t *testing.T) {
	repo := &balanceRepositoryStub{created: true}
	userID := domain.NewUserID()
	number := newOrderNumber(t)

	err := service.NewBalances(repo).Withdraw(t.Context(), number, newMoney(t, "751.5"), userID)
	if err != nil {
		t.Fatalf("списание: %v", err)
	}

	if len(repo.withdrawn) != 1 {
		t.Fatalf("неожиданное число обращений к хранилищу: got %d, want 1", len(repo.withdrawn))
	}

	withdrawal := repo.withdrawn[0]

	if withdrawal.UserID != userID {
		t.Errorf("списание отнесено к чужому счёту: got %s, want %s", withdrawal.UserID, userID)
	}

	if withdrawal.OrderNumber != number {
		t.Errorf("хранилище получило неожиданный номер: got %s, want %s", withdrawal.OrderNumber, number)
	}

	if got := withdrawal.Sum.String(); got != "751.5" {
		t.Errorf("хранилище получило неожиданную сумму: got %s, want 751.5", got)
	}

	// Остаток не читается: решение о допустимости принимает условное
	// изменение счёта на стороне базы данных.
	if repo.balanceCalls != 0 {
		t.Errorf("сервис прочитал счёт перед списанием: %d обращений", repo.balanceCalls)
	}
}

// TestBalancesWithdrawAcceptsRepeat закрепляет сценарий «Повторное списание с
// тем же номером заказа»: повтор не является отказом.
func TestBalancesWithdrawAcceptsRepeat(t *testing.T) {
	repo := &balanceRepositoryStub{created: false}

	err := service.NewBalances(repo).Withdraw(t.Context(),
		newOrderNumber(t), newMoney(t, "100"), domain.NewUserID())
	if err != nil {
		t.Errorf("повтор списания признан отказом: %v", err)
	}
}

// TestBalancesWithdrawPropagatesInsufficientFunds закрепляет сценарий
// «Недостаточно баллов»: доменная ошибка хранилища доходит до вызывающей
// стороны распознаваемой.
func TestBalancesWithdrawPropagatesInsufficientFunds(t *testing.T) {
	repo := &balanceRepositoryStub{withdrawErr: domain.ErrInsufficientFunds}

	err := service.NewBalances(repo).Withdraw(t.Context(),
		newOrderNumber(t), newMoney(t, "100"), domain.NewUserID())

	if !errors.Is(err, domain.ErrInsufficientFunds) {
		t.Errorf("ожидалась ошибка недостатка баллов, получено: %v", err)
	}
}

// TestBalancesWithdrawPropagatesRepositoryFailure закрепляет, что внутренняя
// ошибка хранилища не подменяется доменной.
func TestBalancesWithdrawPropagatesRepositoryFailure(t *testing.T) {
	repo := &balanceRepositoryStub{withdrawErr: errRepository}

	err := service.NewBalances(repo).Withdraw(t.Context(),
		newOrderNumber(t), newMoney(t, "100"), domain.NewUserID())

	switch {
	case !errors.Is(err, errRepository):
		t.Errorf("ошибка хранилища подменена: %v", err)
	case errors.Is(err, domain.ErrInsufficientFunds):
		t.Error("сбой хранилища выдан за недостаток баллов")
	}
}

// TestBalancesListsWithdrawals закрепляет требование «История списаний»:
// сервис возвращает списания запрошенного пользователя.
func TestBalancesListsWithdrawals(t *testing.T) {
	userID := domain.NewUserID()
	repo := &balanceRepositoryStub{
		withdrawals: []domain.Withdrawal{
			{
				UserID:      userID,
				OrderNumber: newOrderNumber(t),
				Sum:         newMoney(t, "500"),
				ProcessedAt: time.Now().UTC(),
			},
		},
	}

	withdrawals, err := service.NewBalances(repo).Withdrawals(t.Context(), userID)
	if err != nil {
		t.Fatalf("чтение истории списаний: %v", err)
	}

	if len(withdrawals) != 1 {
		t.Fatalf("неожиданное число списаний: got %d, want 1", len(withdrawals))
	}

	if repo.listedFor != userID {
		t.Errorf("история прочитана для чужого пользователя: got %s, want %s", repo.listedFor, userID)
	}
}

// TestBalancesListsEmptyWithdrawals закрепляет сценарий «У пользователя нет
// списаний»: пустая история отличима от отказа по значению ошибки.
func TestBalancesListsEmptyWithdrawals(t *testing.T) {
	repo := &balanceRepositoryStub{}

	withdrawals, err := service.NewBalances(repo).Withdrawals(t.Context(), domain.NewUserID())
	if err != nil {
		t.Fatalf("пустая история признана отказом: %v", err)
	}

	if len(withdrawals) != 0 {
		t.Errorf("неожиданные списания: %d", len(withdrawals))
	}
}

// TestBalancesPropagatesRepositoryFailureOnList закрепляет, что сбой чтения
// истории не выглядит как пустая история.
func TestBalancesPropagatesRepositoryFailureOnList(t *testing.T) {
	repo := &balanceRepositoryStub{listErr: errRepository}

	withdrawals, err := service.NewBalances(repo).Withdrawals(t.Context(), domain.NewUserID())
	if !errors.Is(err, errRepository) {
		t.Fatalf("ошибка хранилища подменена: %v", err)
	}

	if withdrawals != nil {
		t.Error("при ошибке возвращён непустой результат")
	}
}
