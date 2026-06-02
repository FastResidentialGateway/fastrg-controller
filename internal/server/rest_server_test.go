package server

import "testing"

func TestNextResourceVersion(t *testing.T) {
	tests := []struct {
		name    string
		current string // raw stored value; "" means key absent (nil bytes)
		want    string
	}{
		{name: "absent key starts at 1", current: "", want: "1"},
		{name: "increments existing version", current: `{"metadata":{"resourceVersion":"3"}}`, want: "4"},
		{name: "unparseable json falls back to 2", current: `not json`, want: "2"},
		{name: "missing version field falls back to 2", current: `{"metadata":{}}`, want: "2"},
		{name: "non-numeric version falls back to 2", current: `{"metadata":{"resourceVersion":"abc"}}`, want: "2"},
		{name: "works for subscriber count value shape", current: `{"metadata":{"resourceVersion":"9"},"subscriber_count":"100"}`, want: "10"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var current []byte
			if tt.current != "" {
				current = []byte(tt.current)
			}
			if got := nextResourceVersion(current); got != tt.want {
				t.Fatalf("nextResourceVersion(%q) = %q, want %q", tt.current, got, tt.want)
			}
		})
	}
}
