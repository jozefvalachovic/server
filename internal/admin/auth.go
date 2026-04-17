package admin

import (
	"cmp"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"net"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	cookieName     = "_admin_session"
	csrfCookieName = "_csrf_token"
	sessionTTL     = 8 * time.Hour
	authRedirParam = "next"
)

// trustXFP is set by Register() during admin bootstrap to indicate whether
// X-Forwarded-Proto from the incoming request should be consulted when
// deciding if the session/CSRF cookies must be marked Secure. Default false
// (safe fallback: only r.TLS is trusted).
var trustXFP bool

// isSecureRequest reports whether the cookie Secure flag should be set.
// When trustXFP is true the X-Forwarded-Proto header is honoured, intended
// for deployments behind a TLS-terminating reverse proxy that strips /
// overrides the header from upstream clients. Do not enable trustXFP when
// untrusted traffic can reach the admin layer directly — the header is
// otherwise client-supplied.
func isSecureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if trustXFP && r.Header.Get("X-Forwarded-Proto") == "https" {
		return true
	}
	return false
}

// adminName returns the configured ADMIN_NAME from env.
func adminName() string { return os.Getenv("ADMIN_NAME") }

// adminSigningKey returns the ADMIN_SIGNING_KEY used exclusively for
// HMAC-SHA256 session cookie and CSRF token signing. This is deliberately
// separate from ADMIN_SECRET (the login password) so that compromise of
// one does not expose the other.
func adminSigningKey() string { return os.Getenv("ADMIN_SIGNING_KEY") }

// ── Password store ───────────────────────────────────────────────────────────
//
// At startup the plaintext ADMIN_SECRET is hashed with PBKDF2-SHA256 and a
// random per-process salt. Only the derived hash and the salt are retained;
// the raw password is never stored in package state. Login attempts recompute
// the PBKDF2 derivation and compare in constant time.

const (
	pbkdf2Iter   = 210_000 // OWASP recommendation range for SHA-256
	pbkdf2KeyLen = 32
)

var pwStore struct {
	mu   sync.Mutex
	hash []byte
	salt []byte
}

// initPasswordStore hashes ADMIN_SECRET with PBKDF2-SHA256 and a fresh
// random salt, then stores the result in pwStore. Safe to call more than
// once (tests call it after each t.Setenv).
func initPasswordStore() {
	secret := os.Getenv("ADMIN_SECRET")
	pwStore.mu.Lock()
	defer pwStore.mu.Unlock()
	if secret == "" {
		pwStore.hash = nil
		pwStore.salt = nil
		return
	}
	salt := make([]byte, 16)
	_, _ = rand.Read(salt)
	pwStore.salt = salt
	h, _ := pbkdf2.Key(sha256.New, secret, salt, pbkdf2Iter, pbkdf2KeyLen)
	pwStore.hash = h
}

// verifyPassword returns true when input matches the PBKDF2 hash computed
// at startup. The comparison is constant-time.
func verifyPassword(input string) bool {
	pwStore.mu.Lock()
	hash, salt := pwStore.hash, pwStore.salt
	pwStore.mu.Unlock()
	if hash == nil {
		return false
	}
	candidate, _ := pbkdf2.Key(sha256.New, input, salt, pbkdf2Iter, pbkdf2KeyLen)
	return subtle.ConstantTimeCompare(candidate, hash) == 1
}

// makeToken creates a signed session token:
// base64url( name | unix_nano_timestamp | hmac_sha256(secret, name|timestamp) )
func makeToken(name, secret string) string {
	ts := strconv.FormatInt(time.Now().UnixNano(), 10)
	mac := signMAC(secret, name+"|"+ts)
	raw := name + "|" + ts + "|" + mac
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// validateToken validates a session token and returns true if it belongs to
// the expected name, is correctly signed, and has not expired.
func validateToken(token, wantName, secret string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return false
	}
	parts := strings.SplitN(string(raw), "|", 3)
	if len(parts) != 3 {
		return false
	}
	name, ts, mac := parts[0], parts[1], parts[2]
	if subtle.ConstantTimeCompare([]byte(name), []byte(wantName)) != 1 {
		return false
	}
	expected := signMAC(secret, name+"|"+ts)
	if !hmac.Equal([]byte(mac), []byte(expected)) {
		return false
	}
	nano, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return false
	}
	return time.Since(time.Unix(0, nano)) < sessionTTL
}

