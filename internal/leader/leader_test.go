package leader

import (
	"os"
	"testing"
)

func TestIdentityPrefersPodName(t *testing.T) {
	t.Setenv("POD_NAME", "fastrg-controller-abc123")
	if got := Identity(); got != "fastrg-controller-abc123" {
		t.Fatalf("Identity() = %q, want pod name", got)
	}
}

func TestIdentityFallsBackToHostname(t *testing.T) {
	// Ensure POD_NAME is unset for this case.
	t.Setenv("POD_NAME", "")
	host, _ := os.Hostname()
	got := Identity()
	if host != "" {
		if got != host {
			t.Fatalf("Identity() = %q, want hostname %q", got, host)
		}
	} else if got != "unknown" {
		t.Fatalf("Identity() = %q, want %q", got, "unknown")
	}
}
