package testutil

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	migratepgx "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib" // драйвер database/sql, необходимый golang-migrate.
)

// EnvDatabaseURI — переменная окружения со строкой подключения к PostgreSQL,
const EnvDatabaseURI = "TEST_DATABASE_URI"

// operationTimeout ограничивает время административных операций с базой.
const operationTimeout = 30 * time.Second

// RequirePostgres возвращает административную строку подключения из окружения.
func RequirePostgres(t *testing.T) string {
	t.Helper()

	dsn := strings.TrimSpace(os.Getenv(EnvDatabaseURI))
	if dsn == "" {
		t.Skipf("переменная %s не задана: интеграционный тест пропущен", EnvDatabaseURI)
	}

	return dsn
}

// NewDatabase создаёт пустую базу данных со случайным именем и возвращает
func NewDatabase(t *testing.T) string {
	t.Helper()

	adminDSN := RequirePostgres(t)
	name := databaseName(t)

	execAdmin(t, adminDSN, fmt.Sprintf("CREATE DATABASE %s", quoteIdentifier(name)))

	t.Cleanup(func() {
		execAdmin(t, adminDSN, fmt.Sprintf("DROP DATABASE IF EXISTS %s WITH (FORCE)", quoteIdentifier(name)))
	})

	return replaceDatabase(t, adminDSN, name)
}

// Connect открывает подключение к указанной базе и закрывает его после теста.
func Connect(t *testing.T, dsn string) *pgx.Conn {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), operationTimeout)
	defer cancel()

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("подключение к базе данных: %v", err)
	}

	t.Cleanup(func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), operationTimeout)
		defer closeCancel()

		if closeErr := conn.Close(closeCtx); closeErr != nil {
			t.Logf("закрытие подключения: %v", closeErr)
		}
	})

	return conn
}

// TableExists сообщает, существует ли таблица в схеме public указанной базы.
func TableExists(t *testing.T, dsn, table string) bool {
	t.Helper()

	conn := Connect(t, dsn)

	ctx, cancel := context.WithTimeout(t.Context(), operationTimeout)
	defer cancel()

	var exists bool

	query := `SELECT EXISTS (
		SELECT 1 FROM information_schema.tables
		WHERE table_schema = 'public' AND table_name = $1
	)`

	if err := conn.QueryRow(ctx, query, table).Scan(&exists); err != nil {
		t.Fatalf("проверка существования таблицы %s: %v", table, err)
	}

	return exists
}

// IndexDefinition возвращает определение индекса схемы public в виде команды
func IndexDefinition(t *testing.T, dsn, index string) string {
	t.Helper()

	conn := Connect(t, dsn)

	ctx, cancel := context.WithTimeout(t.Context(), operationTimeout)
	defer cancel()

	var definition string

	query := `SELECT indexdef FROM pg_indexes WHERE schemaname = 'public' AND indexname = $1`

	err := conn.QueryRow(ctx, query, index).Scan(&definition)

	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return ""
	case err != nil:
		t.Fatalf("чтение определения индекса %s: %v", index, err)
	}

	return definition
}

// SchemaVersion возвращает версию схемы и признак повреждения, записанные
func SchemaVersion(t *testing.T, dsn string) (int64, bool) {
	t.Helper()

	conn := Connect(t, dsn)

	ctx, cancel := context.WithTimeout(t.Context(), operationTimeout)
	defer cancel()

	var (
		version int64
		dirty   bool
	)

	if err := conn.QueryRow(ctx, "SELECT version, dirty FROM schema_migrations").Scan(&version, &dirty); err != nil {
		t.Fatalf("чтение версии схемы: %v", err)
	}

	return version, dirty
}

// execAdmin выполняет административную команду в базе, заданной adminDSN.
func execAdmin(t *testing.T, adminDSN, statement string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()

	conn, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		t.Fatalf("подключение к административной базе: %v", err)
	}

	defer func() {
		if closeErr := conn.Close(ctx); closeErr != nil {
			t.Logf("закрытие административного подключения: %v", closeErr)
		}
	}()

	if _, err = conn.Exec(ctx, statement); err != nil {
		t.Fatalf("выполнение %q: %v", statement, err)
	}
}

// replaceDatabase подставляет в строку подключения другое имя базы.
func replaceDatabase(t *testing.T, dsn, name string) string {
	t.Helper()

	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("разбор строки подключения: %v", err)
	}

	parsed.Path = "/" + name

	return parsed.String()
}

// databaseName формирует уникальное имя базы для теста.
func databaseName(t *testing.T) string {
	t.Helper()

	sanitized := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		default:
			return '_'
		}
	}, t.Name())

	const maxNameLen = 40
	if len(sanitized) > maxNameLen {
		sanitized = sanitized[:maxNameLen]
	}

	return fmt.Sprintf("test_%s_%d", sanitized, time.Now().UnixNano())
}

// quoteIdentifier экранирует идентификатор для подстановки в DDL.
func quoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// Rollback откатывает все применённые миграции из fsys на базе dsn.
func Rollback(t *testing.T, dsn string, fsys fs.FS) {
	t.Helper()

	src, err := iofs.New(fsys, ".")
	if err != nil {
		t.Fatalf("чтение каталога миграций: %v", err)
	}

	db, err := sql.Open("pgx/v5", dsn)
	if err != nil {
		t.Fatalf("подключение к базе данных для отката: %v", err)
	}

	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Logf("закрытие подключения отката: %v", closeErr)
		}
	}()

	driver, err := migratepgx.WithInstance(db, &migratepgx.Config{})
	if err != nil {
		t.Fatalf("инициализация драйвера миграций: %v", err)
	}

	migrator, err := migrate.NewWithInstance("iofs", src, "pgx5", driver)
	if err != nil {
		t.Fatalf("инициализация механизма миграций: %v", err)
	}

	defer func() {
		if sourceErr, dbErr := migrator.Close(); sourceErr != nil || dbErr != nil {
			t.Logf("закрытие механизма миграций: %v, %v", sourceErr, dbErr)
		}
	}()

	if err = migrator.Down(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		t.Fatalf("откат миграций: %v", err)
	}
}
