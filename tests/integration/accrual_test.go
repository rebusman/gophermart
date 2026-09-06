package integration_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"gophermart/internal/accrual"
	"gophermart/internal/auth"
	"gophermart/internal/service"
	"gophermart/internal/storage/postgres"
	httptransport "gophermart/internal/transport/http"
	"gophermart/internal/transport/http/handlers"
	"gophermart/migrations"
	"gophermart/tests/testutil"
)

// Параметры фонового расчёта в сквозных тестах.
//
// Интервалы намеренно малы: тесты проверяют поведение, а не выдержку пауз.
const (
	accrualPollInterval   = 5 * time.Millisecond
	accrualBackoffBase    = 5 * time.Millisecond
	accrualBackoffCap     = 20 * time.Millisecond
	accrualRequestTimeout = time.Second
	accrualLease          = 5 * time.Second
	accrualRetryAfter     = 50 * time.Millisecond
	accrualBatchSize      = 8
)

// fakeAccrualSystem — подставная внешняя система расчёта.
//
// Ответ на каждый номер заказа задаётся тестом, а обращения считаются: это
// позволяет проверить не только исход, но и то, что финализированные заказы
// больше не опрашиваются.
type fakeAccrualSystem struct {
	mu sync.Mutex

	responses map[string]func(w http.ResponseWriter)
	calls     map[string]int

	server *httptest.Server
}

// newFakeAccrualSystem поднимает подставную систему расчёта.
func newFakeAccrualSystem(t *testing.T) *fakeAccrualSystem {
	t.Helper()

	fake := &fakeAccrualSystem{
		responses: make(map[string]func(w http.ResponseWriter)),
		calls:     make(map[string]int),
	}

	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		number := strings.TrimPrefix(r.URL.Path, "/api/orders/")

		fake.mu.Lock()
		fake.calls[number]++
		respond := fake.responses[number]
		fake.mu.Unlock()

		if respond == nil {
			w.WriteHeader(http.StatusNoContent)

			return
		}

		respond(w)
	}))

	t.Cleanup(fake.server.Close)

	return fake
}

// respondProcessed отвечает завершённым расчётом с указанной суммой.
func (f *fakeAccrualSystem) respondProcessed(number, sum string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.responses[number] = func(w http.ResponseWriter) {
		body := `{"order":"` + number + `","status":"PROCESSED"`
		if sum != "" {
			body += `,"accrual":` + sum
		}

		body += `}`

		_, _ = w.Write([]byte(body))
	}
}

// respondStatus отвечает указанным статусом без суммы.
func (f *fakeAccrualSystem) respondStatus(number, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.responses[number] = func(w http.ResponseWriter) {
		_, _ = w.Write([]byte(`{"order":"` + number + `","status":"` + status + `"}`))
	}
}

// respondCode отвечает указанным кодом без тела.
func (f *fakeAccrualSystem) respondCode(number string, code int) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.responses[number] = func(w http.ResponseWriter) {
		w.WriteHeader(code)
	}
}

// callsFor возвращает число обращений по номеру заказа.
func (f *fakeAccrualSystem) callsFor(number string) int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.calls[number]
}

// accrualEnvironment — сервис вместе с фоновым расчётом поверх свежей базы.
type accrualEnvironment struct {
	router *httptransport.Router
	pool   *pgxpool.Pool
	fake   *fakeAccrualSystem
	stop   func()
}

