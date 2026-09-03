package app_test

import (
	"bytes"
	"log/slog"
	"testing"
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
