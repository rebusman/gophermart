package dto_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"gophermart/internal/transport/http/dto"
)

// TestOrderOmitsAccrualWhenNotCalculated закрепляет сценарий «Заказ без
// начисления»: поле отсутствует в JSON, а прочие поля присутствуют.
func TestOrderOmitsAccrualWhenNotCalculated(t *testing.T) {
	order := dto.NewOrder("9278923470", "NEW", nil, time.Date(2020, time.December, 10, 15, 15, 45, 0, time.UTC))

	encoded, err := json.Marshal(order)
	if err != nil {
		t.Fatalf("сериализация заказа: %v", err)
	}

	body := string(encoded)

	if strings.Contains(body, "accrual") {
		t.Errorf("поле начисления присутствует при нерассчитанном начислении: %s", body)
	}

	for _, field := range []string{`"number":"9278923470"`, `"status":"NEW"`, `"uploaded_at":`} {
		if !strings.Contains(body, field) {
			t.Errorf("в ответе отсутствует %s: %s", field, body)
		}
	}
}

// TestOrderEncodesAccrualAsNumber закрепляет сценарий «Заказ с начислением»:
// значение передаётся JSON-числом без кавычек и без искажения дробной части.
func TestOrderEncodesAccrualAsNumber(t *testing.T) {
	tests := map[string]string{
		"дробное значение":        "751.5",
		"два знака после запятой": "0.01",
		"ноль":          "0",
		"большая сумма": "123456789012345.67",
		"значение без дробной части": "500",
	}

	for name, value := range tests {
		t.Run(name, func(t *testing.T) {
			accrual := dto.MoneyPtr(decimal.RequireFromString(value))
			order := dto.NewOrder("9278923470", "PROCESSED", accrual, time.Now())

			encoded, err := json.Marshal(order)
			if err != nil {
				t.Fatalf("сериализация заказа: %v", err)
			}

			want := `"accrual":` + value
			if !strings.Contains(string(encoded), want) {
				t.Errorf("начисление сериализовано неверно: got %s, want подстроку %s", encoded, want)
			}
		})
	}
}

// TestOrderZeroAccrualDiffersFromAbsentAccrual закрепляет требование
// «Представление заказа в ответе»: значение 0 и отсутствие поля не
// эквивалентны.
func TestOrderZeroAccrualDiffersFromAbsentAccrual(t *testing.T) {
	uploadedAt := time.Date(2020, time.December, 10, 15, 15, 45, 0, time.UTC)

	zero, err := json.Marshal(dto.NewOrder("9278923470", "PROCESSED", dto.MoneyPtr(decimal.Zero), uploadedAt))
	if err != nil {
		t.Fatalf("сериализация заказа с нулевым начислением: %v", err)
	}

	absent, err := json.Marshal(dto.NewOrder("9278923470", "PROCESSED", nil, uploadedAt))
	if err != nil {
		t.Fatalf("сериализация заказа без начисления: %v", err)
	}

	if string(zero) == string(absent) {
		t.Errorf("нулевое начисление неотличимо от отсутствующего: %s", zero)
	}
}

// TestOrderFormatsUploadedAtInUTC закрепляет сценарий «Время загрузки»: время
// передаётся в формате RFC3339 и не зависит от часового пояса значения,
// переданного в DTO.
func TestOrderFormatsUploadedAtInUTC(t *testing.T) {
	moment := time.Date(2020, time.December, 10, 15, 15, 45, 0, time.UTC)
	zones := []*time.Location{
		time.UTC,
		time.FixedZone("UTC+3", 3*60*60),
		time.FixedZone("UTC-7", -7*60*60),
	}

	var encoded []string

	for _, zone := range zones {
		order := dto.NewOrder("9278923470", "NEW", nil, moment.In(zone))

		body, err := json.Marshal(order)
		if err != nil {
			t.Fatalf("сериализация заказа в зоне %s: %v", zone, err)
		}

		encoded = append(encoded, string(body))
	}

	for i := 1; i < len(encoded); i++ {
		if encoded[i] != encoded[0] {
			t.Errorf("представление зависит от часового пояса: %s против %s", encoded[i], encoded[0])
		}
	}

	if !strings.Contains(encoded[0], `"uploaded_at":"2020-12-10T15:15:45Z"`) {
		t.Errorf("время загрузки не в формате RFC3339 в UTC: %s", encoded[0])
	}
}

// TestOrderKeepsSubSecondPrecision закрепляет, что доли секунды не теряются:
// иначе две загрузки внутри одной секунды стали бы неразличимы, а требование
// «порядок значений времени соответствует порядку фактических загрузок»
// перестало бы проверяться.
func TestOrderKeepsSubSecondPrecision(t *testing.T) {
	first := time.Date(2020, time.December, 10, 15, 15, 45, 100_000_000, time.UTC)
	second := first.Add(time.Millisecond)

	encodedFirst, err := json.Marshal(dto.NewOrder("9278923470", "NEW", nil, first))
	if err != nil {
		t.Fatalf("сериализация первого заказа: %v", err)
	}

	encodedSecond, err := json.Marshal(dto.NewOrder("9278923470", "NEW", nil, second))
	if err != nil {
		t.Fatalf("сериализация второго заказа: %v", err)
	}

	if string(encodedFirst) == string(encodedSecond) {
		t.Errorf("доли секунды потеряны: %s", encodedFirst)
	}
}
