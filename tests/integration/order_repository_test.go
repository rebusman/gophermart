package integration_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"gophermart/internal/domain"
	"gophermart/internal/storage/postgres"
	"gophermart/migrations"
	"gophermart/tests/testutil"
)

// Номера заказов, используемые интеграционными тестами репозитория.
//
// Все значения проходят проверку алгоритмом Луна: репозиторий получает номер
// уже разобранным доменным типом, поэтому непроходящее значение до него не
// доходит и в тестах не встречается.
const (
	orderNumberFirst  = "9278923470"
	orderNumberSecond = "12345678903"
	orderNumberThird  = "346436439"
)

// newOrderRepository поднимает пустую базу, применяет к ней миграции и
// возвращает репозитории заказов и пользователей поверх неё вместе с пулом,
// который закрывается по завершении теста.
//
// Репозиторий пользователей нужен потому, что заказ ссылается на владельца
// внешним ключом: заказа без существующего пользователя в схеме не бывает.
func newOrderRepository(t *testing.T) (*postgres.OrderRepository, *postgres.UserRepository, *pgxpool.Pool) {
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

	return postgres.NewOrderRepository(pool), postgres.NewUserRepository(pool), pool
}

// createOrderOwner заводит пользователя с указанным логином и возвращает его
// идентификатор.
func createOrderOwner(t *testing.T, users *postgres.UserRepository, login string) domain.UserID {
	t.Helper()

	user := newTestUser()
	user.Login = login

	if err := users.CreateUser(t.Context(), user); err != nil {
		t.Fatalf("создание пользователя %s: %v", login, err)
	}

	return user.ID
}

// newOrder собирает заказ в начальном состоянии расчёта.
func newOrder(t *testing.T, number string, userID domain.UserID) domain.Order {
	t.Helper()

	parsed, err := domain.ParseOrderNumber(number)
	if err != nil {
		t.Fatalf("разбор номера заказа %s: %v", number, err)
	}

	return domain.Order{Number: parsed, UserID: userID, Status: domain.OrderStatusNew}
}

