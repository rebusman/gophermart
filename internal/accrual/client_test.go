package accrual_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gophermart/internal/accrual"
	"gophermart/internal/domain"
)

// Параметры, общие для тестов клиента.
const (
	// testOrderNumber — номер заказа, проходящий проверку алгоритмом Луна.
	testOrderNumber = "9278923470"

	// testTimeout — предельное время обращения в тестах. Значение заведомо
	// перекрывает время ответа подставного сервера.
	testTimeout = 2 * time.Second

	// testRetryAfter — пауза по умолчанию при отсутствующем заголовке.
	testRetryAfter = 42 * time.Second
)

// newOrderNumber разбирает номер заказа для передачи клиенту.
func newOrderNumber(t *testing.T) domain.OrderNumber {
	t.Helper()

	number, err := domain.ParseOrderNumber(testOrderNumber)
	if err != nil {
		t.Fatalf("разбор номера заказа: %v", err)
	}

	return number
}

// newClient поднимает подставной сервер с указанным обработчиком и создаёт
// клиент, обращающийся к нему.
func newClient(t *testing.T, handler http.HandlerFunc) *accrual.Client {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	client, err := accrual.NewClient(server.URL, testTimeout, testRetryAfter)
	if err != nil {
		t.Fatalf("создание клиента: %v", err)
	}

	return client
}

// TestClientRequestsOrderPath закрепляет формирование пути ресурса по номеру
// заказа: путь строится от базового адреса из конфигурации.
func TestClientRequestsOrderPath(t *testing.T) {
	var gotPath, gotMethod string

	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"order":"` + testOrderNumber + `","status":"PROCESSING"}`))
	})

	if _, err := client.OrderAccrual(t.Context(), newOrderNumber(t)); err != nil {
		t.Fatalf("обращение к системе расчёта: %v", err)
	}

	if want := "/api/orders/" + testOrderNumber; gotPath != want {
		t.Errorf("неожиданный путь запроса: got %q, want %q", gotPath, want)
	}

	if gotMethod != http.MethodGet {
		t.Errorf("неожиданный метод запроса: got %s, want %s", gotMethod, http.MethodGet)
	}
}

// TestClientReturnsProcessedResult закрепляет разбор завершённого расчёта:
// статус отображается в состояние заказа, сумма читается без потери точности.
func TestClientReturnsProcessedResult(t *testing.T) {
	client := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"order":"` + testOrderNumber + `","status":"PROCESSED","accrual":729.98}`))
	})

	result, err := client.OrderAccrual(t.Context(), newOrderNumber(t))
	if err != nil {
		t.Fatalf("обращение к системе расчёта: %v", err)
	}

	if result.Status != domain.OrderStatusProcessed {
		t.Errorf("неожиданное состояние: got %s, want %s", result.Status, domain.OrderStatusProcessed)
	}

	if result.Accrual == nil {
		t.Fatal("сумма начисления отсутствует")
	}

	if got := result.Accrual.String(); got != "729.98" {
		t.Errorf("сумма искажена при разборе: got %s, want 729.98", got)
	}
}

// TestClientAcceptsProcessedWithoutAccrual закрепляет законность завершённого
// расчёта без вознаграждения: сумма остаётся отсутствующей, а не нулевой.
func TestClientAcceptsProcessedWithoutAccrual(t *testing.T) {
	client := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"order":"` + testOrderNumber + `","status":"PROCESSED"}`))
	})

	result, err := client.OrderAccrual(t.Context(), newOrderNumber(t))
	if err != nil {
		t.Fatalf("обращение к системе расчёта: %v", err)
	}

	if result.Status != domain.OrderStatusProcessed {
		t.Errorf("неожиданное состояние: got %s, want %s", result.Status, domain.OrderStatusProcessed)
	}

	if result.Accrual != nil {
		t.Errorf("сумма подставлена вместо отсутствия: %s", result.Accrual)
	}
}

// TestClientIgnoresAccrualOnNonFinalStatus закрепляет, что сумма принимается
// только у завершённого расчёта: у незавершённого она означала бы зачисление
// баллов, о которых внешняя система ещё не договорилась.
func TestClientIgnoresAccrualOnNonFinalStatus(t *testing.T) {
	for _, status := range []string{accrual.StatusRegistered, accrual.StatusProcessing, accrual.StatusInvalid} {
		t.Run(status, func(t *testing.T) {
			client := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"order":"x","status":"` + status + `","accrual":500}`))
			})

			result, err := client.OrderAccrual(t.Context(), newOrderNumber(t))
			if err != nil {
				t.Fatalf("обращение к системе расчёта: %v", err)
			}

			if result.Accrual != nil {
				t.Errorf("сумма принята у незавершённого расчёта: %s", result.Accrual)
			}
		})
	}
}

