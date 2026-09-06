package validator

// decimalBase — основание десятичной системы счисления.
const decimalBase = 10

// Luhn сообщает, проходит ли последовательность цифр проверку алгоритмом
func Luhn(digits string) bool {
	if digits == "" {
		return false
	}

	var (
		sum    int
		double bool
	)

	for i := len(digits) - 1; i >= 0; i-- {
		char := digits[i]
		if char < '0' || char > '9' {
			return false
		}

		digit := int(char - '0')

		if double {
			digit *= 2
			if digit >= decimalBase {
				digit -= decimalBase - 1
			}
		}

		sum += digit
		double = !double
	}

	return sum%decimalBase == 0
}
