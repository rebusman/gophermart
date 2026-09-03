package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"gophermart/internal/logging"
)

// BackgroundTask описывает фоновую задачу, живущую столько же, сколько сервис.
type BackgroundTask struct {
	// Name — имя задачи, используемое в журнале.
	Name string

	// Run выполняет задачу до отмены контекста.
	Run func(ctx context.Context) error
}

// App владеет HTTP-сервером и фоновыми задачами сервиса и управляет их
type App struct {
	logger          *slog.Logger
	server          *http.Server
	listener        net.Listener
	tasks           []BackgroundTask
	shutdownTimeout time.Duration
}

// New создаёт приложение, готовое обслуживать запросы обработчиком handler.
func New(ctx context.Context, cfg Config, logger *slog.Logger, handler http.Handler) (*App, error) {
	var listenConfig net.ListenConfig

	listener, err := listenConfig.Listen(ctx, "tcp", cfg.RunAddress)
	if err != nil {
		return nil, fmt.Errorf("открытие сокета %s: %w", cfg.RunAddress, err)
	}

	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	return &App{
		logger:          logger,
		server:          server,
		listener:        listener,
		shutdownTimeout: cfg.ShutdownTimeout,
	}, nil
}

// Addr возвращает фактический адрес прослушивания.
func (a *App) Addr() string {
	return a.listener.Addr().String()
}

// AddBackgroundTask регистрирует фоновую задачу.
func (a *App) AddBackgroundTask(task BackgroundTask) {
	a.tasks = append(a.tasks, task)
}

// Serve запускает фоновые задачи и HTTP-сервер и работает до отмены ctx либо
func (a *App) Serve(ctx context.Context) error {
	tasksCtx, stopTasks := context.WithCancel(context.WithoutCancel(ctx))
	defer stopTasks()

	tasksErr := a.startTasks(tasksCtx)
	serverErr := a.startServer(ctx)

	var runErr error

	select {
	case runErr = <-serverErr:
	case <-ctx.Done():
		a.logger.InfoContext(ctx, "получен сигнал остановки, завершаем работу")
	}

	shutdownErr := a.shutdown()
	stopTasks()

	return errors.Join(runErr, shutdownErr, <-tasksErr)
}

// Close освобождает ресурсы приложения, которое не было запущено.
func (a *App) Close() error {
	if err := a.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		return fmt.Errorf("закрытие сокета: %w", err)
	}

	return nil
}

// startServer запускает HTTP-сервер и возвращает канал с результатом его
func (a *App) startServer(ctx context.Context) <-chan error {
	result := make(chan error, 1)

	go func() {
		a.logger.InfoContext(ctx, "HTTP-сервер запущен", slog.String("address", a.Addr()))

		err := a.server.Serve(a.listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			result <- fmt.Errorf("работа HTTP-сервера: %w", err)

			return
		}

		result <- nil
	}()

	return result
}

// startTasks запускает зарегистрированные фоновые задачи и возвращает канал, в
func (a *App) startTasks(ctx context.Context) <-chan error {
	result := make(chan error, 1)
	errs := make([]error, len(a.tasks))

	var wg sync.WaitGroup

	for i, task := range a.tasks {
		wg.Go(func() {
			a.logger.InfoContext(ctx, "фоновая задача запущена", slog.String("task", task.Name))

			if err := task.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				errs[i] = fmt.Errorf("фоновая задача %s: %w", task.Name, err)
			}
		})
	}

	go func() {
		wg.Wait()
		result <- errors.Join(errs...)
	}()

	return result
}

// shutdown останавливает HTTP-сервер, давая активным запросам завершиться.
func (a *App) shutdown() error {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), a.shutdownTimeout)
	defer cancel()

	if err := a.server.Shutdown(shutdownCtx); err != nil {
		a.logger.Error("HTTP-сервер остановлен принудительно", logging.ErrorAttr(err))

		return fmt.Errorf("остановка HTTP-сервера: %w", err)
	}

	return nil
}
