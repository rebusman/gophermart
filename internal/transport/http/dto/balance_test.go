package dto_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"gophermart/internal/transport/http/dto"
)

// mustDecimal разбирает денежное значение из десятичной строки.
func mustDecimal(t *testing.T, value string) decimal.Decimal {
	t.Helper()

	parsed, err := decimal.NewFromString(value)
	if err != nil {
		t.Fatalf("разбор денежного значения %s: %v", value, err)
	}

	return parsed
}

// TestBalanceAlwaysCarriesBothSums закрепляет сценарий «Счёт без операций»:
// оба поля присутствуют в JSON всегда, а нулевой счёт даёт нули, а не
// отсутствующие поля.
func TestBalanceAlwaysCarriesBothSums(t *testing.T) {
	body, err := json.Marshal(dto.NewBalance(decimal.Zero, decimal.Zero))
	if err != nil {
		t.Fatalf("сериализация счёта: %v", err)
	}

	if got := string(body); got != `{"current":0,"withdrawn":0}` {
		t.Errorf("неожиданное представление нулевого счёта: %s", got)
	}
}

// TestBalanceSerializesFractionalSums закрепляет сценарий «Дробная сумма»:
// значения передаются JSON-числами без кавычек и без искажения.
func TestBalanceSerializesFractionalSums(t *testing.T) {
	body, err := json.Marshal(dto.NewBalance(mustDecimal(t, "500.5"), mustDecimal(t, "42")))
	if err != nil {
		t.Fatalf("сериализация счёта: %v", err)
	}

	got := string(body)

	if got != `{"current":500.5,"withdrawn":42}` {
		t.Errorf("неожиданное представление счёта: %s", got)
	}

	if strings.Contains(got, `"500.5"`) {
		t.Error("денежное значение передано строкой в кавычках")
	}
}

// TestBalanceKeepsPrecisionOnLargeSums закрепляет требование «Представление
// сумм счёта в ответе» на значении, которое потеряло бы точность в float64.
func TestBalanceKeepsPrecisionOnLargeSums(t *testing.T) {
	const large = "123456789012345.67"

	body, err := json.Marshal(dto.NewBalance(mustDecimal(t, large), decimal.Zero))
	if err != nil {
		t.Fatalf("сериализация счёта: %v", err)
	}

	if !strings.Contains(string(body), large) {
		t.Errorf("сумма искажена при сериализации: %s", body)
	}
}

// TestWithdrawRequestAcceptsNumberAndString закрепляет разбор суммы в обоих
// представлениях: клиент не обязан следовать спецификации запроса буквально.
func TestWithdrawRequestAcceptsNumberAndString(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "число", body: `{"order":"2377225624","sum":751.5}`},
		{name: "строка", body: `{"order":"2377225624","sum":"751.5"}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var request dto.WithdrawRequest

			if err := json.Unmarshal([]byte(test.body), &request); err != nil {
				t.Fatalf("разбор запроса: %v", err)
			}

			if request.Order != "2377225624" {
				t.Errorf("неожиданный номер заказа: got %q", request.Order)
			}

			if got := request.Sum.String(); got != "751.5" {
				t.Errorf("неожиданная сумма: got %s, want 751.5", got)
			}
		})
	}
}

// TestWithdrawRequestTreatsMissingSumAsZero закрепляет решение объявить сумму
// значением, а не указателем: отсутствующее поле неотличимо от явного нуля и
// приводит к тому же отказу.
func TestWithdrawRequestTreatsMissingSumAsZero(t *testing.T) {
	tests := map[string]string{
		"поле отсутствует": `{"order":"2377225624"}`,
		"поле равно null":  `{"order":"2377225624","sum":null}`,
		"поле равно нулю":  `{"order":"2377225624","sum":0}`,
	}

	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			var request dto.WithdrawRequest

			if err := json.Unmarshal([]byte(body), &request); err != nil {
				t.Fatalf("разбор запроса: %v", err)
			}

			if request.Sum.IsPositive() {
				t.Errorf("сумма признана положительной: %s", request.Sum)
			}
		})
	}
}

// TestWithdrawalSerializesAllFields закрепляет сценарий «Состав объекта
// списания»: номер, сумма и время присутствуют, сумма передаётся числом.
func TestWithdrawalSerializesAllFields(t *testing.T) {
	moment := time.Date(2020, time.December, 9, 16, 9, 57, 0, time.UTC)

	body, err := json.Marshal(dto.NewWithdrawal("2377225624", mustDecimal(t, "500"), moment))
	if err != nil {
		t.Fatalf("сериализация списания: %v", err)
	}

	want := `{"order":"2377225624","sum":500,"processed_at":"2020-12-09T16:09:57Z"}`
	if got := string(body); got != want {
		t.Errorf("неожиданное представление списания:\n got %s\nwant %s", got, want)
	}
}

// TestWithdrawalKeepsLeadingZeros закрепляет сценарий «Ведущие нули номера
// заказа»: номер переносится строкой без изменения.
func TestWithdrawalKeepsLeadingZeros(t *testing.T) {
	const number = "00000000000000"

	withdrawal := dto.NewWithdrawal(number, mustDecimal(t, "1"), time.Now())

	if withdrawal.Order != number {
		t.Errorf("номер заказа изменён: got %q, want %q", withdrawal.Order, number)
	}
}

// TestWithdrawalNormalizesTimeToUTC закрепляет сценарий «Время списания»:
// результат не зависит от часового пояса переданного значения.
func TestWithdrawalNormalizesTimeToUTC(t *testing.T) {
	zone := time.FixedZone("MSK", 3*60*60)
	moment := time.Date(2020, time.December, 9, 19, 9, 57, 0, zone)

	body, err := json.Marshal(dto.NewWithdrawal("2377225624", mustDecimal(t, "1"), moment))
	if err != nil {
		t.Fatalf("сериализация списания: %v", err)
	}

	if !strings.Contains(string(body), `"processed_at":"2020-12-09T16:09:57Z"`) {
		t.Errorf("время не приведено к UTC: %s", body)
	}
}
