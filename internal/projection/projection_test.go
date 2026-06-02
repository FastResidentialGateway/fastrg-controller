package projection

import "testing"

func TestParseHSIKey(t *testing.T) {
	tests := []struct {
		key      string
		wantNode string
		wantUser string
		wantOK   bool
	}{
		{key: "configs/node1/hsi/2", wantNode: "node1", wantUser: "2", wantOK: true},
		{key: "configs/abc-uuid/hsi/100", wantNode: "abc-uuid", wantUser: "100", wantOK: true},
		{key: "configs/node1/2/dns", wantOK: false},       // DNS key
		{key: "configs/node1/user_count", wantOK: false},  // user_count key
		{key: "configs/node1/hsi", wantOK: false},         // too short
		{key: "nodes/node1", wantOK: false},               // wrong prefix
		{key: "configs/node1/hsi/2/extra", wantOK: false}, // too long
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			node, user, ok := parseHSIKey(tt.key)
			if ok != tt.wantOK || node != tt.wantNode || user != tt.wantUser {
				t.Fatalf("parseHSIKey(%q) = (%q,%q,%v), want (%q,%q,%v)",
					tt.key, node, user, ok, tt.wantNode, tt.wantUser, tt.wantOK)
			}
		})
	}
}

func TestBuildRow(t *testing.T) {
	value := []byte(`{"config":{"user_id":"2","desire_status":"connect"},"metadata":{"resourceVersion":"7","updatedBy":"admin","updatedAt":"2026-06-02T10:00:00Z"}}`)
	row := buildRow("node1", "2", value, 99)

	if row.NodeUUID != "node1" || row.UserID != "2" || row.ModRevision != 99 {
		t.Fatalf("identity fields wrong: %+v", row)
	}
	if row.DesireStatus != "connect" {
		t.Fatalf("DesireStatus = %q, want connect", row.DesireStatus)
	}
	if row.ResourceVersion != "7" || row.UpdatedBy != "admin" {
		t.Fatalf("metadata extraction wrong: %+v", row)
	}
	if row.UpdatedAt == nil || row.UpdatedAt.Year() != 2026 {
		t.Fatalf("UpdatedAt not parsed: %v", row.UpdatedAt)
	}
	if string(row.ConfigJSON) != string(value) {
		t.Fatal("ConfigJSON should keep the full value verbatim")
	}
}

func TestBuildRowDefaultsDesireStatus(t *testing.T) {
	// Legacy / unparseable value: desire_status defaults to disconnect, full
	// value still stored.
	value := []byte(`not json`)
	row := buildRow("node1", "2", value, 5)
	if row.DesireStatus != "disconnect" {
		t.Fatalf("DesireStatus = %q, want disconnect", row.DesireStatus)
	}
	if row.UpdatedAt != nil {
		t.Fatalf("UpdatedAt = %v, want nil", row.UpdatedAt)
	}

	// Valid envelope but no desire_status -> default.
	value2 := []byte(`{"config":{"user_id":"2"},"metadata":{"resourceVersion":"1"}}`)
	row2 := buildRow("node1", "2", value2, 6)
	if row2.DesireStatus != "disconnect" {
		t.Fatalf("DesireStatus(empty) = %q, want disconnect", row2.DesireStatus)
	}
	if row2.ResourceVersion != "1" {
		t.Fatalf("ResourceVersion = %q, want 1", row2.ResourceVersion)
	}
}
