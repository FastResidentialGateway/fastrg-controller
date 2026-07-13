package kafka

import (
	"errors"
	"testing"

	"fastrg-controller/internal/db"
)

func TestGuardedRollbackMutation(t *testing.T) {
	config := func(rv string) []byte {
		return []byte(`{"config":{"user_id":"2"},"metadata":{"resourceVersion":"` + rv + `"}}`)
	}
	previous := func(rv string) *db.HSIConfigRow {
		return &db.HSIConfigRow{ConfigJSON: config(rv)}
	}

	tests := []struct {
		name       string
		previous   *db.HSIConfigRow
		current    []byte
		wantSkip   bool
		wantWarn   bool
		wantDelete bool
		wantValue  string
	}{
		{name: "missing current", previous: previous("1"), current: nil, wantSkip: true},
		{name: "malformed current JSON", previous: previous("1"), current: []byte(`{"metadata":`), wantSkip: true, wantWarn: true},
		{name: "current missing metadata", previous: previous("1"), current: []byte(`{"config":{}}`), wantSkip: true, wantWarn: true},
		{name: "current non-numeric resource version", previous: previous("1"), current: config("abc"), wantSkip: true, wantWarn: true},
		{name: "previous missing metadata", previous: &db.HSIConfigRow{ConfigJSON: []byte(`{"config":{}}`)}, current: config("2"), wantSkip: true, wantWarn: true},
		{name: "one write after success rolls back", previous: previous("1"), current: config("2"), wantValue: string(config("1"))},
		{name: "two writes after success skips", previous: previous("1"), current: config("3"), wantSkip: true},
		{name: "already at successful version skips", previous: previous("1"), current: config("1"), wantSkip: true},
		{name: "current older than success skips", previous: previous("3"), current: config("2"), wantSkip: true},
		{name: "first failed create is deleted", previous: nil, current: config("1"), wantDelete: true},
		{name: "superseded first create skips", previous: nil, current: config("2"), wantSkip: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := guardedRollbackMutation(tt.previous, tt.current)
			if tt.wantSkip {
				if !errors.Is(err, errRollbackSuperseded) {
					t.Fatalf("error = %v, want errRollbackSuperseded", err)
				}
				var skipErr *rollbackSkipError
				if !errors.As(err, &skipErr) {
					t.Fatalf("error type = %T, want *rollbackSkipError", err)
				}
				if skipErr.warn != tt.wantWarn {
					t.Fatalf("warn = %v, want %v", skipErr.warn, tt.wantWarn)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Delete != tt.wantDelete {
				t.Fatalf("Delete = %v, want %v", result.Delete, tt.wantDelete)
			}
			if string(result.Value) != tt.wantValue {
				t.Fatalf("Value = %q, want %q", result.Value, tt.wantValue)
			}
		})
	}
}
