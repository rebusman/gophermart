package app_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
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
