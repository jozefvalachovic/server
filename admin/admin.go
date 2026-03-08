// Package admin provides a lightweight, zero-dependency admin UI for Go HTTP
// servers built with github.com/jozefvalachovic/server. It exposes two
// password-protected sections:
//
//   - /metrics/ — real-time per-route request metrics (count, latency, error rate, bytes)
//   - /cache/   — cache statistics and a live data explorer with delete/flush actions
//
// Authentication is form-based. A signed HMAC-SHA256 session cookie
// (_admin_session) is valid for 8 hours. The cookie covers all sections;
// signing is keyed on ADMIN_SECRET so the server holds no session state.
//
// Environment variables required:
//
//	ADMIN_NAME    — admin username
//	ADMIN_SECRET  — admin password / HMAC signing key
//
// Usage:
//
//	col := admin.NewCollector()
//
//	srv, _ := server.NewHTTPServer(mux, appName, appVersion, server.HTTPServerConfig{
//	    Admin: &admin.Config{Collector: col, Store: cacheStore},
//	})
//
//	admin.Register(mux, admin.Config{
//	    AppName:    appName,
//	    AppVersion: appVersion,
//	    Collector:  col,
//	    Store:      cacheStore,
//	})
package admin

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/jozefvalachovic/server/cache"
	"github.com/jozefvalachovic/server/ui"
)

// Config holds all options for the admin UI.
type Config struct {
	// AppName is displayed in the admin page headers.
	AppName string
	// AppVersion is displayed alongside AppName.
	AppVersion string
	// Collector provides per-route request metrics. Create with NewCollector()
	// and pass to HTTPServerConfig.Admin.Collector so it is wired as middleware.
	Collector *Collector
	// Store is the application cache store. Used by the /cache/ section.
	// nil disables cache-related features.
	Store *cache.CacheStore
}

// adminCSP allows same-origin CSS and JS for the admin UI pages.
// It narrows the outer security middleware's strict "default-src 'none'" policy.
const adminCSP = "default-src 'none'; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'"

// withCSP overrides the Content-Security-Policy header for HTML admin pages.
func withCSP(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", adminCSP)
		h(w, r)
	}
}

// Register mounts all admin UI routes onto mux.
//
//	/admin/style.css  — shared stylesheet
//	/admin/script.js   — shared JavaScript
//	/metrics/auth     — login for the metrics section
//	/metrics/logout   — clear session, redirect to /metrics/auth
//	/metrics/         — metrics dashboard (protected)
//	/cache/auth       — login for the cache section
//	/cache/logout     — clear session, redirect to /cache/auth
//	/cache/           — cache dashboard (protected)
//	/cache/entry/{k}  — DELETE a single cache key (AJAX, protected)
//	/cache/flush      — POST to flush all cache keys (AJAX, protected)
func Register(mux *http.ServeMux, cfg Config) {
	// ── Static assets ────────────────────────────────────────────────────
	mux.HandleFunc("GET /admin/style.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write(ui.StyleCSS)
	})
	mux.HandleFunc("GET /admin/script.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write(adminJS)
	})
	// ── Metrics section ──────────────────────────────────────────────────
	metricsLoginGet, metricsLoginPost := loginHandler("Metrics", "/metrics/", func(w http.ResponseWriter, errMsg string) {
		renderAuth(w, authData{Section: "Metrics", Action: "/metrics/auth", Error: errMsg})
	})
	mux.HandleFunc("GET /metrics/auth", withCSP(metricsLoginGet))
	mux.HandleFunc("POST /metrics/auth", metricsLoginPost)
	mux.HandleFunc("GET /metrics/logout", logoutHandler("/metrics/auth"))

	metricsUI := requireAdmin(http.HandlerFunc(withCSP(func(w http.ResponseWriter, r *http.Request) {
		metricsPageHandler(cfg, w, r)
	})), "/metrics/auth")
	mux.Handle("/metrics/", metricsUI)

	// ── Cache section ────────────────────────────────────────────────────
	cacheLoginGet, cacheLoginPost := loginHandler("Cache", "/cache/", func(w http.ResponseWriter, errMsg string) {
		renderAuth(w, authData{Section: "Cache", Action: "/cache/auth", Error: errMsg})
	})
	mux.HandleFunc("GET /cache/auth", withCSP(cacheLoginGet))
	mux.HandleFunc("POST /cache/auth", cacheLoginPost)
	mux.HandleFunc("GET /cache/logout", logoutHandler("/cache/auth"))

	cacheMux := http.NewServeMux()
	cacheMux.HandleFunc("GET /cache/", withCSP(func(w http.ResponseWriter, r *http.Request) {
		cachePageHandler(cfg, w, r)
	}))
	cacheMux.HandleFunc("DELETE /cache/entry/{key}", func(w http.ResponseWriter, r *http.Request) {
		cacheDeleteHandler(cfg, w, r)
	})
	cacheMux.HandleFunc("POST /cache/flush", func(w http.ResponseWriter, r *http.Request) {
		cacheFlushHandler(cfg, w, r)
	})
	mux.Handle("/cache/", requireAdmin(cacheMux, "/cache/auth"))
}

