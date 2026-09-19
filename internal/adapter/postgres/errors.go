package postgres

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// redact strips driver internals so raw pgx/SQL errors never reach clients
// or cross the repository boundary as typed errors.
func redact(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return errors.New("postgres error " + pgErr.Code)
	}
	return errors.New("database error")
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func pgTime(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t.UTC(), Valid: true}
}

// parseUUID keeps one conversion helper for domain string IDs.
func parseUUID(s string) (pgtype.UUID, error) {
	u, err := uuid.Parse(s)
	if err != nil {
		return pgtype.UUID{}, err
	}
	var b [16]byte
	copy(b[:], u[:])
	return pgtype.UUID{Bytes: b, Valid: true}, nil
}

func uuidString(u pgtype.UUID) string {
	var arr uuid.UUID
	copy(arr[:], u.Bytes[:])
	return arr.String()
}
