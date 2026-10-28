package postgres

import (
	"errors"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/shopspring/decimal"
)

// Ошибки преобразования денежных значений.
var (
	// ErrNumericNull возвращается при попытке преобразовать SQL NULL в
	ErrNumericNull = errors.New("числовое значение равно NULL")

	// ErrNumericNotFinite возвращается для значений NaN и бесконечностей,
	ErrNumericNotFinite = errors.New("числовое значение не является конечным")
)

// NumericFromDecimal преобразует денежное значение домена в тип драйвера
func NumericFromDecimal(value decimal.Decimal) pgtype.Numeric {
	return pgtype.Numeric{
		Int:   value.Coefficient(),
		Exp:   value.Exponent(),
		Valid: true,
	}
}

// NumericFromDecimalPtr преобразует необязательное денежное значение в тип
func NumericFromDecimalPtr(value *decimal.Decimal) pgtype.Numeric {
	if value == nil {
		return pgtype.Numeric{}
	}

	return NumericFromDecimal(*value)
}

// DecimalFromNumeric преобразует значение, прочитанное из PostgreSQL, в
func DecimalFromNumeric(value pgtype.Numeric) (decimal.Decimal, error) {
	if !value.Valid {
		return decimal.Decimal{}, ErrNumericNull
	}

	if value.NaN || value.InfinityModifier != pgtype.Finite {
		return decimal.Decimal{}, ErrNumericNotFinite
	}

	return decimal.NewFromBigInt(value.Int, value.Exp), nil
}

// DecimalPtrFromNumeric преобразует значение, прочитанное из PostgreSQL, в
func DecimalPtrFromNumeric(value pgtype.Numeric) (*decimal.Decimal, error) {
	if !value.Valid {
		return nil, nil //nolint:nilnil // отсутствие значения — это не ошибка: NULL отображается в nil.
	}

	converted, err := DecimalFromNumeric(value)
	if err != nil {
		return nil, err
	}

	return &converted, nil
}
