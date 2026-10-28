package domain_test

import (
	"errors"
	"strings"
	"testing"

	"gophermart/internal/domain"
)

func TestParseOrderStatusAcceptsWholeDictionary(t *testing.T) {
	tests := map[string]domain.OrderStatus{
		"NEW":        domain.OrderStatusNew,
		"PROCESSING": domain.OrderStatusProcessing,
		"INVALID":    domain.OrderStatusInvalid,
		"PROCESSED":  domain.OrderStatusProcessed,
	}

	for raw, want := range tests {
		t.Run(raw, func(t *testing.T) {
			got, err := domain.ParseOrderStatus(raw)
			if err != nil {
				t.Fatalf("разбор состояния %q: %v", raw, err)
			}

			if got != want {
				t.Errorf("неожиданное состояние: got %s, want %s", got, want)
			}

			if got.String() != raw {
				t.Errorf("строковое представление изменилось: got %s, want %s", got.String(), raw)
			}
		})
	}
}

func TestParseOrderStatusRejectsForeignValue(t *testing.T) {
	for _, raw := range []string{"", "new", "PROCESSED ", "DONE", "УСПЕХ"} {
		if _, err := domain.ParseOrderStatus(raw); !errors.Is(err, domain.ErrUnknownOrderStatus) {
			t.Errorf("значение %q принято как состояние расчёта: %v", raw, err)
		}
	}
}

func TestParseOrderNumberAcceptsValidNumber(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want domain.OrderNumber
	}{
		{name: "номер без окружения", raw: "9278923470", want: "9278923470"},
		{name: "номер с завершающим переводом строки", raw: "9278923470\n", want: "9278923470"},
		{name: "номер с возвратом каретки", raw: "9278923470\r\n", want: "9278923470"},
		{name: "номер с окружающими пробелами", raw: "  12345678903\t ", want: "12345678903"},
		{name: "номер с ведущими нулями", raw: "0000000018", want: "0000000018"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := domain.ParseOrderNumber(test.raw)
			if err != nil {
				t.Fatalf("разбор номера %q: %v", test.raw, err)
			}

			if got != test.want {
				t.Errorf("номер изменился при разборе: got %q, want %q", got, test.want)
			}

			if got.String() != string(test.want) {
				t.Errorf("строковое представление изменилось: got %q, want %q", got.String(), test.want)
			}
		})
	}
}

func TestParseOrderNumberRejectsInvalidValue(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "пустое значение", raw: ""},
		{name: "только пробельные символы", raw: " \n\t"},
		{name: "не проходит алгоритм Луна", raw: "9278923471"},
		{name: "буквы вместо цифр", raw: "заказ"},
		{name: "цифры с буквой", raw: "12345678a03"},
		{name: "внутренний пробел", raw: "1234 5678903"},
		{name: "внутренний перевод строки", raw: "12345\n678903"},
		{name: "знак перед цифрами", raw: "+12345678903"},
		{name: "дробное значение", raw: "1234567890.3"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := domain.ParseOrderNumber(test.raw)
			if !errors.Is(err, domain.ErrInvalidOrderNumber) {
				t.Fatalf("значение %q принято как номер заказа: %v", test.raw, err)
			}

			if got != "" {
				t.Errorf("при отказе возвращён непустой номер: %q", got)
			}
		})
	}
}

// TestParseOrderNumberIsOnlyWayToBuildNumber закрепляет, что тип номера
// невозможно наполнить в обход разбора без явного приведения типа: значение,
// полученное разбором, всегда состоит из цифр.
func TestParseOrderNumberIsOnlyWayToBuildNumber(t *testing.T) {
	number, err := domain.ParseOrderNumber("  1234567812345678123456781234567812345678  ")
	if err != nil {
		t.Fatalf("разбор длинного номера: %v", err)
	}

	if strings.ContainsFunc(number.String(), func(r rune) bool { return r < '0' || r > '9' }) {
		t.Errorf("разобранный номер содержит нецифровой символ: %q", number)
	}
}

// TestOrderUploadNamesEveryOutcome закрепляет читаемое имя у каждого исхода
// загрузки: имя попадает в журнал и в сообщения тестов, поэтому безымянных
// значений у перечисления быть не должно.
func TestOrderUploadNamesEveryOutcome(t *testing.T) {
	tests := map[domain.OrderUpload]string{
		domain.OrderUploadAccepted:  "принят",
		domain.OrderUploadDuplicate: "уже загружен",
		domain.OrderUploadUnknown:   "не определён",
		domain.OrderUpload(42):      "не определён",
	}

	for outcome, want := range tests {
		if got := outcome.String(); got != want {
			t.Errorf("неожиданное имя исхода %d: got %q, want %q", int(outcome), got, want)
		}
	}
}
