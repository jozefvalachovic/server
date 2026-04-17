package admin

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const (
	testPassword   = "supersecretpassword1234567890ab"
	testSigningKey = "signing-key-abcdef1234567890!!"
)

// setAdminEnv configures the three admin env vars and re-initialises the
// PBKDF2 password store so tests see the new ADMIN_SECRET value.
func setAdminEnv(t *testing.T, name, password, signingKey string) {
	t.Helper()
	t.Setenv("ADMIN_NAME", name)
	t.Setenv("ADMIN_SECRET", password)
	t.Setenv("ADMIN_SIGNING_KEY", signingKey)
	initPasswordStore()
}

// ── constantTimeCompareWithPad ───────────────────────────────────────────────

func TestConstantTimeCompareWithPad_EqualInputs(t *testing.T) {
	if !constantTimeCompareWithPad([]byte("secret"), []byte("secret")) {
		t.Fatal("equal inputs should match")
	}
}

func TestConstantTimeCompareWithPad_DifferentInputs(t *testing.T) {
	if constantTimeCompareWithPad([]byte("secret"), []byte("wrong")) {
		t.Fatal("different inputs should not match")
	}
}

func TestConstantTimeCompareWithPad_DifferentLengths(t *testing.T) {
	if constantTimeCompareWithPad([]byte("short"), []byte("muchlongerinput")) {
		t.Fatal("different-length inputs should not match")
	}
}

func TestConstantTimeCompareWithPad_EmptyInputs(t *testing.T) {
	if !constantTimeCompareWithPad(nil, nil) {
		t.Fatal("both nil should match")
	}
	if !constantTimeCompareWithPad([]byte{}, []byte{}) {
		t.Fatal("both empty should match")
	}
	if constantTimeCompareWithPad([]byte{}, []byte("x")) {
		t.Fatal("empty vs non-empty should not match")
	}
}

// ── Password store (PBKDF2) ─────────────────────────────────────────────────

func TestInitPasswordStore_HashesPassword(t *testing.T) {
	t.Setenv("ADMIN_SECRET", "mypassword")
	initPasswordStore()
	pwStore.mu.Lock()
	h, s := pwStore.hash, pwStore.salt
	pwStore.mu.Unlock()
	if h == nil || s == nil {
		t.Fatal("hash and salt must be set after init")
	}
	if len(h) != pbkdf2KeyLen {
		t.Fatalf("hash length = %d, want %d", len(h), pbkdf2KeyLen)
	}
}

func TestInitPasswordStore_EmptySecret(t *testing.T) {
	t.Setenv("ADMIN_SECRET", "")
	initPasswordStore()
	pwStore.mu.Lock()
	h := pwStore.hash
	pwStore.mu.Unlock()
	if h != nil {
		t.Fatal("hash should be nil when ADMIN_SECRET is empty")
	}
}

func TestVerifyPassword_Correct(t *testing.T) {
	t.Setenv("ADMIN_SECRET", "correcthorse")
	initPasswordStore()
	if !verifyPassword("correcthorse") {
		t.Fatal("correct password should verify")
	}
}

func TestVerifyPassword_Wrong(t *testing.T) {
	t.Setenv("ADMIN_SECRET", "correcthorse")
	initPasswordStore()
	if verifyPassword("wrongpassword") {
		t.Fatal("wrong password should not verify")
	}
}

func TestVerifyPassword_NotInitialised(t *testing.T) {
	t.Setenv("ADMIN_SECRET", "")
	initPasswordStore()
	if verifyPassword("anything") {
		t.Fatal("should return false when password store is empty")
	}
}

func TestSigningKey_IndependentOfPassword(t *testing.T) {
	// Tokens signed with the signing key should not validate with the password.
	token := makeToken("admin", "signing-key-A")
	if validateToken(token, "admin", "password-B") {
		t.Fatal("token signed with signing key must not validate with a different key")
	}
}

// ── makeToken / validateToken ────────────────────────────────────────────────

func TestMakeToken_RoundTrip(t *testing.T) {
	token := makeToken("admin", "supersecret")
	if token == "" {
		t.Fatal("token should not be empty")
	}
	if !validateToken(token, "admin", "supersecret") {
		t.Fatal("round-trip validation should succeed")
	}
}

