package app_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"gophermart/internal/app"
)

// testConfig возвращает конфигурацию, пригодную для запуска приложения в тесте:
// порт 0 отдаёт свободный порт, а таймауты укорочены, чтобы тест не ждал.
func testConfig() app.Config {
	return app.Config{
		RunAddress:          "127.0.0.1:0",
		ShutdownTimeout:     2 * time.Second,
		ReadHeaderTimeout:   200 * time.Millisecond,
		ReadTimeout:         200 * time.Millisecond,
		WriteTimeout:        2 * time.Second,
		IdleTimeout:         time.Second,
		MaxRequestBodyBytes: 1024,
	}
}

// startApp запускает приложение и возвращает его вместе с функцией остановки,
// дожидающейся полного завершения.
func startApp(t *testing.T, application *app.App) func() error {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)

	go func() {
		done <- application.Serve(ctx)
	}()

	waitReady(t, application.Addr())

	return func() error {
		cancel()

		select {
		case err := <-done:
			return err
		case <-time.After(5 * time.Second):
			return errors.New("приложение не остановилось за отведённое время")
		}
	}
}

// waitReady дожидается, пока сокет начнёт принимать соединения.
func waitReady(t *testing.T, addr string) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()

			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("сокет %s не принимает соединения", addr)
}

func TestServeShutsDownWithoutActiveRequests(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	application, err := app.New(t.Context(), testConfig(), discardLogger(), handler)
	if err != nil {
		t.Fatalf("создание приложения: %v", err)
	}

	stop := startApp(t, application)

	if err = stop(); err != nil {
		t.Errorf("остановка вернула ошибку: %v", err)
	}
}

func TestServeLetsActiveRequestFinish(t *testing.T) {
	const handlerDelay = 300 * time.Millisecond

	started := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		time.Sleep(handlerDelay)
		w.WriteHeader(http.StatusTeapot)
	})

	application, err := app.New(t.Context(), testConfig(), discardLogger(), handler)
	if err != nil {
		t.Fatalf("создание приложения: %v", err)
	}

	stop := startApp(t, application)
	statuses := make(chan int, 1)
	failures := make(chan error, 1)

	// Тело ответа закрывается в породившей запрос горутине, а наружу уходит
	// только код состояния: ответ, дошедший до буфера канала после того, как
	// select ушёл в ветку ошибки или таймаута, иначе не закрыл бы никто.
	go func() {
		resp, reqErr := http.Get("http://" + application.Addr() + "/")
		if reqErr != nil {
			failures <- reqErr

			return
		}

		defer func() {
			_ = resp.Body.Close()
		}()

		statuses <- resp.StatusCode
	}()

	<-started

	stopErr := stop()

	select {
	case status := <-statuses:
		if status != http.StatusTeapot {
			t.Errorf("активный запрос не завершился штатно: got %d, want %d", status, http.StatusTeapot)
		}
	case reqErr := <-failures:
		t.Errorf("активный запрос оборван при остановке: %v", reqErr)
	case <-time.After(5 * time.Second):
		t.Error("ответ на активный запрос не получен")
	}

	if stopErr != nil {
		t.Errorf("остановка вернула ошибку: %v", stopErr)
	}
}

func TestServeRunsAndStopsBackgroundTask(t *testing.T) {
	taskStarted := make(chan struct{})
	taskStopped := make(chan struct{})

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	application, err := app.New(t.Context(), testConfig(), discardLogger(), handler)
	if err != nil {
		t.Fatalf("создание приложения: %v", err)
	}

	application.AddBackgroundTask(app.BackgroundTask{
		Name: "проверочная задача",
		Run: func(ctx context.Context) error {
			close(taskStarted)
			<-ctx.Done()
			close(taskStopped)

			return ctx.Err()
		},
	})

	stop := startApp(t, application)

	select {
	case <-taskStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("фоновая задача не запустилась")
	}

	if err = stop(); err != nil {
		t.Errorf("остановка вернула ошибку: %v", err)
	}

	select {
	case <-taskStopped:
	case <-time.After(time.Second):
		t.Error("фоновая задача не остановилась вместе с приложением")
	}
}

func TestServeReportsBackgroundTaskFailure(t *testing.T) {
	failure := errors.New("задача не смогла выполниться")

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	application, err := app.New(t.Context(), testConfig(), discardLogger(), handler)
	if err != nil {
		t.Fatalf("создание приложения: %v", err)
	}

	application.AddBackgroundTask(app.BackgroundTask{
		Name: "падающая задача",
		Run: func(context.Context) error {
			return failure
		},
	})

	stop := startApp(t, application)

	if err = stop(); !errors.Is(err, failure) {
		t.Errorf("ошибка фоновой задачи не доведена до вызывающей стороны: %v", err)
	}
}