// ── Handlers ─────────────────────────────────────────────────────────────────

func logoutHandler(authURL string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		clearSessionCookie(w)
		http.Redirect(w, r, authURL, http.StatusFound)
	}
}

func metricsPageHandler(cfg Config, w http.ResponseWriter, r *http.Request) {
	if cfg.Collector == nil {
		http.Error(w, "metrics collector not configured", http.StatusServiceUnavailable)
		return
	}
	totalReqs, total5xx, avgLat := cfg.Collector.Summary()
	snaps := cfg.Collector.Snapshots()

	type row struct {
		RouteSnapshot
		RowClass      string
		ErrorPct      float64
		FmtAvgLatency string
		FmtMinLatency string
		FmtMaxLatency string
	}
	rows := make([]row, 0, len(snaps))
	for _, s := range snaps {
		rc := ""
		pct := s.ErrorRate() * 100
		switch {
		case pct >= 5:
			rc = "row-err"
		case pct >= 1:
			rc = "row-warn"
		}
		rows = append(rows, row{
			RouteSnapshot: s,
			RowClass:      rc,
			ErrorPct:      pct,
			FmtAvgLatency: fmt.Sprintf("%.1f ms", s.AvgLatency),
			FmtMinLatency: fmt.Sprintf("%d ms", s.MinLatency),
			FmtMaxLatency: fmt.Sprintf("%d ms", s.MaxLatency),
		})
	}

	var errorPct float64
	if totalReqs > 0 {
		errorPct = float64(total5xx) / float64(totalReqs) * 100
	}

	renderMetrics(w, metricsData{
		AppName:    cfg.AppName,
		AppVersion: cfg.AppVersion,
		TotalReqs:  totalReqs,
		Total5xx:   total5xx,
		ErrorPct:   errorPct,
		AvgLatency: fmt.Sprintf("%.1f ms", avgLat),
		RouteCount: len(snaps),
		Routes:     rows,
	})
}

func cachePageHandler(cfg Config, w http.ResponseWriter, r *http.Request) {
	if cfg.Store == nil {
		http.Error(w, "cache store not configured", http.StatusServiceUnavailable)
		return
	}
	stats := cfg.Store.GetStats()
	exported := cfg.Store.Export()

	type entryRow struct {
		Key      string
		Type     string
		TTL      string
		Bytes    int64
		Expiring bool
	}
	entries := make([]entryRow, 0, len(exported))
	for k, v := range exported {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		ttlDur, _ := m["ttl"].(time.Duration)
		ttlStr := ttlDur.Round(time.Second).String()
		expiring := ttlDur < 30*time.Second

		val := m["value"]
		typeName := reflect.TypeOf(val).String()
		var bytesEst int64
		switch rv := val.(type) {
		case []byte:
			bytesEst = int64(len(rv))
		case string:
			bytesEst = int64(len(rv))
		default:
			raw, _ := json.Marshal(rv)
			bytesEst = int64(len(raw))
		}

		entries = append(entries, entryRow{
			Key:      k,
			Type:     typeName,
			TTL:      ttlStr,
			Bytes:    bytesEst,
			Expiring: expiring,
		})
	}

	renderCache(w, cacheData{
		AppName:      cfg.AppName,
		AppVersion:   cfg.AppVersion,
		Stats:        stats,
		HitRate:      hitRate(stats),
		BytesUsedFmt: fmtBytes(stats.BytesUsed),
		Entries:      entries,
	})
}