// newAccrualEnvironment собирает маршрутизатор, подставную систему расчёта и
// запущенный фоновый воркер поверх свежей базы данных.
//
// Аргумент workers задаёт число одновременно работающих воркеров: несколько
// экземпляров воспроизводят несколько реплик сервиса над одной базой.
func newAccrualEnvironment(t *testing.T, workers int) *accrualEnvironment {
	t.Helper()

	dsn := testutil.NewDatabase(t)

	if _, err := postgres.Migrate(t.Context(), dsn, migrations.FS); err != nil {
		t.Fatalf("применение миграций: %v", err)
	}

	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("создание пула подключений: %v", err)
	}

	t.Cleanup(pool.Close)

	hasher, err := auth.NewHasher(auth.MinCost)
	if err != nil {
		t.Fatalf("инициализация хеширования паролей: %v", err)
	}

	tokens, err := auth.NewTokenIssuer("сквозной-тестовый-секрет", authRouterTokenTTL)
	if err != nil {
		t.Fatalf("инициализация выпуска токенов: %v", err)
	}

	authService := service.NewAuth(postgres.NewUserRepository(pool), hasher, tokens)
	orderService := service.NewOrders(postgres.NewOrderRepository(pool))
	balanceService := service.NewBalances(postgres.NewBalanceRepository(pool))

	router, err := httptransport.NewRouter(httptransport.RouterConfig{
		Logger:              slog.New(slog.DiscardHandler),
		MaxRequestBodyBytes: 1 << 20,
		Auth:                handlers.NewAuth(authService, authRouterTokenTTL),
		Orders:              handlers.NewOrders(orderService),
		Balance:             handlers.NewBalance(balanceService),
		Authenticator:       authService,
	})
	if err != nil {
		t.Fatalf("сборка маршрутизатора: %v", err)
	}

	fake := newFakeAccrualSystem(t)

	ctx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))

	var wg sync.WaitGroup

	for range workers {
		client, clientErr := accrual.NewClient(fake.server.URL, accrualRequestTimeout, accrualRetryAfter)
		if clientErr != nil {
			t.Fatalf("создание клиента системы расчёта: %v", clientErr)
		}

		accrualService := service.NewAccruals(postgres.NewOrderRepository(pool), client, service.AccrualsConfig{
			BackoffBase:  accrualBackoffBase,
			BackoffCap:   accrualBackoffCap,
			PollInterval: accrualPollInterval,
		})

		worker := accrual.NewWorker(accrualService, slog.New(slog.DiscardHandler), accrual.WorkerConfig{
			PollInterval:  accrualPollInterval,
			BatchSize:     accrualBatchSize,
			LeaseDuration: accrualLease,
		})

		wg.Go(func() {
			if runErr := worker.Run(ctx); runErr != nil {
				t.Errorf("воркер завершился ошибкой: %v", runErr)
			}
		})
	}

	env := &accrualEnvironment{router: router, pool: pool, fake: fake}

	env.stop = sync.OnceFunc(func() {
		cancel()
		wg.Wait()
	})

	t.Cleanup(env.stop)

	return env
}

// awaitOrderStatus ожидает, пока заказ не окажется в ожидаемом состоянии.
func awaitOrderStatus(t *testing.T, env *accrualEnvironment, number, want string) {
	t.Helper()

	const timeout = 5 * time.Second

	deadline := time.Now().Add(timeout)

	var got string

	for time.Now().Before(deadline) {
		got, _, _ = orderState(t, env.pool, number)
		if got == want {
			return
		}

		time.Sleep(2 * time.Millisecond)
	}

	t.Fatalf("заказ %s не дошёл до состояния %s за %s: последнее состояние %s", number, want, timeout, got)
}

// TestEndToEndAccrualCreditsBalanceAndAllowsWithdrawal закрепляет полный
// сценарий системы: загруженный заказ получает начисление, баллы попадают на
// счёт и становятся доступны к списанию.
func TestEndToEndAccrualCreditsBalanceAndAllowsWithdrawal(t *testing.T) {
	env := newAccrualEnvironment(t, 1)
	token := registerOrderUser(t, env.router, "gopher")

	env.fake.respondProcessed(orderNumberFirst, "729.98")

	if recorder := uploadOrder(t, env.router, token, orderNumberFirst); recorder.Code != http.StatusAccepted {
		t.Fatalf("загрузка заказа: got %d, want %d", recorder.Code, http.StatusAccepted)
	}

	awaitOrderStatus(t, env, orderNumberFirst, "PROCESSED")

	orders := decodeOrders(t, listOrders(t, env.router, token))
	if len(orders) != 1 {
		t.Fatalf("неожиданное число заказов: %d", len(orders))
	}

	if orders[0]["status"] != "PROCESSED" {
		t.Errorf("неожиданное состояние заказа: %v", orders[0]["status"])
	}

	if orders[0]["accrual"] != 729.98 {
		t.Errorf("неожиданное начисление в ответе: %v", orders[0]["accrual"])
	}

	requireBalanceResponse(t, env.router, token, 729.98, 0)

	// Начисленные баллы доступны к списанию сразу после применения начисления.
	if recorder := withdraw(t, env.router, token, orderNumberSecond, "729.98"); recorder.Code != http.StatusOK {
		t.Fatalf("списание начисленных баллов: got %d, want %d", recorder.Code, http.StatusOK)
	}

	requireBalanceResponse(t, env.router, token, 0, 729.98)
}

