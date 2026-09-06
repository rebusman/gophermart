package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"

	"github.com/golang-migrate/migrate/v4"
	migratepgx "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	migratesource "github.com/golang-migrate/migrate/v4/source"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/jackc/pgx/v5/stdlib" // драйвер database/sql, необходимый golang-migrate.
)

// Имена драйверов, под которыми источник и приёмник миграций регистрируются в
const (
	migrationSourceName   = "iofs"
	migrationDatabaseName = "pgx5"
)

// sqlDriverName — имя драйвера database/sql, регистрируемое пакетом
const sqlDriverName = "pgx/v5"

// ErrDirtySchema возвращается, когда предыдущее применение миграций
var ErrDirtySchema = errors.New("схема базы данных помечена как повреждённая")

// ErrNoMigrations возвращается, когда набор миграций не содержит ни одной
var ErrNoMigrations = errors.New("набор миграций не содержит ни одной миграции")

// Migrate приводит схему базы данных в актуальное состояние, применяя все
func Migrate(ctx context.Context, dsn string, fsys fs.FS) (uint, error) {
	src, err := iofs.New(fsys, ".")
	if err != nil {
		return 0, fmt.Errorf("чтение каталога миграций: %w", err)
	}

	empty, err := isEmptySource(src)
	if err != nil {
		if closeErr := src.Close(); closeErr != nil {
			return 0, errors.Join(err, fmt.Errorf("закрытие источника миграций: %w", closeErr))
		}

		return 0, err
	}

	if empty {
		if closeErr := src.Close(); closeErr != nil {
			return 0, errors.Join(ErrNoMigrations, fmt.Errorf("закрытие источника миграций: %w", closeErr))
		}

		return 0, ErrNoMigrations
	}

	return applyMigrations(ctx, dsn, src)
}

// applyMigrations открывает подключение к базе данных, применяет миграции из
func applyMigrations(ctx context.Context, dsn string, src migratesource.Driver) (uint, error) {
	db, err := sql.Open(sqlDriverName, dsn)
	if err != nil {
		return 0, fmt.Errorf("подключение к базе данных для миграций: %w", err)
	}

	defer func() {
		_ = db.Close()
	}()

	if err = db.PingContext(ctx); err != nil {
		return 0, fmt.Errorf("проверка доступности базы данных перед миграциями: %w", err)
	}

	driver, err := migratepgx.WithInstance(db, &migratepgx.Config{})
	if err != nil {
		return 0, fmt.Errorf("инициализация драйвера миграций: %w", err)
	}

	migrator, err := migrate.NewWithInstance(migrationSourceName, src, migrationDatabaseName, driver)
	if err != nil {
		return 0, fmt.Errorf("инициализация механизма миграций: %w", err)
	}

	defer func() {
		_, _ = migrator.Close()
	}()

	if err = ensureNotDirty(migrator); err != nil {
		return 0, err
	}

	if err = migrator.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return 0, fmt.Errorf("применение миграций: %w", err)
	}

	version, _, err := migrator.Version()
	if err != nil {
		return 0, fmt.Errorf("чтение версии схемы после применения: %w", err)
	}

	return version, nil
}

// ensureNotDirty проверяет, что предыдущее применение миграций завершилось
func ensureNotDirty(migrator *migrate.Migrate) error {
	version, dirty, err := migrator.Version()

	switch {
	case errors.Is(err, migrate.ErrNilVersion):
		return nil
	case err != nil:
		return fmt.Errorf("чтение текущей версии схемы: %w", err)
	case dirty:
		return fmt.Errorf("%w: версия %d", ErrDirtySchema, version)
	default:
		return nil
	}
}

// isEmptySource сообщает, что каталог миграций не содержит ни одного файла,
func isEmptySource(src migratesource.Driver) (bool, error) {
	_, err := src.First()

	switch {
	case err == nil:
		return false, nil
	case errors.Is(err, fs.ErrNotExist):
		return true, nil
	default:
		return false, fmt.Errorf("чтение списка миграций: %w", err)
	}
}