func TestServerClosesIdleRequestByTimeout(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	application, err := app.New(t.Context(), testConfig(), discardLogger(), handler)
	if err != nil {
		t.Fatalf("создание приложения: %v", err)
	}

	stop := startApp(t, application)

	conn, err := net.Dial("tcp", application.Addr())
	if err != nil {
		t.Fatalf("подключение: %v", err)
	}

	defer func() {
		_ = conn.Close()
	}()

	// Запрос намеренно не завершён: отсутствует пустая строка после заголовков.
	if _, err = fmt.Fprint(conn, "GET / HTTP/1.1\r\nHost: localhost\r\n"); err != nil {
		t.Fatalf("отправка неполного запроса: %v", err)
	}

	if err = conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("установка срока чтения: %v", err)
	}

	if _, err = bufio.NewReader(conn).ReadByte(); err == nil {
		t.Error("сервер не закрыл соединение с незавершённым запросом")
	}

	if err = stop(); err != nil {
		t.Errorf("остановка вернула ошибку: %v", err)
	}
}

func TestNewFailsOnBusyAddress(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("занятие порта: %v", err)
	}

	defer func() {
		_ = listener.Close()
	}()

	cfg := testConfig()
	cfg.RunAddress = listener.Addr().String()

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	if _, err = app.New(t.Context(), cfg, discardLogger(), handler); err == nil {
		t.Error("ожидалась ошибка занятого адреса")
	}
}

const (
	// panicTaskName — имя задачи, паникующей в тестах изоляции сбоя.
	panicTaskName = "паникующая задача"

	// panicMessage — значение, которым паникует такая задача.
	panicMessage = "фоновая задача не смогла продолжить работу"
)

// panickingBackgroundTask возвращает задачу, паникующую сразу после запуска и
// закрывающую started перед этим.
func panickingBackgroundTask(started chan struct{}) app.BackgroundTask {
	return app.BackgroundTask{
		Name: panicTaskName,
		Run: func(context.Context) error {
			close(started)

			panic(panicMessage)
		},
	}
}

func TestServeSurvivesBackgroundTaskPanic(t *testing.T) {
	const responseBody = "ответ маршрута"

	started := make(chan struct{})

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)

		_, _ = io.WriteString(w, responseBody)
	})

	application, err := app.New(t.Context(), testConfig(), discardLogger(), handler)
	if err != nil {
		t.Fatalf("создание приложения: %v", err)
	}

	application.AddBackgroundTask(panickingBackgroundTask(started))

	stop := startApp(t, application)

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("фоновая задача не запустилась")
	}

	resp, err := http.Get("http://" + application.Addr() + "/")
	if err != nil {
		t.Fatalf("сервис перестал отвечать после аварии фоновой задачи: %v", err)
	}

	body, readErr := io.ReadAll(resp.Body)

	if closeErr := resp.Body.Close(); closeErr != nil {
		t.Errorf("закрытие тела ответа: %v", closeErr)
	}

	if readErr != nil {
		t.Fatalf("чтение тела ответа: %v", readErr)
	}

	if resp.StatusCode != http.StatusTeapot {
		t.Errorf("маршрут ответил не своим кодом: got %d, want %d", resp.StatusCode, http.StatusTeapot)
	}

	if string(body) != responseBody {
		t.Errorf("тело ответа содержит посторонние сведения: got %q, want %q", body, responseBody)
	}

	// Ошибка остановки здесь ожидаема и проверяется отдельным тестом.
	_ = stop()
}

func TestServeReportsBackgroundTaskPanic(t *testing.T) {
	started := make(chan struct{})

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	application, err := app.New(t.Context(), testConfig(), discardLogger(), handler)
	if err != nil {
		t.Fatalf("создание приложения: %v", err)
	}

	application.AddBackgroundTask(panickingBackgroundTask(started))

	stop := startApp(t, application)

	stopErr := stop()

	if !errors.Is(stopErr, app.ErrBackgroundTaskPanic) {
		t.Errorf("авария фоновой задачи не доведена до вызывающей стороны: %v", stopErr)
	}

	if stopErr == nil || !strings.Contains(stopErr.Error(), panicTaskName) {
		t.Errorf("ошибка не называет аварийно завершившуюся задачу: %v", stopErr)
	}
}

func TestServeKeepsRemainingTasksAfterPanic(t *testing.T) {
	survivors := map[string]chan struct{}{
		"первая уцелевшая задача": make(chan struct{}),
		"вторая уцелевшая задача": make(chan struct{}),
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	application, err := app.New(t.Context(), testConfig(), discardLogger(), handler)
	if err != nil {
		t.Fatalf("создание приложения: %v", err)
	}

	application.AddBackgroundTask(panickingBackgroundTask(make(chan struct{})))

	for name, stopped := range survivors {
		application.AddBackgroundTask(waitingBackgroundTask(name, stopped))
	}

	stop := startApp(t, application)

	// Ошибка остановки здесь ожидаема: её содержание проверяется отдельно.
	_ = stop()

	for name, stopped := range survivors {
		select {
		case <-stopped:
		default:
			t.Errorf("задача %q не завершилась штатно после аварии соседней", name)
		}
	}
}

func TestServeLogsBackgroundTaskPanic(t *testing.T) {
	started := make(chan struct{})
	logger, logs := bufferLogger()

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	application, err := app.New(t.Context(), testConfig(), logger, handler)
	if err != nil {
		t.Fatalf("создание приложения: %v", err)
	}

	application.AddBackgroundTask(panickingBackgroundTask(started))

	stop := startApp(t, application)

	// Ошибка остановки здесь ожидаема: её содержание проверяется отдельно.
	_ = stop()

	var found bool

	for line := range strings.SplitSeq(strings.TrimSpace(logs.String()), "\n") {
		var record map[string]any

		if err = json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("запись журнала не является JSON: %v", err)
		}

		if record["level"] != slog.LevelError.String() {
			continue
		}

		found = true

		if record["task"] != panicTaskName {
			t.Errorf("запись об аварии называет не ту задачу: %v", record["task"])
		}

		if value, ok := record["panic"].(string); !ok || value != panicMessage {
			t.Errorf("в записи нет значения паники: %v", record["panic"])
		}

		if stack, ok := record["stack"].(string); !ok || stack == "" {
			t.Error("в записи нет стека вызовов")
		}
	}

	if !found {
		t.Errorf("авария фоновой задачи не записана в журнал: %s", logs.String())
	}
}

