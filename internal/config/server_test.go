package config

import "testing"

func TestGetTCPMaxConns(t *testing.T) {
	tests := []struct {
		name string
		env  string
		set  bool
		want int
	}{
		{name: "unset returns default", set: false, want: DefaultTCPMaxConns},
		{name: "empty returns default", env: "", set: true, want: DefaultTCPMaxConns},
		{name: "valid override", env: "500", set: true, want: 500},
		{name: "zero returns default", env: "0", set: true, want: DefaultTCPMaxConns},
		{name: "negative returns default", env: "-1", set: true, want: DefaultTCPMaxConns},
		{name: "non-numeric returns default", env: "abc", set: true, want: DefaultTCPMaxConns},
		{name: "one is honoured", env: "1", set: true, want: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.set {
				t.Setenv("TCP_MAX_CONNS", tt.env)
			} else {
				// Ensure any ambient value is cleared for this subtest.
				t.Setenv("TCP_MAX_CONNS", "")
			}
			if got := GetTCPMaxConns(); got != tt.want {
				t.Fatalf("GetTCPMaxConns() = %d, want %d", got, tt.want)
			}
		})
	}
}