// TestClientRejectsNegativeAccrual закрепляет отказ на отрицательном
// начислении: начисление не может уменьшать счёт.
func TestClientRejectsNegativeAccrual(t *testing.T) {
	client := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"order":"x","status":"PROCESSED","accrual":-1}`))
	})

	_, err := client.OrderAccrual(t.Context(), newOrderNumber(t))
	if !errors.Is(err, accrual.ErrServerFailure) {
		t.Errorf("ожидался отказ на отрицательном начислении, получено: %v", err)
	}
}

// TestClientReportsOrderNotRegistered закрепляет сценарий «Заказ не
// зарегистрирован во внешней системе»: ответ 204 не является отказом в
// начислении.
func TestClientReportsOrderNotRegistered(t *testing.T) {
	client := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	_, err := client.OrderAccrual(t.Context(), newOrderNumber(t))
	if !errors.Is(err, accrual.ErrOrderNotRegistered) {
		t.Errorf("ожидалась ошибка отсутствия регистрации, получено: %v", err)
	}
}

// TestClientReportsServerFailure закрепляет обработку кодов вне контракта как
// временного сбоя.
func TestClientReportsServerFailure(t *testing.T) {
	codes := []int{http.StatusInternalServerError, http.StatusBadGateway, http.StatusNotFound}

	for _, code := range codes {
		t.Run(http.StatusText(code), func(t *testing.T) {
			client := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(code)
			})

			_, err := client.OrderAccrual(t.Context(), newOrderNumber(t))
			if !errors.Is(err, accrual.ErrServerFailure) {
				t.Errorf("ожидалась ошибка сбоя внешней системы, получено: %v", err)
			}
		})
	}
}

// TestClientParsesRetryAfter закрепляет разбор заголовка Retry-After и
// подстановку значения по умолчанию.
//
// Ни один случай не должен давать нулевую паузу: она заставила бы сервис
// немедленно упереться в тот же лимит запросов.
func TestClientParsesRetryAfter(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   time.Duration
	}{
		{name: "число секунд", header: "60", want: time.Minute},
		{name: "заголовок отсутствует", header: "", want: testRetryAfter},
		{name: "постороннее значение", header: "скоро", want: testRetryAfter},
		{name: "дата вместо секунд", header: "Wed, 21 Oct 2015 07:28:00 GMT", want: testRetryAfter},
		{name: "ноль секунд", header: "0", want: testRetryAfter},
		{name: "отрицательное значение", header: "-5", want: testRetryAfter},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
				if test.header != "" {
					w.Header().Set("Retry-After", test.header)
				}

				w.WriteHeader(http.StatusTooManyRequests)
			})

			_, err := client.OrderAccrual(t.Context(), newOrderNumber(t))

			if !errors.Is(err, domain.ErrAccrualRateLimited) {
				t.Fatalf("ожидался отказ по лимиту запросов, получено: %v", err)
			}

			var rateLimit *domain.RateLimitError
			if !errors.As(err, &rateLimit) {
				t.Fatalf("длительность паузы не извлекается из ошибки: %v", err)
			}

			if rateLimit.RetryAfter != test.want {
				t.Errorf("неожиданная пауза: got %s, want %s", rateLimit.RetryAfter, test.want)
			}

			if rateLimit.RetryAfter <= 0 {
				t.Error("пауза нулевая: сервис немедленно упрётся в тот же лимит")
			}
		})
	}
}

// TestClientReportsUnknownStatus закрепляет сценарий «Внешняя система вернула
// неизвестное состояние»: расхождение словарей — сбой, а не решение по заказу.
func TestClientReportsUnknownStatus(t *testing.T) {
	client := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"order":"x","status":"CANCELLED"}`))
	})

	_, err := client.OrderAccrual(t.Context(), newOrderNumber(t))
	if !errors.Is(err, accrual.ErrUnknownStatus) {
		t.Errorf("ожидалась ошибка неизвестного статуса, получено: %v", err)
	}
}

// TestClientReportsMalformedBody закрепляет обработку неразбираемого тела как
// ошибки, а не как результата расчёта.
func TestClientReportsMalformedBody(t *testing.T) {
	client := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`не json`))
	})

	if _, err := client.OrderAccrual(t.Context(), newOrderNumber(t)); err == nil {
		t.Error("неразбираемое тело принято за результат расчёта")
	}
}