func TestValidateToken_WrongName(t *testing.T) {
	token := makeToken("alice", "secret")
	if validateToken(token, "bob", "secret") {
		t.Fatal("should reject token signed for a different name")
	}
}

func TestValidateToken_WrongSecret(t *testing.T) {
	token := makeToken("admin", "secret1")
	if validateToken(token, "admin", "secret2") {
		t.Fatal("should reject token signed with a different secret")
	}
}

func TestValidateToken_Malformed(t *testing.T) {
	cases := []string{
		"",
		"not-base64-!!!",
		"YWJj",    // "abc" — no pipe delimiters
		"YXxifGM", // "a|b|c" — invalid timestamp, bad MAC
	}
	for _, tc := range cases {
		if validateToken(tc, "admin", "secret") {
			t.Fatalf("malformed token %q should not validate", tc)
		}
	}
}

func TestValidateToken_Tampered(t *testing.T) {
	token := makeToken("admin", "secret")
	// Flip a character in the token.
	runes := []rune(token)
	if runes[len(runes)-1] == 'A' {
		runes[len(runes)-1] = 'B'
	} else {
		runes[len(runes)-1] = 'A'
	}
	tampered := string(runes)
	if validateToken(tampered, "admin", "secret") {
		t.Fatal("tampered token should not validate")
	}
}

// ── signMAC ──────────────────────────────────────────────────────────────────

func TestSignMAC_Deterministic(t *testing.T) {
	a := signMAC("key", "msg")
	b := signMAC("key", "msg")
	if a != b {
		t.Fatal("same inputs should produce same MAC")
	}
}

func TestSignMAC_DifferentKeys(t *testing.T) {
	a := signMAC("key1", "msg")
	b := signMAC("key2", "msg")
	if a == b {
		t.Fatal("different keys should produce different MACs")
	}
}

// ── isSecureRequest ──────────────────────────────────────────────────────────

func TestIsSecureRequest_TLS(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.TLS = &tls.ConnectionState{}
	if !isSecureRequest(r) {
		t.Fatal("should be secure when TLS is present")
	}
}

func TestIsSecureRequest_NoTLS(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if isSecureRequest(r) {
		t.Fatal("should not be secure without TLS and without trustXFP")
	}
}

func TestIsSecureRequest_XFP_Trusted(t *testing.T) {
	old := trustXFP
	trustXFP = true
	defer func() { trustXFP = old }()

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Forwarded-Proto", "https")
	if !isSecureRequest(r) {
		t.Fatal("should be secure when trustXFP=true and header is https")
	}
}

func TestIsSecureRequest_XFP_NotTrusted(t *testing.T) {
	old := trustXFP
	trustXFP = false
	defer func() { trustXFP = old }()

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Forwarded-Proto", "https")
	if isSecureRequest(r) {
		t.Fatal("should not be secure when trustXFP=false even with header")
	}
}

// ── sanitizeRedirect ─────────────────────────────────────────────────────────

func TestSanitizeRedirect(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", ""},
		{"/dashboard", "/dashboard"},
		{"/admin/../secret", "/secret"},
		{"https://evil.com", ""},
		{"//evil.com", "/evil.com"}, // path.Clean normalizes to local path
		{"relative/path", ""},
		{"/valid/path", "/valid/path"},
	}
	for _, tc := range tests {
		got := sanitizeRedirect(tc.input)
		if got != tc.want {
			t.Errorf("sanitizeRedirect(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// ── validCSRFToken ───────────────────────────────────────────────────────────

func TestValidCSRFToken_Match(t *testing.T) {
	token := generateCSRFToken()
	form := url.Values{"_csrf": {token}}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: token})
	if err := r.ParseForm(); err != nil {
		t.Fatal(err)
	}
	if !validCSRFToken(r) {
		t.Fatal("matching CSRF token should be valid")
	}
}

func TestValidCSRFToken_Mismatch(t *testing.T) {
	form := url.Values{"_csrf": {"token-a"}}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "token-b"})
	if err := r.ParseForm(); err != nil {
		t.Fatal(err)
	}
	if validCSRFToken(r) {
		t.Fatal("mismatched CSRF tokens should be invalid")
	}
}

