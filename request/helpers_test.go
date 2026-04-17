package request

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetIPAddress_XForwardedFor_Single(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Forwarded-For", "203.0.113.50")
	if ip := GetIPAddress(r); ip != "203.0.113.50" {
		t.Fatalf("want 203.0.113.50, got %s", ip)
	}
}

func TestGetIPAddress_XForwardedFor_Multiple(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Forwarded-For", "203.0.113.50, 70.41.3.18, 150.172.238.178")
	if ip := GetIPAddress(r); ip != "203.0.113.50" {
		t.Fatalf("want first IP 203.0.113.50, got %s", ip)
	}
}

func TestGetIPAddress_XRealIP(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Real-IP", "10.0.0.1")
	if ip := GetIPAddress(r); ip != "10.0.0.1" {
		t.Fatalf("want 10.0.0.1, got %s", ip)
	}
}

func TestGetIPAddress_RemoteAddr(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "192.168.1.1:54321"
	if ip := GetIPAddress(r); ip != "192.168.1.1" {
		t.Fatalf("want 192.168.1.1, got %s", ip)
	}
}

func TestGetIPAddress_RemoteAddr_NoPort(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "192.168.1.1"
	// Header fields are empty, RemoteAddr has no port → SplitHostPort fails,
	// returns raw RemoteAddr.
	if ip := GetIPAddress(r); ip != "192.168.1.1" {
		t.Fatalf("want 192.168.1.1, got %s", ip)
	}
}

func TestGetIPAddress_XFF_TakesPrecedence(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Forwarded-For", "1.2.3.4")
	r.Header.Set("X-Real-IP", "5.6.7.8")
	r.RemoteAddr = "9.10.11.12:80"
	if ip := GetIPAddress(r); ip != "1.2.3.4" {
		t.Fatalf("X-Forwarded-For should take precedence, got %s", ip)
	}
}

func TestSanitizeEmail(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"  User@Example.COM  ", "user@example.com"},
		{"already@lower.com", "already@lower.com"},
		{"", ""},
		{"  ", ""},
	}
	for _, tc := range tests {
		got := SanitizeEmail(tc.input)
		if got != tc.want {
			t.Errorf("SanitizeEmail(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestValidateEmail_Valid(t *testing.T) {
	if err := ValidateEmail("user@example.com"); err != nil {
		t.Fatalf("valid email should pass: %v", err)
	}
}

func TestValidateEmail_Empty(t *testing.T) {
	err := ValidateEmail("")
	if err != ErrEmailRequired {
		t.Fatalf("want ErrEmailRequired, got %v", err)
	}
}

func TestValidateEmail_Whitespace(t *testing.T) {
	err := ValidateEmail("   ")
	if err != ErrEmailRequired {
		t.Fatalf("whitespace-only should return ErrEmailRequired, got %v", err)
	}
}

func TestValidateEmail_Invalid(t *testing.T) {
	cases := []string{
		"not-an-email",
		"@missing-local.com",
		"missing-domain@",
	}
	for _, tc := range cases {
		err := ValidateEmail(tc)
		if err != ErrEmailInvalid {
			t.Errorf("ValidateEmail(%q) = %v, want ErrEmailInvalid", tc, err)
		}
	}
}
