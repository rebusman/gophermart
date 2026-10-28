package dto

import (
	"bytes"
	"fmt"

	"github.com/shopspring/decimal"
)

// jsonNull — представление отсутствующего значения в JSON.
const jsonNull = "null"

// Money — денежное значение в теле HTTP-запроса или ответа.
type Money struct {
	decimal.Decimal
}

// NewMoney создаёт денежное значение DTO из доменного decimal.
func NewMoney(value decimal.Decimal) Money {
	return Money{Decimal: value}
}

// MoneyPtr возвращает указатель на денежное значение для подстановки в
func MoneyPtr(value decimal.Decimal) *Money {
	money := NewMoney(value)

	return &money
}

// MarshalJSON сериализует сумму как JSON-число без кавычек.
func (m Money) MarshalJSON() ([]byte, error) {
	return []byte(m.String()), nil
}

// UnmarshalJSON разбирает сумму, принимая как JSON-число, так и строку.
func (m *Money) UnmarshalJSON(data []byte) error {
	if string(data) == jsonNull {
		return nil
	}

	text := string(bytes.Trim(data, `"`))

	value, err := decimal.NewFromString(text)
	if err != nil {
		return fmt.Errorf("разбор денежного значения %s: %w", data, err)
	}

	m.Decimal = value

	return nil
}