func TestValidCSRFToken_NoCookie(t *testing.T) {
	form := url.Values{"_csrf": {"token"}}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := r.ParseForm(); err != nil {
		t.Fatal(err)
	}
	if validCSRFToken(r) {
		t.Fatal("missing CSRF cookie should be invalid")
	}
}

// ── generateCSRFToken ────────────────────────────────────────────────────────

func TestGenerateCSRFToken_Unique(t *testing.T) {
	a := generateCSRFToken()
	b := generateCSRFToken()
	if a == "" || b == "" {
		t.Fatal("tokens should not be empty")
	}
	if a == b {
		t.Fatal("successive tokens should be unique")
	}
}

// ── loginRateOK / loginUserRateOK / recordLoginFailure ───────────────────────

func resetLoginLimiter() {
	loginLimiter.mu.Lock()
	loginLimiter.attempts = make(map[string][]time.Time)
	loginLimiter.userAttempts = make(map[string][]time.Time)
	loginLimiter.mu.Unlock()
}

func TestLoginRateOK_UnderLimit(t *testing.T) {
	resetLoginLimiter()
	for range loginMaxAttempts - 1 {
		recordLoginFailure("10.0.0.1", "admin")
	}
	if !loginRateOK("10.0.0.1") {
		t.Fatal("should be under the rate limit")
	}
}

func TestLoginRateOK_AtLimit(t *testing.T) {
	resetLoginLimiter()
	for range loginMaxAttempts {
		recordLoginFailure("10.0.0.2", "admin")
	}
	if loginRateOK("10.0.0.2") {
		t.Fatal("should be at/over the rate limit")
	}
}

func TestLoginRateOK_DifferentIPs(t *testing.T) {
	resetLoginLimiter()
	for range loginMaxAttempts {
		recordLoginFailure("10.0.0.3", "admin")
	}
	if !loginRateOK("10.0.0.4") {
		t.Fatal("different IP should not be rate-limited")
	}
}

func TestLoginUserRateOK_UnderLimit(t *testing.T) {
	resetLoginLimiter()
	for range loginMaxUserAttempts - 1 {
		recordLoginFailure("10.0.0.1", "targetuser")
	}
	if !loginUserRateOK("targetuser") {
		t.Fatal("should be under the per-user limit")
	}
}

func TestLoginUserRateOK_AtLimit(t *testing.T) {
	resetLoginLimiter()
	for range loginMaxUserAttempts {
		recordLoginFailure("10.0.0.1", "targetuser2")
	}
	if loginUserRateOK("targetuser2") {
		t.Fatal("should be at/over the per-user limit")
	}
}

func TestLoginUserRateOK_EmptyUsername(t *testing.T) {
	resetLoginLimiter()
	if !loginUserRateOK("") {
		t.Fatal("empty username should always pass")
	}
}

// ── loginHandler ─────────────────────────────────────────────────────────────

func TestLoginHandler_GET_Unauthenticated(t *testing.T) {
	var rendered bool
	tmplFn := func(w http.ResponseWriter, errMsg, csrfToken, next string) {
		rendered = true
		if csrfToken == "" {
			t.Fatal("CSRF token should be non-empty")
		}
	}
	get, _ := loginHandler("test", "/home", tmplFn)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/auth", nil)
	get(w, r)
	if !rendered {
		t.Fatal("template should have been rendered")
	}
}

