package integration_test

import (
	"errors"
	"testing"
	"testing/fstest"
	"time"

	"gophermart/internal/storage/postgres"
	"gophermart/migrations"
	"gophermart/tests/testutil"
)

// connectTimeout ограничивает время подключения к базе в тестах.
const connectTimeout = 10 * time.Second

// fixtureV1 создаёт таблицу первой версии.
func fixtureV1() fstest.MapFS {
	return fstest.MapFS{
		"000001_widgets.up.sql":   {Data: []byte(`CREATE TABLE widgets (id INTEGER PRIMARY KEY);`)},
		"000001_widgets.down.sql": {Data: []byte(`DROP TABLE widgets;`)},
	}
}

// fixtureV1V2 добавляет к первой версии вторую.
func fixtureV1V2() fstest.MapFS {
	fixture := fixtureV1()
	fixture["000002_gadgets.up.sql"] = &fstest.MapFile{Data: []byte(`CREATE TABLE gadgets (id INTEGER PRIMARY KEY);`)}
	fixture["000002_gadgets.down.sql"] = &fstest.MapFile{Data: []byte(`DROP TABLE gadgets;`)}

	return fixture
}

// fixtureBroken содержит заведомо неприменимую миграцию.
func fixtureBroken() fstest.MapFS {
	return fstest.MapFS{
		"000001_broken.up.sql":   {Data: []byte(`ЭТО НЕ SQL;`)},
		"000001_broken.down.sql": {Data: []byte(`SELECT 1;`)},
	}
}

func TestPoolConnectsToAvailableDatabase(t *testing.T) {
	dsn := testutil.NewDatabase(t)

	pool, err := postgres.NewPool(t.Context(), dsn, connectTimeout)
	if err != nil {
		t.Fatalf("создание пула: %v", err)
	}

	defer pool.Close()

	if err = pool.Ping(t.Context()); err != nil {
		t.Errorf("пул неработоспособен: %v", err)
	}
}

func TestPoolFailsOnUnreachableDatabase(t *testing.T) {
	testutil.RequirePostgres(t)

	const unreachable = "postgres://gophermart:gophermart@127.0.0.1:1/gophermart?sslmode=disable"

	pool, err := postgres.NewPool(t.Context(), unreachable, 2*time.Second)
	if err == nil {
		pool.Close()
		t.Fatal("ожидалась ошибка подключения к недоступной базе")
	}
}

func TestPoolFailsOnMalformedDSN(t *testing.T) {
	pool, err := postgres.NewPool(t.Context(), "это не строка подключения", connectTimeout)
	if err == nil {
		pool.Close()
		t.Fatal("ожидалась ошибка разбора строки подключения")
	}
}

func TestMigrateAppliesMigrationsToEmptyDatabase(t *testing.T) {
	dsn := testutil.NewDatabase(t)

	if err := postgres.Migrate(t.Context(), dsn, fixtureV1V2()); err != nil {
		t.Fatalf("применение миграций: %v", err)
	}

	for _, table := range []string{"widgets", "gadgets"} {
		if !testutil.TableExists(t, dsn, table) {
			t.Errorf("таблица %s не создана", table)
		}
	}

	if version, dirty := testutil.SchemaVersion(t, dsn); version != 2 || dirty {
		t.Errorf("неожиданное состояние схемы: версия %d, повреждена %t", version, dirty)
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	dsn := testutil.NewDatabase(t)

	if err := postgres.Migrate(t.Context(), dsn, fixtureV1V2()); err != nil {
		t.Fatalf("первое применение миграций: %v", err)
	}

	if err := postgres.Migrate(t.Context(), dsn, fixtureV1V2()); err != nil {
		t.Fatalf("повторное применение миграций: %v", err)
	}

	if version, dirty := testutil.SchemaVersion(t, dsn); version != 2 || dirty {
		t.Errorf("повторный запуск изменил схему: версия %d, повреждена %t", version, dirty)
	}
}

func TestMigrateAppliesOnlyMissingMigrations(t *testing.T) {
	dsn := testutil.NewDatabase(t)

	if err := postgres.Migrate(t.Context(), dsn, fixtureV1()); err != nil {
		t.Fatalf("применение первой версии: %v", err)
	}

	if testutil.TableExists(t, dsn, "gadgets") {
		t.Fatal("вторая миграция применена преждевременно")
	}

	if err := postgres.Migrate(t.Context(), dsn, fixtureV1V2()); err != nil {
		t.Fatalf("применение недостающих миграций: %v", err)
	}

	if !testutil.TableExists(t, dsn, "gadgets") {
		t.Error("недостающая миграция не применена")
	}

	if version, _ := testutil.SchemaVersion(t, dsn); version != 2 {
		t.Errorf("неожиданная версия схемы: %d", version)
	}
}

func TestMigrateFailsAndMarksSchemaDirty(t *testing.T) {
	dsn := testutil.NewDatabase(t)

	if err := postgres.Migrate(t.Context(), dsn, fixtureBroken()); err == nil {
		t.Fatal("ожидалась ошибка применения неприменимой миграции")
	}

	if _, dirty := testutil.SchemaVersion(t, dsn); !dirty {
		t.Fatal("схема не помечена как повреждённая")
	}
}

func TestMigrateRefusesDirtySchema(t *testing.T) {
	dsn := testutil.NewDatabase(t)

	if err := postgres.Migrate(t.Context(), dsn, fixtureBroken()); err == nil {
		t.Fatal("ожидалась ошибка применения неприменимой миграции")
	}

	err := postgres.Migrate(t.Context(), dsn, fixtureV1())
	if !errors.Is(err, postgres.ErrDirtySchema) {
		t.Errorf("повреждённая схема не распознана: %v", err)
	}
}

func TestMigrateSucceedsWithoutMigrations(t *testing.T) {
	dsn := testutil.NewDatabase(t)

	if err := postgres.Migrate(t.Context(), dsn, fstest.MapFS{}); err != nil {
		t.Fatalf("пустой набор миграций должен применяться успешно: %v", err)
	}

	if testutil.TableExists(t, dsn, "schema_migrations") {
		t.Error("пустой набор миграций не должен обращаться к базе данных")
	}
}

func TestMigrateAppliesEmbeddedMigrations(t *testing.T) {
	dsn := testutil.NewDatabase(t)

	if err := postgres.Migrate(t.Context(), dsn, migrations.FS); err != nil {
		t.Fatalf("применение встроенных миграций: %v", err)
	}
}