func TestServeReportsEveryFailedBackgroundTask(t *testing.T) {
	failures := []struct {
		name string
		err  error
	}{
		{name: "задача альфа", err: errors.New("альфа не смогла выполниться")},
		{name: "задача бета", err: nil},
		{name: "задача гамма", err: errors.New("гамма не смогла выполниться")},
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	application, err := app.New(t.Context(), testConfig(), discardLogger(), handler)
	if err != nil {
		t.Fatalf("создание приложения: %v", err)
	}

	for _, failure := range failures {
		application.AddBackgroundTask(app.BackgroundTask{
			Name: failure.name,
			Run: func(context.Context) error {
				return failure.err
			},
		})
	}

	stop := startApp(t, application)

	stopErr := stop()
	if stopErr == nil {
		t.Fatal("ошибки фоновых задач не доведены до вызывающей стороны")
	}

	for _, failure := range failures {
		if failure.err == nil {
			if strings.Contains(stopErr.Error(), failure.name) {
				t.Errorf("успешная задача %q попала в результат: %v", failure.name, stopErr)
			}

			continue
		}

		if !errors.Is(stopErr, failure.err) {
			t.Errorf("ошибка задачи %q потеряна: %v", failure.name, stopErr)
		}

		want := fmt.Sprintf("фоновая задача %s: %s", failure.name, failure.err)
		if !strings.Contains(stopErr.Error(), want) {
			t.Errorf("ошибка не привязана к своей задаче, ожидалось %q: %v", want, stopErr)
		}
	}
}

func TestServeReportsOnlyFailedBackgroundTask(t *testing.T) {
	const failedName = "падающая задача"

	failure := errors.New("задача не смогла выполниться")
	healthy := []string{"первая исправная задача", "вторая исправная задача"}

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	application, err := app.New(t.Context(), testConfig(), discardLogger(), handler)
	if err != nil {
		t.Fatalf("создание приложения: %v", err)
	}

	// Падающая задача регистрируется между исправными: так проверяется, что
	// ошибка попадает в результат по индексу именно своей задачи.
	application.AddBackgroundTask(waitingBackgroundTask(healthy[0], make(chan struct{})))
	application.AddBackgroundTask(app.BackgroundTask{
		Name: failedName,
		Run: func(context.Context) error {
			return failure
		},
	})
	application.AddBackgroundTask(waitingBackgroundTask(healthy[1], make(chan struct{})))

	stop := startApp(t, application)

	stopErr := stop()
	if stopErr == nil {
		t.Fatal("ошибка фоновой задачи не доведена до вызывающей стороны")
	}

	if !errors.Is(stopErr, failure) {
		t.Errorf("ошибка падающей задачи потеряна: %v", stopErr)
	}

	want := fmt.Sprintf("фоновая задача %s: %s", failedName, failure)
	if !strings.Contains(stopErr.Error(), want) {
		t.Errorf("ошибка не привязана к своей задаче, ожидалось %q: %v", want, stopErr)
	}

	for _, name := range healthy {
		if strings.Contains(stopErr.Error(), name) {
			t.Errorf("исправная задача %q попала в результат: %v", name, stopErr)
		}
	}
}

func TestServeReportsNoErrorWhenTasksStopByContext(t *testing.T) {
	names := []string{"первая задача", "вторая задача", "третья задача"}

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	application, err := app.New(t.Context(), testConfig(), discardLogger(), handler)
	if err != nil {
		t.Fatalf("создание приложения: %v", err)
	}

	stopped := make([]chan struct{}, len(names))

	for i, name := range names {
		stopped[i] = make(chan struct{})
		application.AddBackgroundTask(waitingBackgroundTask(name, stopped[i]))
	}

	stop := startApp(t, application)

	if err = stop(); err != nil {
		t.Errorf("штатная остановка задач вернула ошибку: %v", err)
	}

	for i, name := range names {
		select {
		case <-stopped[i]:
		default:
			t.Errorf("задача %q не завершилась при остановке сервиса", name)
		}
	}
}
