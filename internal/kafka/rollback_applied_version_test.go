package kafka

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"fastrg-controller/internal/db"
)

func TestGuardedRollbackMutationAppliedResourceVersion(t *testing.T) {
	config := func(rv, vlan string) []byte {
		return []byte(
			`{"config":{"user_id":"2","vlan_id":"` + vlan +
				`"},"metadata":{"node":"node-1","resourceVersion":"` + rv +
				`","updatedBy":"test","updatedAt":"2026-07-23T00:00:00Z"}}`,
		)
	}
	previous := func(rv string) *db.HSIConfigRow {
		return &db.HSIConfigRow{ConfigJSON: config(rv, "100")}
	}

	tests := []struct {
		name         string
		previous     *db.HSIConfigRow
		current      []byte
		modRevision  int64
		appliedRV    string
		correlation  string
		wantSkip     bool
		wantWarn     bool
		wantDelete   bool
		wantRollback bool
	}{
		{
			name:         "matching applied version and ModRevision rolls back",
			previous:     previous("4"),
			current:      config("5", "900"),
			modRevision:  42,
			appliedRV:    "5",
			correlation:  "42",
			wantRollback: true,
		},
		{
			name:         "empty correlation tolerates resourceVersion-only decision",
			previous:     previous("4"),
			current:      config("5", "900"),
			modRevision:  42,
			appliedRV:    "5",
			wantRollback: true,
		},
		{
			name:        "newer current resourceVersion skips",
			previous:    previous("4"),
			current:     config("6", "900"),
			modRevision: 43,
			appliedRV:   "5",
			correlation: "42",
			wantSkip:    true,
		},
		{
			name:        "invalid applied resourceVersion fails closed",
			previous:    previous("4"),
			current:     config("5", "900"),
			modRevision: 42,
			appliedRV:   "not-a-number",
			correlation: "42",
			wantSkip:    true,
			wantWarn:    true,
		},
		{
			name:         "empty applied resourceVersion preserves transitional allow",
			previous:     previous("4"),
			current:      config("5", "900"),
			modRevision:  42,
			correlation:  "999",
			wantRollback: true,
		},
		{
			name:        "empty applied resourceVersion preserves transitional skip",
			previous:    previous("4"),
			current:     config("6", "900"),
			modRevision: 43,
			wantSkip:    true,
		},
		{
			// Regression for the delete+recreate corner of the old
			// resourceVersion-only guard, which would delete this recreated rv=1
			// value.
			name:        "matching resourceVersion from a recreated generation skips",
			previous:    nil,
			current:     config("1", "901"),
			modRevision: 202,
			appliedRV:   "1",
			correlation: "101",
			wantSkip:    true,
		},
		{
			name:         "invalid correlation is tolerated",
			previous:     previous("4"),
			current:      config("5", "900"),
			modRevision:  42,
			appliedRV:    "5",
			correlation:  "not-a-revision",
			wantRollback: true,
		},
		{
			name:        "first failed create with matching guards is deleted",
			previous:    nil,
			current:     config("1", "900"),
			modRevision: 42,
			appliedRV:   "1",
			correlation: "42",
			wantDelete:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := guardedRollbackMutation(tt.previous, tt.current, rollbackGuardInputs{
				currentModRevision: tt.modRevision,
				appliedRV:          tt.appliedRV,
				correlationID:      tt.correlation,
			})
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
			if !tt.wantRollback {
				return
			}

			var got struct {
				Config   map[string]any `json:"config"`
				Metadata configMetadata `json:"metadata"`
			}
			if err := json.Unmarshal(result.Value, &got); err != nil {
				t.Fatalf("decode rollback value: %v", err)
			}
			wantPayload := map[string]any{"user_id": "2", "vlan_id": "100"}
			if !reflect.DeepEqual(got.Config, wantPayload) {
				t.Fatalf("payload = %#v, want restored payload %#v", got.Config, wantPayload)
			}
			if got.Metadata.ResourceVersion != "6" {
				t.Fatalf("resourceVersion = %q, want current+1 = 6", got.Metadata.ResourceVersion)
			}
			if got.Metadata.UpdatedBy != rollbackUpdatedBy {
				t.Fatalf("updatedBy = %q, want %q", got.Metadata.UpdatedBy, rollbackUpdatedBy)
			}
		})
	}
}