func TestLoginHandler_GET_Authenticated(t *testing.T) {
	setAdminEnv(t, "admin", testPassword, testSigningKey)
	resetLoginLimiter()

	token := makeToken("admin", testSigningKey)

	get, _ := loginHandler("test", "/home", func(w http.ResponseWriter, errMsg, csrfToken, next string) {
		t.Fatal("should not render form when authenticated")
	})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/auth", nil)
	r.AddCookie(&http.Cookie{Name: cookieName, Value: token})
	get(w, r)
	if w.Code != http.StatusFound {
		t.Fatalf("want 302, got %d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/home" {
		t.Fatalf("want redirect to /home, got %s", loc)
	}
}

func TestLoginHandler_POST_ValidCreds(t *testing.T) {
	setAdminEnv(t, "admin", testPassword, testSigningKey)
	resetLoginLimiter()

	csrf := generateCSRFToken()
	form := url.Values{
		"username": {"admin"},
		"password": {testPassword},
		"_csrf":    {csrf},
	}
	_, post := loginHandler("test", "/home", func(w http.ResponseWriter, errMsg, csrfToken, next string) {
		t.Fatalf("should not render error form; errMsg=%q", errMsg)
	})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/auth", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrf})
	r.RemoteAddr = "127.0.0.1:12345"
	post(w, r)
	if w.Code != http.StatusFound {
		t.Fatalf("want 302, got %d", w.Code)
	}
	// Should have set a session cookie.
	found := false
	for _, c := range w.Result().Cookies() {
		if c.Name == cookieName && c.Value != "" {
			found = true
		}
	}
	if !found {
		t.Fatal("session cookie should be set after valid login")
	}
}

func TestLoginHandler_POST_InvalidCreds(t *testing.T) {
	setAdminEnv(t, "admin", testPassword, testSigningKey)
	resetLoginLimiter()

	csrf := generateCSRFToken()
	form := url.Values{
		"username": {"admin"},
		"password": {"wrongpassword"},
		"_csrf":    {csrf},
	}
	var errMsg string
	_, post := loginHandler("test", "/home", func(w http.ResponseWriter, msg, csrfToken, next string) {
		errMsg = msg
	})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/auth", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrf})
	r.RemoteAddr = "127.0.0.1:12345"
	post(w, r)
	if errMsg == "" {
		t.Fatal("should render an error message for invalid credentials")
	}
}

func TestLoginHandler_POST_BadCSRF(t *testing.T) {
	setAdminEnv(t, "admin", testPassword, testSigningKey)
	resetLoginLimiter()

	form := url.Values{
		"username": {"admin"},
		"password": {testPassword},
		"_csrf":    {"bad-csrf"},
	}
	var errMsg string
	_, post := loginHandler("test", "/home", func(w http.ResponseWriter, msg, csrfToken, next string) {
		errMsg = msg
	})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/auth", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "different-csrf"})
	r.RemoteAddr = "127.0.0.1:12345"
	post(w, r)
	if errMsg == "" {
		t.Fatal("should render error when CSRF token doesn't match")
	}
}

func TestLoginHandler_POST_RateLimited(t *testing.T) {
	setAdminEnv(t, "admin", testPassword, testSigningKey)
	resetLoginLimiter()

	// Exhaust the per-IP rate limit.
	for range loginMaxAttempts {
		recordLoginFailure("10.0.0.99", "admin")
	}

	csrf := generateCSRFToken()
	form := url.Values{
		"username": {"admin"},
		"password": {testPassword},
		"_csrf":    {csrf},
	}
	_, post := loginHandler("test", "/home", func(w http.ResponseWriter, msg, csrfToken, next string) {})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/auth", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrf})
	r.RemoteAddr = "10.0.0.99:12345"
	post(w, r)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("want 429, got %d", w.Code)
	}
}

func TestLoginHandler_POST_NoCreds(t *testing.T) {
	setAdminEnv(t, "", "", "")
	resetLoginLimiter()

	csrf := generateCSRFToken()
	form := url.Values{
		"username": {"admin"},
		"password": {"pass"},
		"_csrf":    {csrf},
	}
	_, post := loginHandler("test", "/home", func(w http.ResponseWriter, msg, csrfToken, next string) {})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/auth", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrf})
	r.RemoteAddr = "127.0.0.1:12345"
	post(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 when creds not configured, got %d", w.Code)
	}
}

// ── requireAdmin ─────────────────────────────────────────────────────────────

func TestRequireAdmin_Unauthenticated(t *testing.T) {
	setAdminEnv(t, "admin", testPassword, testSigningKey)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("inner handler should not be called")
	})
	h := requireAdmin(inner, "/login")
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/metrics/", nil)
	h.ServeHTTP(w, r)
	if w.Code != http.StatusFound {
		t.Fatalf("want 302 redirect, got %d", w.Code)
	}
	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, "/login?") {
		t.Fatalf("want redirect to /login, got %s", loc)
	}
}

