package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Stateless, HMAC-signed cookie sessions. There is no server-side session store,
// so logins survive restarts and horizontal scaling. The signing key is derived
// from the app password, so changing the password invalidates all sessions.

const (
	sessionCookie = "pi_session"
	sessionTTL    = 30 * 24 * time.Hour // 30 days, with sliding renewal
)

// sessionKey derives the HMAC key from the app password.
func (a *API) sessionKey() []byte {
	sum := sha256.Sum256([]byte(a.appPassword))
	return sum[:]
}

// signExpiry returns the cookie value "<expiryUnix>.<hexHMAC>".
func (a *API) signExpiry(expiryUnix int64) string {
	exp := strconv.FormatInt(expiryUnix, 10)
	mac := hmac.New(sha256.New, a.sessionKey())
	mac.Write([]byte(exp))
	return exp + "." + hex.EncodeToString(mac.Sum(nil))
}

// sessionValid reports whether the request carries an unexpired, correctly
// signed session cookie. Auth-disabled mode is handled by the caller.
func (a *API) sessionValid(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	exp, sig, ok := strings.Cut(c.Value, ".")
	if !ok {
		return false
	}
	expiryUnix, err := strconv.ParseInt(exp, 10, 64)
	if err != nil {
		return false
	}
	// Recompute the MAC over the claimed expiry and compare in constant time.
	mac := hmac.New(sha256.New, a.sessionKey())
	mac.Write([]byte(exp))
	want := mac.Sum(nil)
	got, err := hex.DecodeString(sig)
	if err != nil || !hmac.Equal(want, got) {
		return false
	}
	return time.Now().Unix() < expiryUnix
}

// issueSession sets a fresh 30-day session cookie (used on login and to slide
// the expiry on every authenticated request).
func (a *API) issueSession(w http.ResponseWriter, r *http.Request) {
	expiry := time.Now().Add(sessionTTL)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    a.signExpiry(expiry.Unix()),
		Path:     "/",
		Expires:  expiry,
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	})
}

// isHTTPS detects TLS directly or via a reverse proxy so Secure cookies work
// behind the VPS terminator.
func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	if a.appPassword == "" {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server missing APP_PASSWORD"})
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	// Constant-time compare to avoid leaking the password via timing.
	if body.Password == "" || subtle.ConstantTimeCompare([]byte(body.Password), []byte(a.appPassword)) != 1 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "incorrect password"})
		return
	}
	a.issueSession(w, r)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *API) authMe(w http.ResponseWriter, r *http.Request) {
	// authRequired lets the SPA decide whether to show a login screen / logout
	// button at all. When auth is disabled (VPN-only) the user is always
	// "authenticated" so the app skips the login gate entirely.
	required := a.appPassword != ""
	authed := !required || a.sessionValid(r)
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": authed, "authRequired": required})
}
