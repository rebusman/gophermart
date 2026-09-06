package app_test

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"testing"

	"gophermart/internal/app"
)

// renderAttr журналирует один атрибут и возвращает получившуюся запись.
//
// Помощник нужен тестам, проверяющим, что значение безопасно для журнала:
// проверяется именно то, что реально попадёт в вывод.
func renderAttr(t *testing.T, attr slog.Attr) string {
	t.Helper()

	var buf bytes.Buffer

	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	logger.Info("проверка", attr)

	return buf.String()
}

// discardLogger возвращает логгер, отбрасывающий записи.
func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// syncBuffer накапливает журнал приложения, работающего в отдельной goroutine,
// и допускает одновременное чтение из теста.
//
// bytes.Buffer для этого непригоден: одновременные запись логгером и чтение
// проверкой являются гонкой.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write добавляет данные в буфер.
func (b *syncBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(data)
}

// String возвращает снимок накопленного содержимого.
func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

// bufferLogger возвращает логгер, пишущий записи в формате JSON, и буфер, в
// который они попадают.
func bufferLogger() (*slog.Logger, *syncBuffer) {
	buf := &syncBuffer{}

	return slog.New(slog.NewJSONHandler(buf, nil)), buf
}

// waitingBackgroundTask возвращает фоновую задачу, работающую до отмены
// контекста и закрывающую stopped перед завершением.
//
// Помощник нужен тестам, проверяющим, что задача дожила до остановки сервиса:
// закрытый канал отличает штатное завершение от преждевременного выхода.
func waitingBackgroundTask(name string, stopped chan struct{}) app.BackgroundTask {
	return app.BackgroundTask{
		Name: name,
		Run: func(ctx context.Context) error {
			<-ctx.Done()
			close(stopped)

			return ctx.Err()
		},
	}
}