func signMAC(secret, msg string) string {
	key := sha256.Sum256([]byte(secret))
	h := hmac.New(sha256.New, key[:])
	h.Write([]byte(msg))
	return hex.EncodeToString(h.Sum(nil))
}

// constantTimeCompareWithPad compares two byte slices in constant time,
// regardless of whether their lengths differ. crypto/subtle.ConstantTimeCompare
// returns immediately when lengths differ, leaking length information via a
// timing side-channel. When lengths are unequal the shorter slice is padded
// (via HMAC-SHA256) so the comparison always processes equal-length inputs.
func constantTimeCompareWithPad(a, b []byte) bool {
	if len(a) == len(b) {
		return subtle.ConstantTimeCompare(a, b) == 1
	}
	key := [32]byte{}
	ha := hmacDigest(key[:], a)
	hb := hmacDigest(key[:], b)
	return subtle.ConstantTimeCompare(ha, hb) == 1
}

func hmacDigest(key, msg []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(msg)
	return mac.Sum(nil)
}

// isAuthenticated reports whether the request carries a valid admin session cookie.
func isAuthenticated(r *http.Request) bool {
	name := adminName()
	key := adminSigningKey()
	if name == "" || key == "" {
		return false
	}
	c, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	return validateToken(c.Value, name, key)
}

// setSessionCookie writes a signed session cookie to the response.
// Cookie path is "/" because admin routes span multiple top-level prefixes
// (/metrics/, /cache/, /admin/*) with no common ancestor. The HttpOnly +
// SameSite=Lax flags prevent client-side JS and cross-site access.
func setSessionCookie(w http.ResponseWriter, r *http.Request, name, secret string) {
	token := makeToken(name, secret)
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
}

// clearSessionCookie expires the admin session cookie.
// Cookie path remains "/" because admin routes span /metrics/, /cache/, and
// /admin/* — there is no single common prefix. The HttpOnly + SameSite=Lax +
// Secure flags prevent client-side JS, cross-site access, and plain-HTTP leaks.
func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// requireAdmin is middleware that redirects unauthenticated requests to authRedirectURL.
func requireAdmin(next http.Handler, authRedirectURL string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isAuthenticated(r) {
			http.Redirect(w, r, authRedirectURL+"?"+authRedirParam+"="+r.URL.RequestURI(), http.StatusFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ── CSRF double-submit cookie ────────────────────────────────────────────────

// generateCSRFToken returns a cryptographically random, URL-safe token for
// the double-submit cookie CSRF pattern. Uses crypto/rand.Text (Go 1.24+)
// which returns 26 base32 characters = ~130 bits of entropy — more than
// sufficient for a short-lived anti-CSRF token and one call shorter than
// the prior make+Read+Encode triplet.
func generateCSRFToken() string {
	return rand.Text()
}

// setCSRFCookie writes a short-lived CSRF cookie for the login form.
func setCSRFCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   900, // 15 minutes
	})
}

// validCSRFToken checks that the form's _csrf field matches the CSRF cookie.
func validCSRFToken(r *http.Request) bool {
	c, err := r.Cookie(csrfCookieName)
	if err != nil || c.Value == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(c.Value), []byte(r.FormValue("_csrf"))) == 1
}

// sanitizeRedirect validates and cleans a redirect path to prevent open-redirect
// attacks. Returns "" when the input is empty, off-site, or otherwise unsafe.
func sanitizeRedirect(raw string) string {
	if raw == "" {
		return ""
	}
	clean := path.Clean(raw)
	if len(clean) == 0 || clean[0] != '/' || strings.HasPrefix(clean, "//") {
		return ""
	}
	return clean
}

