package dto_test

import (
	"encoding/json"
	"testing"

	"github.com/shopspring/decimal"

	"gophermart/internal/transport/http/dto"
)

// order повторяет форму ответа с необязательным денежным полем: поле-указатель
// исчезает из JSON, когда значение не задано.
type order struct {
	Number  string     `json:"number"`
	Accrual *dto.Money `json:"accrual,omitempty"`
}

// orderByValue объявляет то же поле значением и служит контрпримером: тег
// omitempty не действует на структуры, поэтому поле остаётся в JSON.
type orderByValue struct {
	Number string `json:"number"`
	//nolint:modernize // Намеренный контрпример: omitempty не действует на структуру.
	Accrual dto.Money `json:"accrual,omitempty"`
}

func TestOptionalMoneyOmittedWhenNil(t *testing.T) {
	encoded, err := json.Marshal(order{Number: "12345678903"})
	if err != nil {
		t.Fatalf("сериализация: %v", err)
	}

	if got, want := string(encoded), `{"number":"12345678903"}`; got != want {
		t.Errorf("незаданное поле попало в JSON: got %s, want %s", got, want)
	}
}

func TestOptionalMoneySerializedAsNumber(t *testing.T) {
	value := decimal.RequireFromString("751.5")

	encoded, err := json.Marshal(order{Number: "12345678903", Accrual: dto.MoneyPtr(value)})
	if err != nil {
		t.Fatalf("сериализация: %v", err)
	}

	if got, want := string(encoded), `{"number":"12345678903","accrual":751.5}`; got != want {
		t.Errorf("неожиданное представление суммы: got %s, want %s", got, want)
	}
}

func TestMoneyByValueIsAlwaysSerialized(t *testing.T) {
	encoded, err := json.Marshal(orderByValue{Number: "12345678903"})
	if err != nil {
		t.Fatalf("сериализация: %v", err)
	}

	if got, want := string(encoded), `{"number":"12345678903","accrual":0}`; got != want {
		t.Errorf("omitempty неожиданно сработал для структуры: got %s, want %s", got, want)
	}
}

func TestMoneyUnmarshalAcceptsNumberAndString(t *testing.T) {
	tests := map[string]struct {
		input string
		want  string
	}{
		"число":               {input: `{"accrual":751.5}`, want: "751.5"},
		"целое число":         {input: `{"accrual":500}`, want: "500"},
		"строка":              {input: `{"accrual":"751.5"}`, want: "751.5"},
		"много знаков":        {input: `{"accrual":0.01}`, want: "0.01"},
		"большое значение":    {input: `{"accrual":9999999999999999.99}`, want: "9999999999999999.99"},
		"отрицательное число": {input: `{"accrual":-12.34}`, want: "-12.34"},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			var decoded order
			if err := json.Unmarshal([]byte(test.input), &decoded); err != nil {
				t.Fatalf("разбор: %v", err)
			}

			if decoded.Accrual == nil {
				t.Fatal("поле не разобрано")
			}

			if got := decoded.Accrual.String(); got != test.want {
				t.Errorf("неожиданное значение: got %s, want %s", got, test.want)
			}
		})
	}
}

func TestMoneyUnmarshalRejectsGarbage(t *testing.T) {
	var decoded order
	if err := json.Unmarshal([]byte(`{"accrual":"не число"}`), &decoded); err == nil {
		t.Error("ожидалась ошибка разбора нечислового значения")
	}
}

func TestMoneyRoundTrip(t *testing.T) {
	original := order{Number: "12345678903", Accrual: dto.MoneyPtr(decimal.RequireFromString("0.01"))}

	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("сериализация: %v", err)
	}

	var decoded order
	if err = json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("разбор: %v", err)
	}

	if !decoded.Accrual.Equal(original.Accrual.Decimal) {
		t.Errorf("значение изменилось: got %s, want %s", decoded.Accrual, original.Accrual)
	}
}
