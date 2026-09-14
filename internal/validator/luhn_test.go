package validator_test

import (
	"strings"
	"testing"

	"gophermart/internal/validator"
)

// digitCount — число десятичных цифр и, значит, возможных контрольных цифр.
const digitCount = 10

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

// TestLuhnDoesNotAllocate закрепляет обещание doc-комментария: проверка не
// выделяет память ни при принятии, ни при отказе, ни на нецифровом символе.
func TestLuhnDoesNotAllocate(t *testing.T) {
	for _, digits := range []string{"9278923470", "9278923471", "12345678a03"} {
		allocs := testing.AllocsPerRun(100, func() {
			_ = validator.Luhn(digits)
		})

		if allocs != 0 {
			t.Errorf("проверка номера %q выделяет память: %.0f выделений на вызов", digits, allocs)
		}
	}
}

// FuzzLuhn проверяет два свойства алгоритма, не зависящие от способа
// вычисления.
//
// Для любой строки цифр ровно одна из десяти контрольных цифр делает номер
// валидным: контрольная цифра однозначно дополняет сумму до кратной десяти.
// Строка, содержащая символ, отличный от десятичной цифры ASCII, валидной не
// бывает.
//
// Произвольный вход фаззера почти никогда не состоит из одних цифр, поэтому
// для первого свойства он отображается в строку цифр побайтно.
func FuzzLuhn(f *testing.F) {
	seeds := []string{
		"9278923470", "12345678903", "18", "0", "0000000018",
		"1234567812345678123456781234567812345678",
		"", "9278923471", "abcd", "12345678a03", "1234 5678903", "-12345678903", "１２３",
	}

	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, input string) {
		if !onlyASCIIDigits(input) && validator.Luhn(input) {
			t.Errorf("строка %q с нецифровым символом принята", input)
		}

		payload := digitsFrom(input)

		valid := 0

		for _, check := range "0123456789" {
			if validator.Luhn(payload + string(check)) {
				valid++
			}
		}

		if valid != 1 {
			t.Errorf("для %q валидных контрольных цифр: %d, ожидалась ровно одна", payload, valid)
		}
	})
}

// BenchmarkLuhn измеряет проверку номеров характерной длины: номера заказа из
// технического задания, номера банковской карты и длинного номера.
func BenchmarkLuhn(b *testing.B) {
	benchmarks := []struct {
		name   string
		digits string
	}{
		{name: "10 цифр", digits: "9278923470"},
		{name: "16 цифр", digits: "4561261212345467"},
		{name: "40 цифр", digits: "1234567812345678123456781234567812345678"},
	}

	for _, benchmark := range benchmarks {
		b.Run(benchmark.name, func(b *testing.B) {
			b.ReportAllocs()

			for b.Loop() {
				_ = validator.Luhn(benchmark.digits)
			}
		})
	}
}

// onlyASCIIDigits сообщает, состоит ли строка только из десятичных цифр ASCII.
// Пустая строка удовлетворяет условию.
func onlyASCIIDigits(input string) bool {
	for i := range len(input) {
		if input[i] < '0' || input[i] > '9' {
			return false
		}
	}

	return true
}

// digitsFrom отображает каждый байт строки в десятичную цифру ASCII: так любой
// вход фаззера превращается в номер, к которому применимо свойство
// контрольной цифры.
func digitsFrom(input string) string {
	digits := make([]byte, len(input))

	for i := range len(input) {
		digits[i] = '0' + input[i]%digitCount
	}

	return string(digits)
}