// countOrders возвращает общее число заказов в базе.
//
// Помощник проверяет инвариант «один номер — один заказ» в тестах, которые
// загружают единственный номер: любая лишняя строка означает, что повторная
// или конкурентная загрузка создала второй заказ.
func countOrders(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()

	var count int

	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM orders`).Scan(&count); err != nil {
		t.Fatalf("подсчёт заказов: %v", err)
	}

	return count
}

// sameOrder сообщает, совпадают ли все наблюдаемые свойства двух заказов.
//
// Сравнение поэлементное, а не оператором равенства: время сравнивается
// методом Equal, а начисление — по значению, а не по адресу.
func sameOrder(first, second domain.Order) bool {
	if first.Number != second.Number || first.Status != second.Status {
		return false
	}

	if !first.UploadedAt.Equal(second.UploadedAt) {
		return false
	}

	switch {
	case first.Accrual == nil && second.Accrual == nil:
		return true
	case first.Accrual == nil || second.Accrual == nil:
		return false
	default:
		return first.Accrual.Equal(*second.Accrual)
	}
}

// uuidOf возвращает идентификатор пользователя в виде, пригодном для передачи
// драйверу.
func uuidOf(t *testing.T, id domain.UserID) uuid.UUID {
	t.Helper()

	parsed, err := uuid.Parse(id.String())
	if err != nil {
		t.Fatalf("разбор идентификатора пользователя: %v", err)
	}

	return parsed
}

// TestOrderRepositoryCreatesOrderInNewStatus закрепляет сценарий «Новый номер
// принят в обработку»: ранее неизвестный номер создаёт заказ в состоянии NEW,
// закреплённый за загрузившим его пользователем.
func TestOrderRepositoryCreatesOrderInNewStatus(t *testing.T) {
	orders, users, _ := newOrderRepository(t)
	userID := createOrderOwner(t, users, "gopher")

	created, err := orders.CreateOrder(t.Context(), newOrder(t, orderNumberFirst, userID))
	if err != nil {
		t.Fatalf("создание заказа: %v", err)
	}

	if !created {
		t.Fatal("новый номер не создал заказ")
	}

	stored, err := orders.OrdersByUser(t.Context(), userID)
	if err != nil {
		t.Fatalf("чтение заказов пользователя: %v", err)
	}

	if len(stored) != 1 {
		t.Fatalf("неожиданное число заказов: got %d, want 1", len(stored))
	}

	if stored[0].Status != domain.OrderStatusNew {
		t.Errorf("неожиданное состояние расчёта: got %s, want %s", stored[0].Status, domain.OrderStatusNew)
	}

	if stored[0].Number.String() != orderNumberFirst {
		t.Errorf("неожиданный номер заказа: got %s, want %s", stored[0].Number, orderNumberFirst)
	}

	if stored[0].UploadedAt.IsZero() {
		t.Error("время загрузки не проставлено базой данных")
	}
}

// TestOrderRepositoryDoesNotDuplicateOrder закрепляет сценарий «Повторная
// загрузка своего номера»: вторая вставка того же номера не создаёт второй
// заказ и не меняет состояние существующего.
func TestOrderRepositoryDoesNotDuplicateOrder(t *testing.T) {
	orders, users, pool := newOrderRepository(t)
	userID := createOrderOwner(t, users, "gopher")
	order := newOrder(t, orderNumberFirst, userID)

	if _, err := orders.CreateOrder(t.Context(), order); err != nil {
		t.Fatalf("первое создание заказа: %v", err)
	}

	before, err := orders.OrdersByUser(t.Context(), userID)
	if err != nil {
		t.Fatalf("чтение заказов до повторной вставки: %v", err)
	}

	created, err := orders.CreateOrder(t.Context(), order)
	if err != nil {
		t.Fatalf("повторное создание заказа: %v", err)
	}

	if created {
		t.Error("повторная вставка сообщила о создании заказа")
	}

	if count := countOrders(t, pool); count != 1 {
		t.Errorf("неожиданное число заказов с номером: got %d, want 1", count)
	}

	after, err := orders.OrdersByUser(t.Context(), userID)
	if err != nil {
		t.Fatalf("чтение заказов после повторной вставки: %v", err)
	}

	if len(after) != 1 || !sameOrder(after[0], before[0]) {
		t.Errorf("состояние существующего заказа изменилось: got %+v, want %+v", after, before)
	}
}

// TestOrderRepositoryReportsFirstOwner закрепляет сценарий «Загрузка чужого
// номера» на уровне хранилища: занятый номер сохраняет первого владельца, а
// его состояние не меняется от попытки второго пользователя.
func TestOrderRepositoryReportsFirstOwner(t *testing.T) {
	orders, users, pool := newOrderRepository(t)
	first := createOrderOwner(t, users, "gopher")
	second := createOrderOwner(t, users, "другой-gopher")

	if _, err := orders.CreateOrder(t.Context(), newOrder(t, orderNumberFirst, first)); err != nil {
		t.Fatalf("создание заказа первым пользователем: %v", err)
	}

	before, err := orders.OrdersByUser(t.Context(), first)
	if err != nil {
		t.Fatalf("чтение заказов первого пользователя: %v", err)
	}

	created, err := orders.CreateOrder(t.Context(), newOrder(t, orderNumberFirst, second))
	if err != nil {
		t.Fatalf("создание заказа вторым пользователем: %v", err)
	}

	if created {
		t.Error("занятый номер создал второй заказ")
	}

	owner, err := orders.OrderOwner(t.Context(), before[0].Number)
	if err != nil {
		t.Fatalf("чтение владельца заказа: %v", err)
	}

	if owner != first {
		t.Errorf("неожиданный владелец заказа: got %s, want %s", owner, first)
	}

	if count := countOrders(t, pool); count != 1 {
		t.Errorf("неожиданное число заказов с номером: got %d, want 1", count)
	}

	after, err := orders.OrdersByUser(t.Context(), first)
	if err != nil {
		t.Fatalf("повторное чтение заказов первого пользователя: %v", err)
	}

	if len(after) != 1 || !sameOrder(after[0], before[0]) {
		t.Errorf("состояние заказа изменилось: got %+v, want %+v", after, before)
	}

	foreign, err := orders.OrdersByUser(t.Context(), second)
	if err != nil {
		t.Fatalf("чтение заказов второго пользователя: %v", err)
	}

	if len(foreign) != 0 {
		t.Errorf("второму пользователю приписан чужой заказ: %+v", foreign)
	}
}

// TestOrderRepositoryOwnerOfUnknownNumber закрепляет контракт OrderOwner:
// отсутствие заказа отличимо от прочих отказов доменной ошибкой.
func TestOrderRepositoryOwnerOfUnknownNumber(t *testing.T) {
	orders, _, _ := newOrderRepository(t)

	number, err := domain.ParseOrderNumber(orderNumberFirst)
	if err != nil {
		t.Fatalf("разбор номера заказа: %v", err)
	}

	if _, err = orders.OrderOwner(t.Context(), number); !errors.Is(err, domain.ErrOrderNotFound) {
		t.Errorf("неожиданная ошибка для несуществующего номера: %v", err)
	}
}

// TestOrderRepositoryOrdersNewestFirst закрепляет требование «Список заказов
// пользователя»: заказы возвращаются от самых новых к самым старым, а при
// совпадающих метках времени порядок доопределён номером и воспроизводим.
func TestOrderRepositoryOrdersNewestFirst(t *testing.T) {
	orders, users, pool := newOrderRepository(t)
	userID := createOrderOwner(t, users, "gopher")

	for _, number := range []string{orderNumberFirst, orderNumberSecond, orderNumberThird} {
		if _, err := orders.CreateOrder(t.Context(), newOrder(t, number, userID)); err != nil {
			t.Fatalf("создание заказа %s: %v", number, err)
		}
	}

	stored, err := orders.OrdersByUser(t.Context(), userID)
	if err != nil {
		t.Fatalf("чтение заказов пользователя: %v", err)
	}

	if len(stored) != 3 {
		t.Fatalf("неожиданное число заказов: got %d, want 3", len(stored))
	}

	for i := 1; i < len(stored); i++ {
		if stored[i-1].UploadedAt.Before(stored[i].UploadedAt) {
			t.Errorf("порядок нарушен: %s загружен раньше %s", stored[i-1].Number, stored[i].Number)
		}
	}

	// Совпадающие метки времени: порядок обязан определяться номером по
	// убыванию и не зависеть от порядка вставки.
	sameTime := `UPDATE orders SET uploaded_at = '2020-12-10T15:15:45Z' WHERE user_id = $1`
	if _, err = pool.Exec(t.Context(), sameTime, uuidOf(t, userID)); err != nil {
		t.Fatalf("выравнивание меток времени: %v", err)
	}

	tied, err := orders.OrdersByUser(t.Context(), userID)
	if err != nil {
		t.Fatalf("повторное чтение заказов: %v", err)
	}

	want := []string{orderNumberFirst, orderNumberSecond, orderNumberThird}
	slices.SortFunc(want, func(a, b string) int { return strings.Compare(b, a) })

	for i, number := range want {
		if tied[i].Number.String() != number {
			t.Errorf("неожиданный порядок при совпадающих метках: got %s, want %s", tied[i].Number, number)
		}
	}
}

// TestOrderRepositoryReadsOptionalAccrual закрепляет сценарии «Заказ без
// начисления» и «Заказ с начислением»: NULL в базе даёт отсутствующее
// значение, а записанное значение читается без потери точности.
func TestOrderRepositoryReadsOptionalAccrual(t *testing.T) {
	orders, users, pool := newOrderRepository(t)
	userID := createOrderOwner(t, users, "gopher")

	if _, err := orders.CreateOrder(t.Context(), newOrder(t, orderNumberFirst, userID)); err != nil {
		t.Fatalf("создание заказа: %v", err)
	}

	stored, err := orders.OrdersByUser(t.Context(), userID)
	if err != nil {
		t.Fatalf("чтение заказов пользователя: %v", err)
	}

	if stored[0].Accrual != nil {
		t.Errorf("нерассчитанное начисление прочитано как значение: %v", stored[0].Accrual)
	}

	const accrual = "751.50"

	update := `UPDATE orders SET status = 'PROCESSED', accrual = $2 WHERE number = $1`
	if _, err = pool.Exec(t.Context(), update, orderNumberFirst, accrual); err != nil {
		t.Fatalf("запись начисления: %v", err)
	}

	processed, err := orders.OrdersByUser(t.Context(), userID)
	if err != nil {
		t.Fatalf("повторное чтение заказов: %v", err)
	}

	if processed[0].Accrual == nil {
		t.Fatal("рассчитанное начисление прочитано как отсутствующее")
	}

	if want := decimal.RequireFromString(accrual); !processed[0].Accrual.Equal(want) {
		t.Errorf("начисление искажено при чтении: got %s, want %s", processed[0].Accrual, want)
	}

	if processed[0].Status != domain.OrderStatusProcessed {
		t.Errorf("неожиданное состояние расчёта: got %s, want %s", processed[0].Status, domain.OrderStatusProcessed)
	}
}

// TestOrderRepositoryReturnsEmptyListWithoutError закрепляет сценарий «У
// пользователя нет заказов»: пустой список отличим от ошибки.
func TestOrderRepositoryReturnsEmptyListWithoutError(t *testing.T) {
	orders, users, _ := newOrderRepository(t)
	userID := createOrderOwner(t, users, "gopher")

	stored, err := orders.OrdersByUser(t.Context(), userID)
	if err != nil {
		t.Fatalf("чтение заказов пользователя без заказов: %v", err)
	}

	if len(stored) != 0 {
		t.Errorf("пользователю без заказов возвращён непустой список: %+v", stored)
	}
}