// ── Login rate limiting ──────────────────────────────────────────────────────

const (
	loginMaxAttempts = 5
	loginWindow      = 15 * time.Minute
)

var loginLimiter struct {
	mu sync.Mutex
	// attempts tracks failed login timestamps keyed by source IP. IP-based
	// tracking stops a single attacker probing from one source.
	attempts map[string][]time.Time
	// userAttempts tracks failed login timestamps keyed by the submitted
	// username. Username-based tracking defeats distributed credential
	// stuffing where an attacker rotates source IPs but targets a fixed
	// known admin username — without this, the IP limiter rate-limits nothing
	// because each IP only attempts once. Counted per submitted username so
	// the attacker cannot enumerate valid usernames (every name is tracked).
	userAttempts map[string][]time.Time
	stopOnce     sync.Once
	stopCh       chan struct{}
}

// loginMaxUserAttempts caps failed attempts per submitted username across
// ALL source IPs inside loginWindow. Higher than the per-IP cap because
// legitimate users occasionally mistype from shared egress (offices, VPNs).
const loginMaxUserAttempts = 20

func init() {
	loginLimiter.attempts = make(map[string][]time.Time)
	loginLimiter.userAttempts = make(map[string][]time.Time)
	loginLimiter.stopCh = make(chan struct{})

	// Background janitor evicts stale IPs every loginWindow so that rotated
	// source addresses do not accumulate indefinitely in memory.
	// The goroutine exits when StopLoginLimiter is called (e.g. during shutdown
	// or in tests to prevent goroutine leaks).
	go func() {
		ticker := time.NewTicker(loginWindow)
		defer ticker.Stop()
		for {
			select {
			case <-loginLimiter.stopCh:
				return
			case <-ticker.C:
				now := time.Now()
				cutoff := now.Add(-loginWindow)
				loginLimiter.mu.Lock()
				pruneLocked(loginLimiter.attempts, cutoff)
				pruneLocked(loginLimiter.userAttempts, cutoff)
				loginLimiter.mu.Unlock()
			}
		}
	}()
}

// pruneLocked drops entries older than cutoff and removes empty slots.
// Caller must hold loginLimiter.mu.
func pruneLocked(m map[string][]time.Time, cutoff time.Time) {
	for k, times := range m {
		n := 0
		for _, t := range times {
			if t.After(cutoff) {
				times[n] = t
				n++
			}
		}
		if n == 0 {
			delete(m, k)
		} else {
			m[k] = times[:n]
		}
	}
}

// StopLoginLimiter terminates the background login-rate-limit janitor.
// Safe to call multiple times; subsequent calls are no-ops.
func StopLoginLimiter() {
	loginLimiter.stopOnce.Do(func() {
		close(loginLimiter.stopCh)
	})
}

// loginRateOK returns true if the IP has not exceeded the login attempt limit.
func loginRateOK(ip string) bool {
	loginLimiter.mu.Lock()
	defer loginLimiter.mu.Unlock()
	cutoff := time.Now().Add(-loginWindow)
	recent := loginLimiter.attempts[ip]
	n := 0
	for _, t := range recent {
		if t.After(cutoff) {
			recent[n] = t
			n++
		}
	}
	if n == 0 {
		delete(loginLimiter.attempts, ip)
		return true
	}
	loginLimiter.attempts[ip] = recent[:n]
	return n < loginMaxAttempts
}

