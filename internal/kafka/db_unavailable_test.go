package kafka

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestIsDatabaseUnavailableSQLStateClassification(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "cannot connect now",
			err:  wrapDatabaseError(&pgconn.PgError{Code: "57P03"}),
			want: true,
		},
		{
			name: "too many connections",
			err:  wrapDatabaseError(&pgconn.PgError{Code: "53300"}),
			want: true,
		},
		{
			name: "serialization failure",
			err:  wrapDatabaseError(&pgconn.PgError{Code: "40001"}),
			want: true,
		},
		{
			name: "deadlock detected",
			err:  wrapDatabaseError(&pgconn.PgError{Code: "40P01"}),
			want: true,
		},
		{
			name: "admin shutdown",
			err:  wrapDatabaseError(&pgconn.PgError{Code: "57P01"}),
			want: true,
		},
		{
			name: "disk full",
			err:  wrapDatabaseError(&pgconn.PgError{Code: "53100"}),
			want: true,
		},
		{
			name: "connection failure",
			err:  wrapDatabaseError(&pgconn.PgError{Code: "08006"}),
			want: true,
		},
		{
			name: "unique violation",
			err:  wrapDatabaseError(&pgconn.PgError{Code: "23505"}),
			want: false,
		},
		{
			name: "undefined table",
			err:  wrapDatabaseError(&pgconn.PgError{Code: "42P01"}),
			want: false,
		},
		{
			name: "invalid text representation",
			err:  wrapDatabaseError(&pgconn.PgError{Code: "22P02"}),
			want: false,
		},
		{
			name: "transaction integrity constraint violation",
			err:  wrapDatabaseError(&pgconn.PgError{Code: "40002"}),
			want: false,
		},
		{
			name: "wrapped non PostgreSQL error",
			err:  wrapDatabaseError(errors.New("connection refused")),
			want: true,
		},
		{
			name: "bare error",
			err:  errors.New("connection refused"),
			want: false,
		},
		{
			name: "bare PostgreSQL error",
			err:  &pgconn.PgError{Code: "57P03"},
			want: false,
		},
		{
			name: "nil",
			err:  nil,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isDatabaseUnavailable(tt.err); got != tt.want {
				t.Fatalf("isDatabaseUnavailable() = %v, want %v", got, tt.want)
			}
		})
	}
}