// TestEndToEndAccrualMapsExternalStatuses закрепляет требование «Отображение
// ответа внешней системы в состояние заказа» для всех исходов контракта.
func TestEndToEndAccrualMapsExternalStatuses(t *testing.T) {
	tests := []struct {
		name   string
		number string
		setup  func(f *fakeAccrualSystem, number string)
		want   string
	}{
		{
			name:   "зарегистрирован",
			number: orderNumberFirst,
			setup:  func(f *fakeAccrualSystem, n string) { f.respondStatus(n, accrual.StatusRegistered) },
			want:   "PROCESSING",
		},
		{
			name:   "выполняется",
			number: orderNumberSecond,
			setup:  func(f *fakeAccrualSystem, n string) { f.respondStatus(n, accrual.StatusProcessing) },
			want:   "PROCESSING",
		},
		{
			name:   "отказ в начислении",
			number: orderNumberThird,
			setup:  func(f *fakeAccrualSystem, n string) { f.respondStatus(n, accrual.StatusInvalid) },
			want:   "INVALID",
		},
		{
			// Расхождение словарей — рассогласование версий, а не решение по
			// заказу: состояние остаётся начальным, проверки продолжаются.
			name:   "постороннее значение статуса",
			number: orderNumberFirst,
			setup:  func(f *fakeAccrualSystem, n string) { f.respondStatus(n, "CANCELLED") },
			want:   "NEW",
		},
		{
			// Заказ, неизвестный внешней системе, состояние тоже не меняет.
			name:   "заказ не зарегистрирован",
			number: orderNumberSecond,
			setup:  func(f *fakeAccrualSystem, n string) { f.respondCode(n, http.StatusNoContent) },
			want:   "NEW",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := newAccrualEnvironment(t, 1)
			token := registerOrderUser(t, env.router, "gopher")

			test.setup(env.fake, test.number)

			if recorder := uploadOrder(t, env.router, token, test.number); recorder.Code != http.StatusAccepted {
				t.Fatalf("загрузка заказа: got %d, want %d", recorder.Code, http.StatusAccepted)
			}

			if test.want == "NEW" {
				// Состояние не меняется, поэтому дожидаемся не перехода, а
				// нескольких обращений: проверки обязаны продолжаться.
				waitForCalls(t, env.fake, test.number, 2)

				if status, _, _ := orderState(t, env.pool, test.number); status != test.want {
					t.Errorf("состояние заказа изменено: got %s, want %s", status, test.want)
				}
			} else {
				awaitOrderStatus(t, env, test.number, test.want)
			}

			// Счёт изменяется только завершённым расчётом с вознаграждением.
			requireBalanceResponse(t, env.router, token, 0, 0)
		})
	}
}

// TestEndToEndAccrualKeepsStatusWhenOrderUnknown закрепляет сценарий «Заказ не
// зарегистрирован во внешней системе»: состояние расчёта не изменяется вовсе.
func TestEndToEndAccrualKeepsStatusWhenOrderUnknown(t *testing.T) {
	env := newAccrualEnvironment(t, 1)
	token := registerOrderUser(t, env.router, "gopher")

	env.fake.respondCode(orderNumberFirst, http.StatusNoContent)

	if recorder := uploadOrder(t, env.router, token, orderNumberFirst); recorder.Code != http.StatusAccepted {
		t.Fatalf("загрузка заказа: got %d, want %d", recorder.Code, http.StatusAccepted)
	}

	// Дожидаемся нескольких обращений, чтобы убедиться: проверки продолжаются,
	// а состояние остаётся прежним.
	waitForCalls(t, env.fake, orderNumberFirst, 2)

	status, accrualValue, attempts := orderState(t, env.pool, orderNumberFirst)

	if status != "NEW" {
		t.Errorf("ответ об отсутствии регистрации изменил состояние: got %s, want NEW", status)
	}

	if accrualValue != "" {
		t.Errorf("создано начисление: %q", accrualValue)
	}

	if attempts == 0 {
		t.Error("счётчик попыток не увеличен")
	}

	requireBalanceResponse(t, env.router, token, 0, 0)
}