// loginUserRateOK returns true if the submitted username has not exceeded
// its cross-IP attempt cap inside the rolling window. Empty usernames always
// pass (they fail credential check anyway and do not indicate targeted
// enumeration). The check and eviction happen atomically under the same
// mutex used by the IP limiter so both views stay consistent.
func loginUserRateOK(username string) bool {
	if username == "" {
		return true
	}
	loginLimiter.mu.Lock()
	defer loginLimiter.mu.Unlock()
	cutoff := time.Now().Add(-loginWindow)
	recent := loginLimiter.userAttempts[username]
	n := 0
	for _, t := range recent {
		if t.After(cutoff) {
			recent[n] = t
			n++
		}
	}
	if n == 0 {
		delete(loginLimiter.userAttempts, username)
		return true
	}
	loginLimiter.userAttempts[username] = recent[:n]
	return n < loginMaxUserAttempts
}

func recordLoginFailure(ip, username string) {
	loginLimiter.mu.Lock()
	now := time.Now()
	loginLimiter.attempts[ip] = append(loginLimiter.attempts[ip], now)
	if username != "" {
		loginLimiter.userAttempts[username] = append(loginLimiter.userAttempts[username], now)
	}
	loginLimiter.mu.Unlock()
}

func loginIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// loginHandler returns GET (render form) and POST (validate, set cookie) handlers
// for the given section. onSuccess is the default redirect after login.
// The tmplFn callback receives the error message, CSRF token, and sanitised
// ?next redirect so the template can embed them.
func loginHandler(section, onSuccess string, tmplFn func(w http.ResponseWriter, errMsg, csrfToken, next string)) (get http.HandlerFunc, post http.HandlerFunc) {
	get = func(w http.ResponseWriter, r *http.Request) {
		if isAuthenticated(r) {
			http.Redirect(w, r, onSuccess, http.StatusFound)
			return
		}
		csrf := generateCSRFToken()
		setCSRFCookie(w, r, csrf)
		tmplFn(w, "", csrf, sanitizeRedirect(r.URL.Query().Get(authRedirParam)))
	}

	post = func(w http.ResponseWriter, r *http.Request) {
		ip := loginIP(r)
		if !loginRateOK(ip) {
			http.Error(w, "Too many login attempts. Try again later.", http.StatusTooManyRequests)
			return
		}

		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		// CSRF double-submit cookie check — must pass before credential validation.
		if !validCSRFToken(r) {
			csrf := generateCSRFToken()
			setCSRFCookie(w, r, csrf)
			tmplFn(w, "Invalid or expired form. Please try again.", csrf, sanitizeRedirect(r.URL.Query().Get(authRedirParam)))
			return
		}

		wantName := adminName()
		sigKey := adminSigningKey()
		if wantName == "" || sigKey == "" {
			http.Error(w, "admin credentials not configured (ADMIN_NAME / ADMIN_SECRET / ADMIN_SIGNING_KEY)", http.StatusServiceUnavailable)
			return
		}
		inputName := r.FormValue("username")
		inputPass := r.FormValue("password")

		// Per-username rate check runs after form parsing so we have the
		// submitted username, but before credential validation so an attacker
		// cannot rotate IPs to bypass the per-IP cap. Uses the same generic
		// "too many attempts" error to avoid exposing which axis tripped.
		if !loginUserRateOK(inputName) {
			http.Error(w, "Too many login attempts. Try again later.", http.StatusTooManyRequests)
			return
		}

		// Username: constant-time comparison safe for unequal lengths.
		// Password: verified against the PBKDF2-SHA256 hash computed at startup;
		// the derivation cost is length-independent so no timing oracle exists.
		nameOK := constantTimeCompareWithPad([]byte(inputName), []byte(wantName))
		passOK := verifyPassword(inputPass)
		if !nameOK || !passOK {
			recordLoginFailure(ip, inputName)
			csrf := generateCSRFToken()
			setCSRFCookie(w, r, csrf)
			tmplFn(w, "Invalid username or password.", csrf, sanitizeRedirect(r.URL.Query().Get(authRedirParam)))
			return
		}

		setSessionCookie(w, r, wantName, sigKey)

		next := cmp.Or(sanitizeRedirect(r.URL.Query().Get(authRedirParam)), onSuccess)
		http.Redirect(w, r, next, http.StatusFound)
	}
	_ = section // reserved for structured logging
	return
}