func TestRequireAdmin_Authenticated(t *testing.T) {
	setAdminEnv(t, "admin", testPassword, testSigningKey)

	var called bool
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})
	h := requireAdmin(inner, "/login")
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/metrics/", nil)
	r.AddCookie(&http.Cookie{Name: cookieName, Value: makeToken("admin", testSigningKey)})
	h.ServeHTTP(w, r)
	if !called {
		t.Fatal("inner handler should have been called for authenticated request")
	}
}

// ── isAuthenticated ──────────────────────────────────────────────────────────

func TestIsAuthenticated_NoCreds(t *testing.T) {
	setAdminEnv(t, "", "", "")
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if isAuthenticated(r) {
		t.Fatal("should return false when creds not configured")
	}
}

func TestIsAuthenticated_NoCookie(t *testing.T) {
	setAdminEnv(t, "admin", "secret123", "signing-key-123")
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if isAuthenticated(r) {
		t.Fatal("should return false when no session cookie")
	}
}

func TestIsAuthenticated_ValidCookie(t *testing.T) {
	setAdminEnv(t, "admin", testPassword, testSigningKey)
	token := makeToken("admin", testSigningKey)
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: cookieName, Value: token})
	if !isAuthenticated(r) {
		t.Fatal("should return true for valid session cookie")
	}
}

// ── setSessionCookie / clearSessionCookie ────────────────────────────────────

func TestSetSessionCookie(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	setSessionCookie(w, r, "admin", "secret")
	cookies := w.Result().Cookies()
	var found *http.Cookie
	for _, c := range cookies {
		if c.Name == cookieName {
			found = c
		}
	}
	if found == nil {
		t.Fatal("session cookie should be set")
	}
	if !found.HttpOnly {
		t.Fatal("session cookie should be HttpOnly")
	}
	if found.SameSite != http.SameSiteLaxMode {
		t.Fatal("session cookie should be SameSite=Lax")
	}
	if found.Path != "/" {
		t.Fatalf("session cookie path should be /, got %s", found.Path)
	}
}

func TestClearSessionCookie(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	clearSessionCookie(w, r)
	cookies := w.Result().Cookies()
	var found *http.Cookie
	for _, c := range cookies {
		if c.Name == cookieName {
			found = c
		}
	}
	if found == nil {
		t.Fatal("clear should set a cookie")
	}
	if found.MaxAge != -1 {
		t.Fatalf("MaxAge should be -1, got %d", found.MaxAge)
	}
}

// ── pruneLocked ──────────────────────────────────────────────────────────────

func TestPruneLocked(t *testing.T) {
	now := time.Now()
	m := map[string][]time.Time{
		"old":  {now.Add(-2 * loginWindow)},
		"mix":  {now.Add(-2 * loginWindow), now},
		"new":  {now},
		"gone": {now.Add(-3 * loginWindow)},
	}
	pruneLocked(m, now.Add(-loginWindow))
	if _, ok := m["old"]; ok {
		t.Fatal("old entries should be pruned")
	}
	if _, ok := m["gone"]; ok {
		t.Fatal("gone entries should be pruned")
	}
	if len(m["mix"]) != 1 {
		t.Fatalf("mix should have 1 entry, got %d", len(m["mix"]))
	}
	if len(m["new"]) != 1 {
		t.Fatal("new should be kept")
	}
}

// ── loginIP ──────────────────────────────────────────────────────────────────

func TestLoginIP(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "192.168.1.1:54321"
	if ip := loginIP(r); ip != "192.168.1.1" {
		t.Fatalf("want 192.168.1.1, got %s", ip)
	}
}

func TestLoginIP_NoPort(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "192.168.1.1"
	if ip := loginIP(r); ip != "192.168.1.1" {
		t.Fatalf("want 192.168.1.1, got %s", ip)
	}
}
