package integration_test

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"gophermart/internal/storage/postgres"
	"gophermart/migrations"
	"gophermart/tests/testutil"
)

// schemaVersionLatest — версия последней миграции схемы.
//
// Значение обновляется вместе с добавлением каждой новой пары файлов
// миграции: тесты сверяют с ним состояние схемы после применения всех
// миграций.
const schemaVersionLatest = 4

func TestMigrationCreatesSchemaTables(t *testing.T) {
	dsn := testutil.NewDatabase(t)

	applied, err := postgres.Migrate(t.Context(), dsn, migrations.FS)
	if err != nil {
		t.Fatalf("применение миграций: %v", err)
	}

	if applied != schemaVersionLatest {
		t.Errorf("возвращённая версия схемы: got %d, want %d", applied, schemaVersionLatest)
	}

	for _, table := range []string{"users", "balances", "orders", "withdrawals"} {
		if !testutil.TableExists(t, dsn, table) {
			t.Errorf("таблица %s не создана", table)
		}
	}

	if version, dirty := testutil.SchemaVersion(t, dsn); version != schemaVersionLatest || dirty {
		t.Errorf("неожиданное состояние схемы: версия %d, повреждена %t", version, dirty)
	}
}

func TestMigrationIsIdempotentOnActualSchema(t *testing.T) {
	dsn := testutil.NewDatabase(t)

	if _, err := postgres.Migrate(t.Context(), dsn, migrations.FS); err != nil {
		t.Fatalf("первое применение миграций: %v", err)
	}

	if _, err := postgres.Migrate(t.Context(), dsn, migrations.FS); err != nil {
		t.Fatalf("повторное применение миграций: %v", err)
	}

	if version, dirty := testutil.SchemaVersion(t, dsn); version != schemaVersionLatest || dirty {
		t.Errorf("повторный запуск изменил схему: версия %d, повреждена %t", version, dirty)
	}
}

func TestMigrationIsReversible(t *testing.T) {
	dsn := testutil.NewDatabase(t)

	if _, err := postgres.Migrate(t.Context(), dsn, migrations.FS); err != nil {
		t.Fatalf("первое применение миграций: %v", err)
	}

	testutil.Rollback(t, dsn, migrations.FS)

	for _, table := range []string{"users", "balances", "orders", "withdrawals"} {
		if testutil.TableExists(t, dsn, table) {
			t.Errorf("таблица %s не удалена обратной миграцией", table)
		}
	}

	if _, err := postgres.Migrate(t.Context(), dsn, migrations.FS); err != nil {
		t.Fatalf("повторное применение миграций после отката: %v", err)
	}

	for _, table := range []string{"users", "balances", "orders", "withdrawals"} {
		if !testutil.TableExists(t, dsn, table) {
			t.Errorf("таблица %s не создана после повторного применения", table)
		}
	}
}

