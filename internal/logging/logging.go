package logging

import (
	"context"
	"io"
	"log/slog"
)

// Форматы вывода журнала.
const (
	// FormatJSON — структурные записи в формате JSON, пригодные для машинной
	FormatJSON = "json"

	// FormatText — человекочитаемые записи в формате key=value.
	FormatText = "text"
)

// Имена атрибутов, общие для всего сервиса.
const (
	// AttrRequestID — идентификатор HTTP-запроса.
	AttrRequestID = "request_id"

	// AttrError — текст ошибки.
	AttrError = "error"
)

// loggerContextKey — тип ключа, под которым логгер хранится в контексте.
type loggerContextKey struct{}

// New создаёт логгер, пишущий в w записи не ниже уровня level.
func New(w io.Writer, level slog.Level, format string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: level}

	var handler slog.Handler
	if format == FormatText {
		handler = slog.NewTextHandler(w, opts)
	} else {
		handler = slog.NewJSONHandler(w, opts)
	}

	return slog.New(handler)
}

// ContextWithLogger возвращает контекст, в котором сохранён логгер.
func ContextWithLogger(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerContextKey{}, logger)
}

// FromContext извлекает логгер из контекста.
func FromContext(ctx context.Context) *slog.Logger {
	if logger, ok := ctx.Value(loggerContextKey{}).(*slog.Logger); ok && logger != nil {
		return logger
	}

	return slog.New(slog.DiscardHandler)
}

// ErrorAttr возвращает атрибут журнала с текстом ошибки.
func ErrorAttr(err error) slog.Attr {
	if err == nil {
		return slog.Attr{}
	}

	return slog.String(AttrError, err.Error())
}