// TestEndToEndAccrualDoesNotInvalidateOnServerFailure закрепляет требование
// «Сбой внешней системы не является результатом расчёта».
func TestEndToEndAccrualDoesNotInvalidateOnServerFailure(t *testing.T) {
	env := newAccrualEnvironment(t, 1)
	token := registerOrderUser(t, env.router, "gopher")

	env.fake.respondCode(orderNumberFirst, http.StatusInternalServerError)

	if recorder := uploadOrder(t, env.router, token, orderNumberFirst); recorder.Code != http.StatusAccepted {
		t.Fatalf("загрузка заказа: got %d, want %d", recorder.Code, http.StatusAccepted)
	}

	waitForCalls(t, env.fake, orderNumberFirst, 2)

	status, _, _ := orderState(t, env.pool, orderNumberFirst)
	if status == "INVALID" {
		t.Error("сбой внешней системы переведён в отказ в начислении")
	}

	requireBalanceResponse(t, env.router, token, 0, 0)

	// Пользовательские эндпоинты продолжают работать при недоступной системе
	// расчёта.
	if recorder := listOrders(t, env.router, token); recorder.Code != http.StatusOK {
		t.Errorf("список заказов недоступен при сбое системы расчёта: %d", recorder.Code)
	}
}

// TestEndToEndAccrualStopsPollingFinalizedOrders закрепляет требование
// «Окончательные состояния расчёта не опрашиваются повторно».
func TestEndToEndAccrualStopsPollingFinalizedOrders(t *testing.T) {
	env := newAccrualEnvironment(t, 1)
	token := registerOrderUser(t, env.router, "gopher")

	env.fake.respondProcessed(orderNumberFirst, "100")

	if recorder := uploadOrder(t, env.router, token, orderNumberFirst); recorder.Code != http.StatusAccepted {
		t.Fatalf("загрузка заказа: got %d, want %d", recorder.Code, http.StatusAccepted)
	}

	awaitOrderStatus(t, env, orderNumberFirst, "PROCESSED")

	settled := env.fake.callsFor(orderNumberFirst)

	// Циклов заведомо больше одного: если бы финализированный заказ
	// опрашивался, счётчик обращений вырос бы.
	time.Sleep(20 * accrualPollInterval)

	if got := env.fake.callsFor(orderNumberFirst); got != settled {
		t.Errorf("финализированный заказ опрошен повторно: было %d обращений, стало %d", settled, got)
	}

	// Начисление осталось однократным.
	requireBalanceResponse(t, env.router, token, 100, 0)

	// Перезапуск не возобновляет опрос: окончательность состояния хранится в
	// базе, а не в памяти процесса.
	env.stop()
	restartWorker(t, env)

	if got := env.fake.callsFor(orderNumberFirst); got != settled {
		t.Errorf("перезапуск возобновил опрос финализированного заказа: было %d, стало %d", settled, got)
	}

	requireBalanceResponse(t, env.router, token, 100, 0)
}

// restartWorker поднимает новый воркер над той же базой и даёт ему поработать
// несколько циклов.
//
// Помощник воспроизводит перезапуск сервиса: состояние планировщика и
// окончательность расчёта живут в базе, а не в памяти процесса.
func restartWorker(t *testing.T, env *accrualEnvironment) {
	t.Helper()

	client, err := accrual.NewClient(env.fake.server.URL, accrualRequestTimeout, accrualRetryAfter)
	if err != nil {
		t.Fatalf("создание клиента системы расчёта: %v", err)
	}

	accrualService := service.NewAccruals(postgres.NewOrderRepository(env.pool), client, service.AccrualsConfig{
		BackoffBase:  accrualBackoffBase,
		BackoffCap:   accrualBackoffCap,
		PollInterval: accrualPollInterval,
	})

	worker := accrual.NewWorker(accrualService, slog.New(slog.DiscardHandler), accrual.WorkerConfig{
		PollInterval:  accrualPollInterval,
		BatchSize:     accrualBatchSize,
		LeaseDuration: accrualLease,
	})

	ctx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))

	done := make(chan struct{})

	go func() {
		defer close(done)

		if runErr := worker.Run(ctx); runErr != nil {
			t.Errorf("перезапущенный воркер завершился ошибкой: %v", runErr)
		}
	}()

	time.Sleep(20 * accrualPollInterval)

	cancel()
	<-done
}