func cacheDeleteHandler(cfg Config, w http.ResponseWriter, r *http.Request) {
	if cfg.Store == nil {
		http.Error(w, "cache store not configured", http.StatusServiceUnavailable)
		return
	}
	key := r.PathValue("key")
	if key == "" {
		http.Error(w, "missing key", http.StatusBadRequest)
		return
	}
	cfg.Store.Delete(key)
	w.WriteHeader(http.StatusNoContent)
}

func cacheFlushHandler(cfg Config, w http.ResponseWriter, r *http.Request) {
	if cfg.Store == nil {
		http.Error(w, "cache store not configured", http.StatusServiceUnavailable)
		return
	}
	cfg.Store.Flush()
	w.WriteHeader(http.StatusNoContent)
}

// ── Template rendering ────────────────────────────────────────────────────────

var (
	//go:embed templates/auth.html
	authHTML []byte
	//go:embed templates/metrics.html
	metricsHTML []byte
	//go:embed templates/cache.html
	cacheHTML []byte
)

var (
	authTmpl    = template.Must(template.New("auth").Parse(string(authHTML)))
	metricsTmpl = template.Must(template.New("metrics").Parse(string(metricsHTML)))
	cacheTmpl   = template.Must(template.New("cache").Parse(string(cacheHTML)))
)

type authData struct {
	Section string
	Action  string
	Next    string
	Error   string
}

type metricsData struct {
	AppName    string
	AppVersion string
	TotalReqs  int64
	Total5xx   int64
	ErrorPct   float64
	AvgLatency string
	RouteCount int
	Routes     any
}

type cacheData struct {
	AppName      string
	AppVersion   string
	Stats        cache.CacheStats
	HitRate      string
	BytesUsedFmt string
	MemPct       float64
	MaxMemFmt    string
	MemBarClass  string
	Entries      any
}

func renderAuth(w http.ResponseWriter, d authData) {
	var buf bytes.Buffer
	if err := authTmpl.Execute(&buf, d); err != nil {
		http.Error(w, "render error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

func renderMetrics(w http.ResponseWriter, d metricsData) {
	var buf bytes.Buffer
	if err := metricsTmpl.Execute(&buf, d); err != nil {
		http.Error(w, "render error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

func renderCache(w http.ResponseWriter, d cacheData) {
	var buf bytes.Buffer
	if err := cacheTmpl.Execute(&buf, d); err != nil {
		http.Error(w, "render error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

// ── Helpers ──────────────────────────────────────────────────────────────────

func hitRate(s cache.CacheStats) string {
	total := s.Hits + s.Misses
	if total == 0 {
		return "—"
	}
	return trimFloat(float64(s.Hits)/float64(total)*100, 1)
}

func trimFloat(f float64, prec int) string {
	s := strconv.FormatFloat(f, 'f', prec, 64)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(s, "0")
		s = strings.TrimRight(s, ".")
	}
	return s
}

func fmtBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return trimFloat(float64(n)/(1<<30), 2) + " GB"
	case n >= 1<<20:
		return trimFloat(float64(n)/(1<<20), 2) + " MB"
	case n >= 1<<10:
		return trimFloat(float64(n)/(1<<10), 2) + " KB"
	default:
		return fmt.Sprintf("%d B", n)
	}
}

//go:embed templates/script.js
var adminJS []byte
