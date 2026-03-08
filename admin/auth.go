package admin

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	cookieName     = "_admin_session"
	sessionTTL     = 8 * time.Hour
	authRedirParam = "next"
)

// adminCreds returns the configured ADMIN_NAME and ADMIN_SECRET from env.
func adminCreds() (name, secret string) {
	return os.Getenv("ADMIN_NAME"), os.Getenv("ADMIN_SECRET")
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
	if name != wantName {
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
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(msg))
	return hex.EncodeToString(h.Sum(nil))
}

// isAuthenticated reports whether the request carries a valid admin session cookie.
func isAuthenticated(r *http.Request) bool {
	name, secret := adminCreds()
	if name == "" || secret == "" {
		return false
	}
	c, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	return validateToken(c.Value, name, secret)
}

// setSessionCookie writes a signed session cookie to the response.
func setSessionCookie(w http.ResponseWriter, r *http.Request, name, secret string) {
	token := makeToken(name, secret)
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
}

// clearSessionCookie expires the admin session cookie.
func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
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

// nonce generates a random hex string for use as a CSRF-equivalent one-time
// field on login forms. Not strictly required for a tool-operator UI, but good
// hygiene. Currently unused — reserved for future hardening.

// loginHandler returns GET (render form) and POST (validate, set cookie) handlers
// for the given section. onSuccess is the default redirect after login.
func loginHandler(section, onSuccess string, tmplFn func(w http.ResponseWriter, errMsg string)) (get http.HandlerFunc, post http.HandlerFunc) {
	get = func(w http.ResponseWriter, r *http.Request) {
		if isAuthenticated(r) {
			http.Redirect(w, r, onSuccess, http.StatusFound)
			return
		}
		tmplFn(w, "")
	}

	post = func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		wantName, wantSecret := adminCreds()
		if wantName == "" || wantSecret == "" {
			http.Error(w, "admin credentials not configured (ADMIN_NAME / ADMIN_SECRET)", http.StatusServiceUnavailable)
			return
		}
		inputName := r.FormValue("username")
		inputPass := r.FormValue("password")

		// Constant-time equality check via HMAC to avoid timing side-channels.
		nameOK := hmac.Equal([]byte(inputName), []byte(wantName))
		passOK := hmac.Equal([]byte(inputPass), []byte(wantSecret))
		if !nameOK || !passOK {
			tmplFn(w, "Invalid username or password.")
			return
		}

		setSessionCookie(w, r, wantName, wantSecret)

		next := r.URL.Query().Get(authRedirParam)
		if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
			next = onSuccess
		}
		http.Redirect(w, r, next, http.StatusFound)
	}
	_ = section // reserved for structured logging
	return
}