// TestEndToEndAccrualPausesOnRateLimit закрепляет требование «Превышение
// лимита запросов приостанавливает опрос целиком».
func TestEndToEndAccrualPausesOnRateLimit(t *testing.T) {
	env := newAccrualEnvironment(t, 1)
	token := registerOrderUser(t, env.router, "gopher")

	var limited atomic.Bool

	limited.Store(true)

	env.fake.mu.Lock()
	env.fake.responses[orderNumberFirst] = func(w http.ResponseWriter) {
		if limited.Load() {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)

			return
		}

		_, _ = w.Write([]byte(`{"order":"` + orderNumberFirst + `","status":"PROCESSED","accrual":50}`))
	}
	env.fake.mu.Unlock()

	if recorder := uploadOrder(t, env.router, token, orderNumberFirst); recorder.Code != http.StatusAccepted {
		t.Fatalf("загрузка заказа: got %d, want %d", recorder.Code, http.StatusAccepted)
	}

	waitForCalls(t, env.fake, orderNumberFirst, 1)

	before := env.fake.callsFor(orderNumberFirst)

	// Пауза назначена внешней системой на секунду: за это время новых
	// обращений быть не должно, хотя интервал опроса на порядки меньше.
	time.Sleep(200 * time.Millisecond)

	if got := env.fake.callsFor(orderNumberFirst); got != before {
		t.Errorf("опрос продолжился во время паузы: было %d обращений, стало %d", before, got)
	}

	status, _, attempts := orderState(t, env.pool, orderNumberFirst)

	if status != "NEW" {
		t.Errorf("отказ по лимиту изменил состояние заказа: got %s, want NEW", status)
	}

	if attempts != 0 {
		t.Errorf("отказ по лимиту увеличил персональную отсрочку: попыток %d", attempts)
	}

	// После снятия лимита опрос возобновляется и заказ доходит до результата.
	limited.Store(false)
	awaitOrderStatus(t, env, orderNumberFirst, "PROCESSED")
	requireBalanceResponse(t, env.router, token, 50, 0)
}

// TestEndToEndAccrualKeepsAbsentAccrualForProcessed закрепляет сценарий
// «Расчёт завершён без вознаграждения»: поле начисления в ответе отсутствует.
func TestEndToEndAccrualKeepsAbsentAccrualForProcessed(t *testing.T) {
	env := newAccrualEnvironment(t, 1)
	token := registerOrderUser(t, env.router, "gopher")

	env.fake.respondProcessed(orderNumberFirst, "")

	if recorder := uploadOrder(t, env.router, token, orderNumberFirst); recorder.Code != http.StatusAccepted {
		t.Fatalf("загрузка заказа: got %d, want %d", recorder.Code, http.StatusAccepted)
	}

	awaitOrderStatus(t, env, orderNumberFirst, "PROCESSED")

	orders := decodeOrders(t, listOrders(t, env.router, token))
	if len(orders) != 1 {
		t.Fatalf("неожиданное число заказов: %d", len(orders))
	}

	if _, ok := orders[0]["accrual"]; ok {
		t.Errorf("поле начисления присутствует у расчёта без вознаграждения: %v", orders[0])
	}

	requireBalanceResponse(t, env.router, token, 0, 0)
}

// TestEndToEndAccrualDoesNotExposeSchedulerState закрепляет требование
// «Состояние планировщика повторов не входит в публичный контракт».
func TestEndToEndAccrualDoesNotExposeSchedulerState(t *testing.T) {
	env := newAccrualEnvironment(t, 1)
	token := registerOrderUser(t, env.router, "gopher")

	env.fake.respondCode(orderNumberFirst, http.StatusInternalServerError)

	if recorder := uploadOrder(t, env.router, token, orderNumberFirst); recorder.Code != http.StatusAccepted {
		t.Fatalf("загрузка заказа: got %d, want %d", recorder.Code, http.StatusAccepted)
	}

	waitForCalls(t, env.fake, orderNumberFirst, 2)

	recorder := listOrders(t, env.router, token)
	orders := decodeOrders(t, recorder)

	if len(orders) != 1 {
		t.Fatalf("неожиданное число заказов: %d", len(orders))
	}

	allowed := map[string]bool{"number": true, "status": true, "accrual": true, "uploaded_at": true}

	for field := range orders[0] {
		if !allowed[field] {
			t.Errorf("ответ содержит постороннее поле %q: %s", field, recorder.Body)
		}
	}

	for _, leaked := range []string{"attempts", "next_attempt_at"} {
		if strings.Contains(recorder.Body.String(), leaked) {
			t.Errorf("ответ раскрывает состояние планировщика %q: %s", leaked, recorder.Body)
		}
	}
}

