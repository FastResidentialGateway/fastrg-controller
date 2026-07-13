package kafka

import (
	"errors"
	"testing"

	"fastrg-controller/internal/storage"
)

func TestIsEtcdUnavailable(t *testing.T) {
	databaseErr := wrapDatabaseError(errors.New("database unavailable"))
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "wrapped etcd error",
			err:  wrapEtcdError(errors.New("connection refused")),
			want: true,
		},
		{
			name: "wrapped CAS conflict",
			err:  wrapEtcdError(storage.ErrCASConflict),
			want: true,
		},
		{
			name: "bare error",
			err:  errors.New("connection refused"),
			want: false,
		},
		{
			name: "database operation error",
			err:  databaseErr,
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
			if got := isEtcdUnavailable(tt.err); got != tt.want {
				t.Fatalf("isEtcdUnavailable() = %v, want %v", got, tt.want)
			}
		})
	}

	if !isDatabaseUnavailable(databaseErr) {
		t.Fatal("database operation error should retain database-unavailable classification")
	}
	if got := wrapEtcdError(nil); got != nil {
		t.Fatalf("wrapEtcdError(nil) = %v, want nil", got)
	}
}
