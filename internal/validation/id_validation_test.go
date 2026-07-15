package validation

import (
	"strings"
	"testing"
)

func TestValidateNodeID(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		wantErr bool
	}{
		{name: "UUID", id: "550e8400-e29b-41d4-a716-446655440000"},
		{name: "simple name", id: "node1"},
		{name: "underscore and hyphen", id: "test_node-001"},
		{name: "empty", id: "", wantErr: true},
		{name: "slash", id: "a/b", wantErr: true},
		{name: "dot segments", id: "..", wantErr: true},
		{name: "space", id: "node 1", wantErr: true},
		{name: "too long", id: strings.Repeat("a", 65), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateNodeID(tt.id)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateNodeID(%q) error = %v, wantErr %v", tt.id, err, tt.wantErr)
			}
		})
	}
}

func TestValidateUserID(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		wantErr bool
	}{
		{name: "valid", id: "1001"},
		{name: "maximum uint32", id: "4294967295"},
		{name: "empty", id: "", wantErr: true},
		{name: "slash", id: "1/2", wantErr: true},
		{name: "dot segments", id: "..", wantErr: true},
		{name: "space", id: "1 2", wantErr: true},
		{name: "non numeric", id: "abc", wantErr: true},
		{name: "leading zero", id: "01", wantErr: true},
		{name: "zero", id: "0", wantErr: true},
		{name: "uint32 overflow", id: "4294967296", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateUserID(tt.id)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateUserID(%q) error = %v, wantErr %v", tt.id, err, tt.wantErr)
			}
		})
	}
}