// TestEndToEndAccrualSharesWorkBetweenWorkers закрепляет сценарий «Два
// экземпляра выбирают задания одновременно»: ни один заказ не обработан дважды.
func TestEndToEndAccrualSharesWorkBetweenWorkers(t *testing.T) {
	env := newAccrualEnvironment(t, 3)
	token := registerOrderUser(t, env.router, "gopher")

	numbers := []string{orderNumberFirst, orderNumberSecond, orderNumberThird}
	for _, number := range numbers {
		env.fake.respondProcessed(number, "100")

		if recorder := uploadOrder(t, env.router, token, number); recorder.Code != http.StatusAccepted {
			t.Fatalf("загрузка заказа %s: got %d, want %d", number, recorder.Code, http.StatusAccepted)
		}
	}

	for _, number := range numbers {
		awaitOrderStatus(t, env, number, "PROCESSED")
	}

	// Суммарный баланс равен сумме начислений: ни одно не применено дважды.
	requireBalanceResponse(t, env.router, token, 300, 0)

	// Обращений не больше числа заказов: аренда и SKIP LOCKED экономят
	// запросы к внешней системе.
	for _, number := range numbers {
		if got := env.fake.callsFor(number); got != 1 {
			t.Errorf("заказ %s опрошен %d раз, ожидался один", number, got)
		}
	}
}

// TestEndToEndAccrualReturnsJobAfterInstanceDeath закрепляет сценарий
// «Экземпляр погиб между выборкой и применением результата»: заказ возвращается
// в выборку по истечении аренды, без отдельной процедуры уборки.
func TestEndToEndAccrualReturnsJobAfterInstanceDeath(t *testing.T) {
	env := newAccrualEnvironment(t, 1)
	token := registerOrderUser(t, env.router, "gopher")

	env.fake.respondCode(orderNumberFirst, http.StatusInternalServerError)

	if recorder := uploadOrder(t, env.router, token, orderNumberFirst); recorder.Code != http.StatusAccepted {
		t.Fatalf("загрузка заказа: got %d, want %d", recorder.Code, http.StatusAccepted)
	}

	waitForCalls(t, env.fake, orderNumberFirst, 1)

	// Гибель экземпляра воспроизводится остановкой воркера: заказ остаётся
	// занятым до истечения аренды.
	env.stop()

	repo := postgres.NewOrderRepository(env.pool)

	jobs, err := repo.ClaimAccrualJobs(t.Context(), 10, accrualLease)
	if err != nil {
		t.Fatalf("выборка заданий сразу после гибели: %v", err)
	}

	if len(jobs) != 0 {
		t.Error("занятое задание доступно другому экземпляру до истечения аренды")
	}

	status, _, _ := orderState(t, env.pool, orderNumberFirst)
	if status != "NEW" {
		t.Errorf("состояние заказа изменилось при гибели экземпляра: got %s, want NEW", status)
	}

	// По истечении аренды заказ возвращается в работу сам.
	makeDue(t, env.pool, orderNumberFirst)

	jobs, err = repo.ClaimAccrualJobs(t.Context(), 10, accrualLease)
	if err != nil {
		t.Fatalf("выборка заданий после истечения аренды: %v", err)
	}

	if len(jobs) != 1 {
		t.Errorf("заказ не вернулся в выборку после истечения аренды: %d заданий", len(jobs))
	}
}

