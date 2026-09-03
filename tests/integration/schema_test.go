package integration_test

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"gophermart/internal/storage/postgres"
	"gophermart/migrations"
	"gophermart/tests/testutil"
)

// schemaVersionUsersBalances — версия миграции, создающей users и balances.
const schemaVersionUsersBalances = 1

func TestMigrationCreatesUsersAndBalances(t *testing.T) {
	dsn := testutil.NewDatabase(t)

	if err := postgres.Migrate(t.Context(), dsn, migrations.FS); err != nil {
		t.Fatalf("применение миграций: %v", err)
	}

	for _, table := range []string{"users", "balances"} {
		if !testutil.TableExists(t, dsn, table) {
			t.Errorf("таблица %s не создана", table)
		}
	}

	if version, dirty := testutil.SchemaVersion(t, dsn); version != schemaVersionUsersBalances || dirty {
		t.Errorf("неожиданное состояние схемы: версия %d, повреждена %t", version, dirty)
	}
}

func TestMigrationIsIdempotentOnActualSchema(t *testing.T) {
	dsn := testutil.NewDatabase(t)

	if err := postgres.Migrate(t.Context(), dsn, migrations.FS); err != nil {
		t.Fatalf("первое применение миграций: %v", err)
	}

	if err := postgres.Migrate(t.Context(), dsn, migrations.FS); err != nil {
		t.Fatalf("повторное применение миграций: %v", err)
	}

	if version, dirty := testutil.SchemaVersion(t, dsn); version != schemaVersionUsersBalances || dirty {
		t.Errorf("повторный запуск изменил схему: версия %d, повреждена %t", version, dirty)
	}
}

func TestMigrationIsReversible(t *testing.T) {
	dsn := testutil.NewDatabase(t)

	if err := postgres.Migrate(t.Context(), dsn, migrations.FS); err != nil {
		t.Fatalf("первое применение миграций: %v", err)
	}

	testutil.Rollback(t, dsn, migrations.FS)

	for _, table := range []string{"users", "balances"} {
		if testutil.TableExists(t, dsn, table) {
			t.Errorf("таблица %s не удалена обратной миграцией", table)
		}
	}

	if err := postgres.Migrate(t.Context(), dsn, migrations.FS); err != nil {
		t.Fatalf("повторное применение миграций после отката: %v", err)
	}

	for _, table := range []string{"users", "balances"} {
		if !testutil.TableExists(t, dsn, table) {
			t.Errorf("таблица %s не создана после повторного применения", table)
		}
	}
}

func TestBalancesRejectNegativeAmounts(t *testing.T) {
	dsn := testutil.NewDatabase(t)

	if err := postgres.Migrate(t.Context(), dsn, migrations.FS); err != nil {
		t.Fatalf("применение миграций: %v", err)
	}

	conn := testutil.Connect(t, dsn)
	userID := uuid.New()

	insertUser := `INSERT INTO users (id, login, password_hash) VALUES ($1, $2, $3)`
	if _, err := conn.Exec(t.Context(), insertUser, userID, "gopher", "хеш"); err != nil {
		t.Fatalf("создание пользователя: %v", err)
	}

	tests := []struct {
		name      string
		current   string
		withdrawn string
	}{
		{name: "отрицательный текущий баланс", current: "-1", withdrawn: "0"},
		{name: "отрицательная сумма списаний", current: "0", withdrawn: "-1"},
	}

	insertBalance := `INSERT INTO balances (user_id, current, withdrawn_total) VALUES ($1, $2, $3)`

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := conn.Exec(t.Context(), insertBalance, userID, test.current, test.withdrawn)
			if err == nil {
				t.Fatal("ожидался отказ ограничения неотрицательности")
			}

			if !strings.Contains(err.Error(), "nonnegative") {
				t.Errorf("отказ вызван не ограничением неотрицательности: %v", err)
			}
		})
	}
}

func TestUsersRejectDuplicateAndEmptyLogin(t *testing.T) {
	dsn := testutil.NewDatabase(t)

	if err := postgres.Migrate(t.Context(), dsn, migrations.FS); err != nil {
		t.Fatalf("применение миграций: %v", err)
	}

	conn := testutil.Connect(t, dsn)
	insertUser := `INSERT INTO users (id, login, password_hash) VALUES ($1, $2, $3)`

	if _, err := conn.Exec(t.Context(), insertUser, uuid.New(), "gopher", "хеш"); err != nil {
		t.Fatalf("создание пользователя: %v", err)
	}

	_, err := conn.Exec(t.Context(), insertUser, uuid.New(), "gopher", "хеш")
	if err == nil {
		t.Error("повторный логин принят, ограничение уникальности не сработало")
	} else if !strings.Contains(err.Error(), "users_login_unique") {
		t.Errorf("отказ вызван не ограничением уникальности логина: %v", err)
	}

	_, err = conn.Exec(t.Context(), insertUser, uuid.New(), "   ", "хеш")
	if err == nil {
		t.Error("пустой логин принят, ограничение непустоты не сработало")
	} else if !strings.Contains(err.Error(), "users_login_not_empty") {
		t.Errorf("отказ вызван не ограничением непустоты логина: %v", err)
	}
}
