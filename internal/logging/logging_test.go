package logging_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"gophermart/internal/logging"
)

func TestNewWritesJSONRecord(t *testing.T) {
	var buf bytes.Buffer

	logger := logging.New(&buf, slog.LevelInfo, logging.FormatJSON)
	logger.Info("сообщение", slog.String("ключ", "значение"))

	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatalf("вывод не является JSON: %v (%s)", err, buf.String())
	}

	if record["level"] != "INFO" {
		t.Errorf("неожиданный уровень: %v", record["level"])
	}

	if record["msg"] != "сообщение" {
		t.Errorf("неожиданное сообщение: %v", record["msg"])
	}

	if record["ключ"] != "значение" {
		t.Errorf("атрибут потерян: %v", record)
	}
}

func TestNewWritesTextRecord(t *testing.T) {
	var buf bytes.Buffer

	logger := logging.New(&buf, slog.LevelInfo, logging.FormatText)
	logger.Info("сообщение")

	output := buf.String()

	for _, want := range []string{"level=INFO", "msg=сообщение"} {
		if !strings.Contains(output, want) {
			t.Errorf("вывод не содержит %q: %s", want, output)
		}
	}
}

func TestNewRespectsLevel(t *testing.T) {
	var buf bytes.Buffer

	logger := logging.New(&buf, slog.LevelWarn, logging.FormatJSON)
	logger.Info("не должно попасть в вывод")

	if buf.Len() != 0 {
		t.Errorf("запись ниже порога попала в вывод: %s", buf.String())
	}

	logger.Warn("должно попасть в вывод")

	if buf.Len() == 0 {
		t.Error("запись уровня предупреждения не попала в вывод")
	}
}

func TestFromContextReturnsStoredLogger(t *testing.T) {
	var buf bytes.Buffer

	base := logging.New(&buf, slog.LevelInfo, logging.FormatJSON)
	stored := base.With(slog.String(logging.AttrRequestID, "abc-123"))

	ctx := logging.ContextWithLogger(t.Context(), stored)
	logging.FromContext(ctx).InfoContext(ctx, "запрос")

	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatalf("вывод не является JSON: %v (%s)", err, buf.String())
	}

	if record[logging.AttrRequestID] != "abc-123" {
		t.Errorf("логгер из контекста потерял идентификатор запроса: %v", record)
	}
}

func TestFromContextWithoutLogger(t *testing.T) {
	logger := logging.FromContext(t.Context())
	if logger == nil {
		t.Fatal("возвращён nil вместо логгера по умолчанию")
	}

	// Логгер по умолчанию должен быть работоспособен и не приводить к panic.
	logger.Info("запись отбрасывается")
}

func TestErrorAttr(t *testing.T) {
	if attr := logging.ErrorAttr(nil); !attr.Equal(slog.Attr{}) {
		t.Errorf("nil-ошибка не должна давать атрибут: %v", attr)
	}

	attr := logging.ErrorAttr(errSample)
	if attr.Key != logging.AttrError {
		t.Errorf("неожиданное имя атрибута: %s", attr.Key)
	}

	if attr.Value.String() != errSample.Error() {
		t.Errorf("неожиданное значение атрибута: %s", attr.Value)
	}
}
