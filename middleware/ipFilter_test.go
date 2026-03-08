package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// ── IPFilter ──────────────────────────────────────────────────────────────────

func ipRequest(mw func(http.Handler) http.Handler, remoteAddr string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = remoteAddr
	rec := httptest.NewRecorder()
	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)
	return rec
}

func TestIPFilter_EmptyLists_Passthrough(t *testing.T) {
	mw := IPFilter(IPFilterConfig{})
	rec := ipRequest(mw, "1.2.3.4:1234")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 (no-op), got %d", rec.Code)
	}
}

func TestIPFilter_Allowlist_PermitsListed(t *testing.T) {
	mw := IPFilter(IPFilterConfig{Allowlist: []string{"10.0.0.0/8"}})
	rec := ipRequest(mw, "10.1.2.3:5000")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
}

func TestIPFilter_Allowlist_DeniesUnlisted(t *testing.T) {
	mw := IPFilter(IPFilterConfig{Allowlist: []string{"10.0.0.0/8"}})
	rec := ipRequest(mw, "192.168.1.1:5000")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", rec.Code)
	}
}

func TestIPFilter_Blocklist_DeniesListed(t *testing.T) {
	mw := IPFilter(IPFilterConfig{Blocklist: []string{"203.0.113.42"}})
	rec := ipRequest(mw, "203.0.113.42:1234")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", rec.Code)
	}
}

func TestIPFilter_Blocklist_PermitsOthers(t *testing.T) {
	mw := IPFilter(IPFilterConfig{Blocklist: []string{"203.0.113.42"}})
	rec := ipRequest(mw, "1.1.1.1:1234")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
}

func TestIPFilter_AllowlistAndBlocklist_BlocklistHasNoEffect_IfAllowlistFails(t *testing.T) {
	// IP not in allowlist → 403 before even reaching blocklist.
	mw := IPFilter(IPFilterConfig{
		Allowlist: []string{"10.0.0.0/8"},
		Blocklist: []string{"10.0.0.1"},
	})
	rec := ipRequest(mw, "172.16.0.1:9999")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403 (not in allowlist), got %d", rec.Code)
	}
}

func TestIPFilter_AllowlistAndBlocklist_BlocklistDeniesAllowedIP(t *testing.T) {
	mw := IPFilter(IPFilterConfig{
		Allowlist: []string{"10.0.0.0/8"},
		Blocklist: []string{"10.0.0.5"},
	})
	rec := ipRequest(mw, "10.0.0.5:1234")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403 (in allowlist but also blocked), got %d", rec.Code)
	}
}

func TestIPFilter_BareIP_Allowlist(t *testing.T) {
	// Bare IPs (no CIDR) should be auto-appended /32.
	mw := IPFilter(IPFilterConfig{Allowlist: []string{"192.168.1.100"}})
	rec := ipRequest(mw, "192.168.1.100:9000")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	rec2 := ipRequest(mw, "192.168.1.101:9000")
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", rec2.Code)
	}
}

func TestIPFilter_TrustForwardedFor(t *testing.T) {
	mw := IPFilter(IPFilterConfig{
		Allowlist:         []string{"10.0.0.0/8"},
		TrustForwardedFor: true,
		TrustedProxies:    []string{"127.0.0.1"}, // direct-connection address of the proxy
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:1234" // proxy itself
	req.Header.Set("X-Forwarded-For", "10.5.5.5, 127.0.0.1")
	rec := httptest.NewRecorder()
	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 (XFF trusted, IP in allowlist), got %d", rec.Code)
	}
}

func TestIPFilter_TrustForwardedFor_Denied(t *testing.T) {
	mw := IPFilter(IPFilterConfig{
		Allowlist:         []string{"10.0.0.0/8"},
		TrustForwardedFor: true,
		TrustedProxies:    []string{"127.0.0.1"}, // direct-connection address of the proxy
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "8.8.8.8")
	rec := httptest.NewRecorder()
	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403 (XFF IP not in allowlist), got %d", rec.Code)
	}
}