// TestEndToEndAccrualDoesNotHoldTransactionDuringExternalCall закрепляет
// требование «Внешний вызов не выполняется внутри транзакции базы данных»:
// пользовательские запросы не ждут медленного ответа системы расчёта.
func TestEndToEndAccrualDoesNotHoldTransactionDuringExternalCall(t *testing.T) {
	env := newAccrualEnvironment(t, 1)
	token := registerOrderUser(t, env.router, "gopher")

	entered := make(chan struct{}, 1)
	release := make(chan struct{})

	env.fake.mu.Lock()
	env.fake.responses[orderNumberFirst] = func(w http.ResponseWriter) {
		select {
		case entered <- struct{}{}:
		default:
		}

		<-release

		_, _ = w.Write([]byte(`{"order":"` + orderNumberFirst + `","status":"PROCESSED","accrual":10}`))
	}
	env.fake.mu.Unlock()

	t.Cleanup(func() { close(release) })

	if recorder := uploadOrder(t, env.router, token, orderNumberFirst); recorder.Code != http.StatusAccepted {
		t.Fatalf("загрузка заказа: got %d, want %d", recorder.Code, http.StatusAccepted)
	}

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("подставная система расчёта не получила обращения")
	}

	// Пока внешний вызов висит, пользовательские запросы обязаны выполняться
	// без ожидания: транзакция и блокировка строки не удерживаются.
	done := make(chan struct{})

	go func() {
		defer close(done)

		if recorder := listOrders(t, env.router, token); recorder.Code != http.StatusOK {
			t.Errorf("список заказов: got %d, want %d", recorder.Code, http.StatusOK)
		}

		if recorder := getBalance(t, env.router, token); recorder.Code != http.StatusOK {
			t.Errorf("баланс: got %d, want %d", recorder.Code, http.StatusOK)
		}

		if recorder := uploadOrder(t, env.router, token, orderNumberSecond); recorder.Code != http.StatusAccepted {
			t.Errorf("загрузка второго заказа: got %d, want %d", recorder.Code, http.StatusAccepted)
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("пользовательские запросы заблокированы обращением к внешней системе")
	}
}

// TestEndToEndAccrualCoexistsWithWithdrawals закрепляет сценарий «Начисление
// одновременно со списанием»: счёт остаётся согласованным.
func TestEndToEndAccrualCoexistsWithWithdrawals(t *testing.T) {
	env := newAccrualEnvironment(t, 2)
	token := registerOrderUser(t, env.router, "gopher")

	creditUser(t, env.pool, "gopher", "1000")

	numbers := []string{orderNumberFirst, orderNumberSecond, orderNumberThird}
	for _, number := range numbers {
		env.fake.respondProcessed(number, "100")

		if recorder := uploadOrder(t, env.router, token, number); recorder.Code != http.StatusAccepted {
			t.Fatalf("загрузка заказа %s: got %d, want %d", number, recorder.Code, http.StatusAccepted)
		}
	}

	// Списания идут одновременно с начислениями.
	var wg sync.WaitGroup

	withdrawalNumbers := []string{"79927398713", "4561261212345467", "6011111111111117"}
	codes := make([]int, len(withdrawalNumbers))

	for i, number := range withdrawalNumbers {
		wg.Go(func() {
			codes[i] = withdraw(t, env.router, token, number, "100").Code
		})
	}

	wg.Wait()

	for i, code := range codes {
		if code != http.StatusOK {
			t.Errorf("списание %s завершилось кодом %d", withdrawalNumbers[i], code)
		}
	}

	for _, number := range numbers {
		awaitOrderStatus(t, env, number, "PROCESSED")
	}

	// 1000 начислено помощником + 300 фоновым расчётом − 300 списано = 1000.
	awaitBalance(t, env, token, 1000, 300)
}

// waitForCalls ожидает, пока подставная система не получит указанное число
// обращений по номеру заказа.
func waitForCalls(t *testing.T, fake *fakeAccrualSystem, number string, want int) {
	t.Helper()

	const timeout = 5 * time.Second

	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		if fake.callsFor(number) >= want {
			return
		}

		time.Sleep(time.Millisecond)
	}

	t.Fatalf("подставная система получила %d обращений по заказу %s, ожидалось %d",
		fake.callsFor(number), number, want)
}

// awaitBalance ожидает, пока счёт не примет ожидаемые значения.
//
// Ожидание нужно там, где счёт изменяется фоновым процессом: момент применения
// последнего начисления заранее неизвестен.
func awaitBalance(t *testing.T, env *accrualEnvironment, token string, current, withdrawn float64) {
	t.Helper()

	const timeout = 5 * time.Second

	deadline := time.Now().Add(timeout)

	var payload map[string]any

	for time.Now().Before(deadline) {
		payload = decodeBalance(t, getBalance(t, env.router, token))

		if payload["current"] == current && payload["withdrawn"] == withdrawn {
			return
		}

		time.Sleep(2 * time.Millisecond)
	}

	body, _ := json.Marshal(payload)
	t.Fatalf("счёт не сошёлся за %s: got %s, want current %v, withdrawn %v", timeout, body, current, withdrawn)
}
