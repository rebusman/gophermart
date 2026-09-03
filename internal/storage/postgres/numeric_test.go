package postgres_test

import (
	"errors"
	"math/big"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/shopspring/decimal"

	"gophermart/internal/storage/postgres"
)

func TestNumericDecimalRoundTrip(t *testing.T) {
	tests := map[string]string{
		"ноль": "0",
		"два знака после запятой": "751.50",
		"один знак":               "751.5",
		"копейка":                 "0.01",
		"крупное значение":        "9999999999999999.99",
		"отрицательное значение":  "-42.75",
	}

	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			original := decimal.RequireFromString(raw)

			restored, err := postgres.DecimalFromNumeric(postgres.NumericFromDecimal(original))
			if err != nil {
				t.Fatalf("обратное преобразование: %v", err)
			}

			if !restored.Equal(original) {
				t.Errorf("значение изменилось: got %s, want %s", restored, original)
			}
		})
	}
}

func TestNumericFromDecimalIsValid(t *testing.T) {
	numeric := postgres.NumericFromDecimal(decimal.RequireFromString("10.25"))
	if !numeric.Valid {
		t.Error("преобразованное значение помечено как NULL")
	}
}

func TestDecimalFromNumericRejectsNull(t *testing.T) {
	_, err := postgres.DecimalFromNumeric(pgtype.Numeric{})
	if !errors.Is(err, postgres.ErrNumericNull) {
		t.Errorf("NULL не распознан: %v", err)
	}
}

func TestDecimalFromNumericRejectsNaN(t *testing.T) {
	_, err := postgres.DecimalFromNumeric(pgtype.Numeric{NaN: true, Valid: true})
	if !errors.Is(err, postgres.ErrNumericNotFinite) {
		t.Errorf("NaN не распознан: %v", err)
	}
}

func TestDecimalFromNumericRejectsInfinity(t *testing.T) {
	value := pgtype.Numeric{
		Int:              big.NewInt(1),
		InfinityModifier: pgtype.Infinity,
		Valid:            true,
	}

	if _, err := postgres.DecimalFromNumeric(value); !errors.Is(err, postgres.ErrNumericNotFinite) {
		t.Errorf("бесконечность не распознана: %v", err)
	}
}

func TestNullableConversions(t *testing.T) {
	if got := postgres.NumericFromDecimalPtr(nil); got.Valid {
		t.Error("nil должен преобразовываться в NULL")
	}

	restored, err := postgres.DecimalPtrFromNumeric(pgtype.Numeric{})
	if err != nil {
		t.Fatalf("преобразование NULL: %v", err)
	}

	if restored != nil {
		t.Errorf("NULL должен преобразовываться в nil: got %v", restored)
	}
}

func TestNullableRoundTrip(t *testing.T) {
	original := decimal.RequireFromString("500.00")

	restored, err := postgres.DecimalPtrFromNumeric(postgres.NumericFromDecimalPtr(&original))
	if err != nil {
		t.Fatalf("обратное преобразование: %v", err)
	}

	if restored == nil {
		t.Fatal("заданное значение потеряно")
	}

	if !restored.Equal(original) {
		t.Errorf("значение изменилось: got %s, want %s", restored, original)
	}
}

func TestNullableConversionPropagatesError(t *testing.T) {
	_, err := postgres.DecimalPtrFromNumeric(pgtype.Numeric{NaN: true, Valid: true})
	if !errors.Is(err, postgres.ErrNumericNotFinite) {
		t.Errorf("ошибка не доведена до вызывающей стороны: %v", err)
	}
}