// TestClientStopsOnTimeout закрепляет ограничение времени обращения: внешняя
// система, не ответившая в срок, даёт ошибку, а не блокирует воркер.
func TestClientStopsOnTimeout(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(server.Close)

	client, err := accrual.NewClient(server.URL, 50*time.Millisecond, testRetryAfter)
	if err != nil {
		t.Fatalf("создание клиента: %v", err)
	}

	start := time.Now()

	_, err = client.OrderAccrual(t.Context(), newOrderNumber(t))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ожидалось истечение времени ожидания, получено: %v", err)
	}

	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("вызов не прерван вовремя: прошло %s", elapsed)
	}
}

// TestClientStopsOnContextCancel закрепляет прерывание обращения отменой
// контекста: остановка сервиса не должна дожидаться ответа внешней системы.
func TestClientStopsOnContextCancel(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(server.Close)

	client, err := accrual.NewClient(server.URL, time.Minute, testRetryAfter)
	if err != nil {
		t.Fatalf("создание клиента: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())

	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	start := time.Now()

	_, err = client.OrderAccrual(ctx, newOrderNumber(t))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ожидалась отмена контекста, получено: %v", err)
	}

	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("вызов не прерван вовремя: прошло %s", elapsed)
	}
}

// TestClientDoesNotFollowRedirects закрепляет, что клиент не следует за
// адресами, полученными от внешней системы: базовый адрес берётся только из
// конфигурации.
func TestClientDoesNotFollowRedirects(t *testing.T) {
	var elsewhereCalls int

	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		elsewhereCalls++
		_, _ = w.Write([]byte(`{"order":"x","status":"PROCESSED","accrual":1000000}`))
	}))
	t.Cleanup(elsewhere.Close)

	client := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, &http.Request{}, elsewhere.URL+"/api/orders/"+testOrderNumber, http.StatusFound)
	})

	_, err := client.OrderAccrual(t.Context(), newOrderNumber(t))
	if err == nil {
		t.Error("перенаправление принято за результат расчёта")
	}

	if elsewhereCalls != 0 {
		t.Errorf("клиент проследовал за посторонним адресом: %d обращений", elsewhereCalls)
	}
}

// TestNewClientRejectsMalformedBaseURL закрепляет отказ на некорректном
// базовом адресе: сервис не должен стартовать с адресом, по которому нельзя
// построить запрос.
func TestNewClientRejectsMalformedBaseURL(t *testing.T) {
	for _, raw := range []string{"", "localhost:8081", "://", "/api"} {
		t.Run(raw, func(t *testing.T) {
			_, err := accrual.NewClient(raw, testTimeout, testRetryAfter)
			if !errors.Is(err, accrual.ErrInvalidBaseURL) {
				t.Errorf("адрес %q принят: %v", raw, err)
			}
		})
	}
}

// TestParseStatusMapsExternalDictionary закрепляет отображение статусов
// внешней системы в состояния заказа.
//
// REGISTERED и PROCESSING дают одно состояние: словарь заказа не различает
// зарегистрированный и уже начатый расчёт, и различать их наружу незачем.
func TestParseStatusMapsExternalDictionary(t *testing.T) {
	tests := []struct {
		raw     string
		want    domain.OrderStatus
		wantErr bool
	}{
		{raw: accrual.StatusRegistered, want: domain.OrderStatusProcessing},
		{raw: accrual.StatusProcessing, want: domain.OrderStatusProcessing},
		{raw: accrual.StatusInvalid, want: domain.OrderStatusInvalid},
		{raw: accrual.StatusProcessed, want: domain.OrderStatusProcessed},
		{raw: "NEW", wantErr: true},
		{raw: "", wantErr: true},
		{raw: "processed", wantErr: true},
	}

	for _, test := range tests {
		name := test.raw
		if name == "" {
			name = "пустое значение"
		}

		t.Run(name, func(t *testing.T) {
			status, err := accrual.ParseStatus(test.raw)

			if test.wantErr {
				if !errors.Is(err, accrual.ErrUnknownStatus) {
					t.Errorf("значение %q принято как статус: %v", test.raw, err)
				}

				return
			}

			if err != nil {
				t.Fatalf("статус %q отвергнут: %v", test.raw, err)
			}

			if status != test.want {
				t.Errorf("неожиданное состояние: got %s, want %s", status, test.want)
			}
		})
	}
}

// TestClientErrorsDoNotLeakExternalDetails закрепляет, что ошибки клиента не
// переносят тело ответа внешней системы: наружу оно попасть не должно.
func TestClientErrorsDoNotLeakExternalDetails(t *testing.T) {
	const secret = "внутренняя-подробность-системы-расчёта"

	client := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(secret))
	})

	_, err := client.OrderAccrual(t.Context(), newOrderNumber(t))
	if err == nil {
		t.Fatal("ошибка внешней системы не сообщена")
	}

	if strings.Contains(err.Error(), secret) {
		t.Errorf("ошибка переносит тело ответа внешней системы: %v", err)
	}
}
