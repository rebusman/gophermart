package validator_test

import (
	"strings"
	"testing"

	"gophermart/internal/validator"
)

func TestLuhnAcceptsValidNumbers(t *testing.T) {
	tests := []struct {
		name   string
		digits string
	}{
		{name: "номер из примера технического задания", digits: "9278923470"},
		{name: "номер из контракта API", digits: "12345678903"},
		{name: "короткий номер из двух цифр", digits: "18"},
		{name: "номер из одного нуля", digits: "0"},
		{name: "номер с ведущими нулями", digits: "0000000018"},
		{name: "длинный номер из 40 цифр", digits: "1234567812345678123456781234567812345678"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if !validator.Luhn(test.digits) {
				t.Errorf("номер %q отвергнут, ожидалось принятие", test.digits)
			}
		})
	}
}

func TestLuhnRejectsInvalidNumbers(t *testing.T) {
	tests := []struct {
		name   string
		digits string
	}{
		{name: "пустая строка", digits: ""},
		{name: "контрольная цифра не сходится", digits: "9278923471"},
		{name: "номер из одной ненулевой цифры", digits: "1"},
		{name: "буквы вместо цифр", digits: "abcd"},
		{name: "цифры с буквой", digits: "12345678a03"},
		{name: "цифры с внутренним пробелом", digits: "1234 5678903"},
		{name: "цифры со знаком", digits: "-12345678903"},
		{name: "нецифровые символы вне ASCII", digits: "１２３"},
		{name: "длинный номер с несходящейся суммой", digits: strings.Repeat("1", 41)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if validator.Luhn(test.digits) {
				t.Errorf("номер %q принят, ожидался отказ", test.digits)
			}
		})
	}
}

// TestLuhnIgnoresLeadingZeros закрепляет значимость ведущих нулей: они
// участвуют в подсчёте, но не меняют исход проверки, потому что добавляют к
// сумме ноль.
func TestLuhnIgnoresLeadingZeros(t *testing.T) {
	const base = "9278923470"

	for _, prefix := range []string{"", "0", "00", "000"} {
		digits := prefix + base
		if !validator.Luhn(digits) {
			t.Errorf("номер %q отвергнут, ожидалось принятие", digits)
		}
	}
}
