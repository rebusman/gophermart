package postgres

import (
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// ErrUUIDNull возвращается при попытке преобразовать SQL NULL в идентификатор,
var ErrUUIDNull = errors.New("идентификатор равен NULL")

// UUIDFromGoogle преобразует идентификатор в тип драйвера PostgreSQL.
func UUIDFromGoogle(id uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: id, Valid: true}
}

// GoogleFromUUID преобразует идентификатор, прочитанный из PostgreSQL.
func GoogleFromUUID(value pgtype.UUID) (uuid.UUID, error) {
	if !value.Valid {
		return uuid.Nil, ErrUUIDNull
	}

	return value.Bytes, nil
}
