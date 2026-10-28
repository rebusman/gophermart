package migrations_test

import (
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/golang-migrate/migrate/v4/source/iofs"

	"gophermart/migrations"
)

// firstVersion возвращает наименьшую версию миграции, которую драйвер
// источника находит в fsys, и признак того, что набор непуст.
//
// Функция повторяет тот путь, которым набор читает применяющий миграции код,
// поэтому проверяет не наличие файлов в каталоге, а их пригодность: файл, имя
// которого не разбирается как версия, драйвером игнорируется и в набор не
// попадает.
func firstVersion(t *testing.T, fsys fs.FS) (uint, bool) {
	t.Helper()

	src, err := iofs.New(fsys, ".")
	if err != nil {
		t.Fatalf("чтение каталога миграций: %v", err)
	}

	defer func() {
		if closeErr := src.Close(); closeErr != nil {
			t.Errorf("закрытие источника миграций: %v", closeErr)
		}
	}()

	version, err := src.First()
	if err != nil {
		return 0, false
	}

	return version, true
}

// TestEmbeddedMigrationsAreNotEmpty закрепляет, что в бинарник встроена хотя бы
// одна пригодная миграция.
//
// Проверка не требует PostgreSQL и потому выполняется везде, включая окружения,
// где интеграционные тесты пропускаются: именно в таком окружении пустой набор
// миграций и остался незамеченным.
func TestEmbeddedMigrationsAreNotEmpty(t *testing.T) {
	version, ok := firstVersion(t, migrations.FS)
	if !ok {
		t.Fatal("встроенный набор миграций пуст: сервис не сможет создать схему")
	}

	if version != 1 {
		t.Errorf("наименьшая версия миграции: got %d, want 1", version)
	}
}

// TestEmptySourceIsRecognizedAsEmpty закрепляет, что проверка непустоты
// содержательна: набор без пригодных миграций распознаётся как пустой, а не
// принимается за актуальный.
func TestEmptySourceIsRecognizedAsEmpty(t *testing.T) {
	empty := fstest.MapFS{
		"README.md": {Data: []byte("каталог без единой миграции")},
	}

	if _, ok := firstVersion(t, empty); ok {
		t.Error("набор без миграций признан непустым")
	}
}
