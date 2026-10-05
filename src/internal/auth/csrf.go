package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
)

const csrfFormField = "csrf_token"

// CSRFToken returns a token tied to the current signed session cookie. The
// token has no server-side state and becomes unusable when the session does.
func (a *Authenticator) CSRFToken(r *http.Request) string {
	cookie, err := r.Cookie(CookieName)
	if err != nil || cookie.Value == "" {
		return ""
	}
	if _, _, ok := a.VerifySession(r); !ok {
		return ""
	}
	mac := hmac.New(sha256.New, a.secret)
	mac.Write([]byte("csrf:" + cookie.Value))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// RequireCSRF rejects requests whose form token or X-CSRF-Token header is not
// bound to the current authenticated session. It is intended to run inside an
// authentication middleware so unauthenticated requests retain their existing
// behavior.
func (a *Authenticator) RequireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		expected := a.CSRFToken(r)
		provided := r.FormValue(csrfFormField)
		if provided == "" {
			provided = r.Header.Get("X-CSRF-Token")
		}
		if expected == "" || !hmac.Equal([]byte(expected), []byte(provided)) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