func TestBalancesRejectNegativeAmounts(t *testing.T) {
	dsn := testutil.NewDatabase(t)

	if _, err := postgres.Migrate(t.Context(), dsn, migrations.FS); err != nil {
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

	if _, err := postgres.Migrate(t.Context(), dsn, migrations.FS); err != nil {
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

func TestMigrationCreatesOrdersListIndex(t *testing.T) {
	dsn := testutil.NewDatabase(t)

	if _, err := postgres.Migrate(t.Context(), dsn, migrations.FS); err != nil {
		t.Fatalf("применение миграций: %v", err)
	}

	definition := testutil.IndexDefinition(t, dsn, "orders_user_uploaded_at_idx")
	if definition == "" {
		t.Fatal("индекс под выдачу списка заказов не создан")
	}

	// Состав и порядок сортировки существенны: индекс должен покрывать
	// ORDER BY uploaded_at DESC, number DESC внутри одного пользователя
	// целиком, иначе группы строк с совпадающим временем досортировываются.
	if !strings.Contains(definition, "(user_id, uploaded_at DESC, number DESC)") {
		t.Errorf("неожиданное определение индекса: %s", definition)
	}
}

func TestOrdersRejectUnknownStatus(t *testing.T) {
	dsn := testutil.NewDatabase(t)

	if _, err := postgres.Migrate(t.Context(), dsn, migrations.FS); err != nil {
		t.Fatalf("применение миграций: %v", err)
	}

	conn := testutil.Connect(t, dsn)
	userID := uuid.New()

	insertUser := `INSERT INTO users (id, login, password_hash) VALUES ($1, $2, $3)`
	if _, err := conn.Exec(t.Context(), insertUser, userID, "gopher", "хеш"); err != nil {
		t.Fatalf("создание пользователя: %v", err)
	}

	insertOrder := `INSERT INTO orders (number, user_id, status) VALUES ($1, $2, $3)`

	for _, status := range []string{"NEW", "PROCESSING", "INVALID", "PROCESSED"} {
		if _, err := conn.Exec(t.Context(), insertOrder, "номер-"+status, userID, status); err != nil {
			t.Errorf("статус %s отвергнут базой: %v", status, err)
		}
	}

	_, err := conn.Exec(t.Context(), insertOrder, "12345678903", userID, "UNKNOWN")
	if err == nil {
		t.Fatal("ожидался отказ ограничения словаря статусов")
	}

	if !strings.Contains(err.Error(), "orders_status_known") {
		t.Errorf("отказ вызван не ограничением словаря статусов: %v", err)
	}
}

func TestWithdrawalsUniquePerUserAndOrder(t *testing.T) {
	dsn := testutil.NewDatabase(t)

	if _, err := postgres.Migrate(t.Context(), dsn, migrations.FS); err != nil {
		t.Fatalf("применение миграций: %v", err)
	}

	conn := testutil.Connect(t, dsn)
	owner := uuid.New()
	stranger := uuid.New()

	insertUser := `INSERT INTO users (id, login, password_hash) VALUES ($1, $2, $3)`
	for login, id := range map[string]uuid.UUID{"gopher": owner, "stranger": stranger} {
		if _, err := conn.Exec(t.Context(), insertUser, id, login, "хеш"); err != nil {
			t.Fatalf("создание пользователя %s: %v", login, err)
		}
	}

	insertWithdrawal := `INSERT INTO withdrawals (user_id, order_number, sum) VALUES ($1, $2, $3)`
	if _, err := conn.Exec(t.Context(), insertWithdrawal, owner, orderNumberSecond, "100"); err != nil {
		t.Fatalf("создание списания: %v", err)
	}

	_, err := conn.Exec(t.Context(), insertWithdrawal, owner, orderNumberSecond, "50")
	if err == nil {
		t.Error("второе списание по тому же номеру принято, первичный ключ не сработал")
	} else if !strings.Contains(err.Error(), "withdrawals_pkey") {
		t.Errorf("отказ вызван не первичным ключом: %v", err)
	}

	// Уникальность действует в пределах пользователя: тот же номер у другого
	// пользователя — законное списание, а не повтор.
	if _, err = conn.Exec(t.Context(), insertWithdrawal, stranger, orderNumberSecond, "50"); err != nil {
		t.Errorf("списание другого пользователя по тому же номеру отвергнуто: %v", err)
	}
}

func TestWithdrawalsRejectNonPositiveSum(t *testing.T) {
	dsn := testutil.NewDatabase(t)

	if _, err := postgres.Migrate(t.Context(), dsn, migrations.FS); err != nil {
		t.Fatalf("применение миграций: %v", err)
	}

	conn := testutil.Connect(t, dsn)
	userID := uuid.New()

	insertUser := `INSERT INTO users (id, login, password_hash) VALUES ($1, $2, $3)`
	if _, err := conn.Exec(t.Context(), insertUser, userID, "gopher", "хеш"); err != nil {
		t.Fatalf("создание пользователя: %v", err)
	}

	insertWithdrawal := `INSERT INTO withdrawals (user_id, order_number, sum) VALUES ($1, $2, $3)`

	tests := []struct {
		name string
		sum  string
	}{
		{name: "нулевая сумма", sum: "0"},
		{name: "отрицательная сумма", sum: "-1"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := conn.Exec(t.Context(), insertWithdrawal, userID, orderNumberSecond, test.sum)
			if err == nil {
				t.Fatal("ожидался отказ ограничения положительности суммы")
			}

			if !strings.Contains(err.Error(), "withdrawals_sum_positive") {
				t.Errorf("отказ вызван не ограничением положительности суммы: %v", err)
			}
		})
	}
}

func TestMigrationCreatesWithdrawalsHistoryIndex(t *testing.T) {
	dsn := testutil.NewDatabase(t)

	if _, err := postgres.Migrate(t.Context(), dsn, migrations.FS); err != nil {
		t.Fatalf("применение миграций: %v", err)
	}

	definition := testutil.IndexDefinition(t, dsn, "withdrawals_user_processed_at_idx")
	if definition == "" {
		t.Fatal("индекс под выдачу истории списаний не создан")
	}

	// Первичный ключ этот индекс не заменяет: он упорядочен по номеру заказа,
	// а история читается по времени списания от новых к старым.
	if !strings.Contains(definition, "(user_id, processed_at DESC, order_number DESC)") {
		t.Errorf("неожиданное определение индекса: %s", definition)
	}
}

func TestMigrationAddsAccrualSchedulingColumns(t *testing.T) {
	dsn := testutil.NewDatabase(t)

	if _, err := postgres.Migrate(t.Context(), dsn, migrations.FS); err != nil {
		t.Fatalf("применение миграций: %v", err)
	}

	conn := testutil.Connect(t, dsn)
	userID := uuid.New()

	insertUser := `INSERT INTO users (id, login, password_hash) VALUES ($1, $2, $3)`
	if _, err := conn.Exec(t.Context(), insertUser, userID, "gopher", "хеш"); err != nil {
		t.Fatalf("создание пользователя: %v", err)
	}

	insertOrder := `INSERT INTO orders (number, user_id, status) VALUES ($1, $2, 'NEW')`
	if _, err := conn.Exec(t.Context(), insertOrder, orderNumberFirst, userID); err != nil {
		t.Fatalf("создание заказа: %v", err)
	}

	// Значения по умолчанию делают заказ доступным для проверки немедленно:
	// отдельного шага заполнения после миграции не требуется.
	var (
		attempts int
		due      bool
	)

	query := `SELECT attempts, next_attempt_at <= now() FROM orders WHERE number = $1`
	if err := conn.QueryRow(t.Context(), query, orderNumberFirst).Scan(&attempts, &due); err != nil {
		t.Fatalf("чтение состояния планировщика: %v", err)
	}

	if attempts != 0 {
		t.Errorf("неожиданное число попыток у нового заказа: got %d, want 0", attempts)
	}

	if !due {
		t.Error("новый заказ не доступен для проверки немедленно")
	}
}

func TestOrdersRejectNegativeAttempts(t *testing.T) {
	dsn := testutil.NewDatabase(t)

	if _, err := postgres.Migrate(t.Context(), dsn, migrations.FS); err != nil {
		t.Fatalf("применение миграций: %v", err)
	}

	conn := testutil.Connect(t, dsn)
	userID := uuid.New()

	insertUser := `INSERT INTO users (id, login, password_hash) VALUES ($1, $2, $3)`
	if _, err := conn.Exec(t.Context(), insertUser, userID, "gopher", "хеш"); err != nil {
		t.Fatalf("создание пользователя: %v", err)
	}

	insertOrder := `INSERT INTO orders (number, user_id, status, attempts) VALUES ($1, $2, 'NEW', -1)`

	_, err := conn.Exec(t.Context(), insertOrder, orderNumberFirst, userID)
	if err == nil {
		t.Fatal("ожидался отказ ограничения неотрицательности числа попыток")
	}

	if !strings.Contains(err.Error(), "orders_attempts_nonnegative") {
		t.Errorf("отказ вызван не ограничением неотрицательности: %v", err)
	}
}

func TestMigrationCreatesPendingOrdersIndex(t *testing.T) {
	dsn := testutil.NewDatabase(t)

	if _, err := postgres.Migrate(t.Context(), dsn, migrations.FS); err != nil {
		t.Fatalf("применение миграций: %v", err)
	}

	definition := testutil.IndexDefinition(t, dsn, "orders_pending_next_attempt_idx")
	if definition == "" {
		t.Fatal("индекс под выборку заданий не создан")
	}

	// Индекс обязан быть частичным: заказы в окончательных состояниях в
	// выборку не попадают никогда и со временем составляют большинство строк.
	if !strings.Contains(definition, "WHERE") || !strings.Contains(definition, "status") {
		t.Errorf("индекс не является частичным по статусу: %s", definition)
	}

	if !strings.Contains(definition, "(next_attempt_at)") {
		t.Errorf("неожиданный состав индекса: %s", definition)
	}
}

func TestMigrationDownRemovesSchedulingColumns(t *testing.T) {
	dsn := testutil.NewDatabase(t)

	if _, err := postgres.Migrate(t.Context(), dsn, migrations.FS); err != nil {
		t.Fatalf("применение миграций: %v", err)
	}

	testutil.Rollback(t, dsn, migrations.FS)

	if _, err := postgres.Migrate(t.Context(), dsn, migrations.FS); err != nil {
		t.Fatalf("повторное применение миграций после отката: %v", err)
	}

	// Колонка accrual принадлежит миграции 000002 и обратной миграцией 000004
	// затрагиваться не должна.
	conn := testutil.Connect(t, dsn)

	query := `SELECT count(*) FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'orders' AND column_name = $1`

	for _, column := range []string{"attempts", "next_attempt_at", "accrual"} {
		var count int
		if err := conn.QueryRow(t.Context(), query, column).Scan(&count); err != nil {
			t.Fatalf("чтение сведений о колонке %s: %v", column, err)
		}

		if count != 1 {
			t.Errorf("колонка %s отсутствует после повторного применения миграций", column)
		}
	}
}
