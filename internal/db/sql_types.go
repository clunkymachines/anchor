package db

import (
	"context"
	"database/sql"
)

type nullableString = sql.NullString
type nullableBool = sql.NullBool
type nullableInt64 = sql.NullInt64

type txRunner interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}
