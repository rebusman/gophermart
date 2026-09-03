package integration_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"gophermart/internal/domain"
	"gophermart/internal/storage/postgres"
	"gophermart/migrations"
	"gophermart/tests/testutil"
)

// newUserRepository поднимает пустую базу, применяет к ней миграции и
// возвращает репозиторий пользователей поверх неё вместе с пулом, который
// закрывается по завершении теста.
func newUserRepository(t *testing.T) (*postgres.UserRepository, *pgxpool.Pool) {
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

	return postgres.NewUserRepository(pool), pool
}

// newTestUser собирает учётную запись с логином "gopher", готовую к передаче в CreateUser.
func newTestUser() domain.User {
	return domain.User{
		ID:           domain.NewUserID(),
		Login:        "gopher",
		PasswordHash: "хеш",
		CreatedAt:    time.Now().UTC(),
	}
}

// TestUserRepositoryCreatesUserWithZeroBalance закрепляет сценарий требования
// «Счёт создан вместе с пользователем»: после успешной регистрации у
// пользователя существует счёт с нулевыми суммами.
func TestUserRepositoryCreatesUserWithZeroBalance(t *testing.T) {
	repo, pool := newUserRepository(t)
	user := newTestUser()

	if err := repo.CreateUser(t.Context(), user); err != nil {
		t.Fatalf("создание пользователя: %v", err)
	}

	var (
		current   decimal.Decimal
		withdrawn decimal.Decimal
	)

	query := `SELECT current, withdrawn_total FROM balances WHERE user_id = $1`

	err := pool.QueryRow(t.Context(), query, postgres.UUIDFromGoogle(uuid.UUID(user.ID))).Scan(&current, &withdrawn)
	if err != nil {
		t.Fatalf("чтение счёта лояльности: %v", err)
	}

	if !current.IsZero() {
		t.Errorf("текущий баланс не нулевой: %s", current)
	}

	if !withdrawn.IsZero() {
		t.Errorf("сумма списаний не нулевая: %s", withdrawn)
	}
}

// TestUserRepositoryReportsLoginTakenOnConflict закрепляет распознавание
// конфликта логина по ограничению users_login_unique, а не по обобщённой
// ошибке.
func TestUserRepositoryReportsLoginTakenOnConflict(t *testing.T) {
	repo, _ := newUserRepository(t)

	if err := repo.CreateUser(t.Context(), newTestUser()); err != nil {
		t.Fatalf("первая регистрация: %v", err)
	}

	err := repo.CreateUser(t.Context(), newTestUser())
	if !errors.Is(err, domain.ErrLoginTaken) {
		t.Errorf("занятый логин не распознан как ErrLoginTaken: %v", err)
	}
}

// TestUserRepositoryFindsUserByLoginAndID закрепляет поиск учётной записи по
// логину и по идентификатору, включая случай отсутствия записи.
func TestUserRepositoryFindsUserByLoginAndID(t *testing.T) {
	repo, _ := newUserRepository(t)
	user := newTestUser()

	if err := repo.CreateUser(t.Context(), user); err != nil {
		t.Fatalf("создание пользователя: %v", err)
	}

	byLogin, err := repo.UserByLogin(t.Context(), user.Login)
	if err != nil {
		t.Fatalf("поиск по логину: %v", err)
	}

	if byLogin.ID != user.ID || byLogin.PasswordHash != user.PasswordHash {
		t.Errorf("поиск по логину вернул другую запись: %+v", byLogin)
	}

	byID, err := repo.UserByID(t.Context(), user.ID)
	if err != nil {
		t.Fatalf("поиск по идентификатору: %v", err)
	}

	if byID.Login != user.Login {
		t.Errorf("поиск по идентификатору вернул другую запись: %+v", byID)
	}

	if _, err = repo.UserByLogin(t.Context(), "нет-такого-логина"); !errors.Is(err, domain.ErrUserNotFound) {
		t.Errorf("отсутствующий логин дал не ту ошибку: %v", err)
	}

	if _, err = repo.UserByID(t.Context(), domain.NewUserID()); !errors.Is(err, domain.ErrUserNotFound) {
		t.Errorf("отсутствующий идентификатор дал не ту ошибку: %v", err)
	}
}

// TestUserRepositoryRollsBackOnBalanceCreationFailure закрепляет требование
// «Сбой при создании счёта»: искусственный сбой второго шага транзакции —
// удалённая таблица balances — не должен оставлять учётную запись в базе, а
// логин обязан остаться свободным для повторной регистрации.
func TestUserRepositoryRollsBackOnBalanceCreationFailure(t *testing.T) {
	dsn := testutil.NewDatabase(t)

	if err := postgres.Migrate(t.Context(), dsn, migrations.FS); err != nil {
		t.Fatalf("применение миграций: %v", err)
	}

	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("создание пула подключений: %v", err)
	}

	t.Cleanup(pool.Close)

	// Второй шаг транзакции — вставка в balances — искусственно ломается
	// удалением таблицы: вставка в users на первом шаге при этом успешна.
	if _, err = pool.Exec(t.Context(), "DROP TABLE balances"); err != nil {
		t.Fatalf("удаление таблицы balances: %v", err)
	}

	repo := postgres.NewUserRepository(pool)
	user := newTestUser()

	if err = repo.CreateUser(t.Context(), user); err == nil {
		t.Fatal("ожидался сбой создания счёта лояльности")
	}

	var usersCount int
	if err = pool.QueryRow(t.Context(), "SELECT count(*) FROM users").Scan(&usersCount); err != nil {
		t.Fatalf("подсчёт учётных записей: %v", err)
	}

	if usersCount != 0 {
		t.Errorf("учётная запись не откатилась после сбоя: найдено %d записей", usersCount)
	}

	if _, err = repo.UserByLogin(t.Context(), user.Login); !errors.Is(err, domain.ErrUserNotFound) {
		t.Errorf("логин не освободился после сбоя: %v", err)
	}
}
