// Package auth handles the login flow: delegating password checks to
// internal/users, and issuing a stateless HMAC-signed session cookie
// (no server-side session store).
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sethwv/my-sideload-library/internal/users"
)

const CookieName = "sideload_session"

// Authenticator's ttl is mutable (see SetTTL) so the admin Settings page can
// change the session lifetime without a restart; already-issued cookies
// keep whatever expiry they were signed with, only sessions issued after
// the change use the new ttl.
type Authenticator struct {
	users  *users.Store
	secret []byte

	mu  sync.RWMutex
	ttl time.Duration
}

// New creates an Authenticator backed by the given users store.
func New(secret string, ttl time.Duration, store *users.Store) *Authenticator {
	return &Authenticator{
		users:  store,
		secret: []byte(secret),
		ttl:    ttl,
	}
}

// SetTTL updates the session lifetime used for sessions issued from now on.
func (a *Authenticator) SetTTL(ttl time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ttl = ttl
}

// CheckPassword reports whether the given credentials are a valid login.
func (a *Authenticator) CheckPassword(username, password string) bool {
	return a.users.CheckPassword(username, password)
}

// IssueSession sets a signed, full-access session cookie for username —
// used after a password login. Full sessions can reach admin routes and
// other sensitive actions; see IssueRestrictedSession for the bookmark-
// token equivalent.
func (a *Authenticator) IssueSession(w http.ResponseWriter, r *http.Request, username string) {
	a.issueSession(w, r, username, false)
}

// IssueRestrictedSession sets a signed session cookie for username limited
// to view/shelves-only access. Issued opportunistically when a bookmark
// token authenticates a request, so cookie-capable browsing within the
// session doesn't need the token on every link — without handing the token
// holder admin powers or the ability to change passwords, since anyone with
// the bookmark link has a weaker guarantee than someone who typed a
// password. Reaching an admin route with a restricted session redirects to
// a password prompt (RequireFull) rather than 403ing outright, so the
// legitimate account owner can step up to a full session in place.
func (a *Authenticator) IssueRestrictedSession(w http.ResponseWriter, r *http.Request, username string) {
	a.issueSession(w, r, username, true)
}

func (a *Authenticator) issueSession(w http.ResponseWriter, r *http.Request, username string, restricted bool) {
	a.mu.RLock()
	ttl := a.ttl
	a.mu.RUnlock()
	expires := time.Now().Add(ttl)
	payload := username + "|" + strconv.FormatInt(expires.Unix(), 10)
	if restricted {
		payload += "|restricted"
	}
	token := a.sign(payload)

	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
		Expires:  expires,
	})
}

// ClearSession removes the session cookie using the current request's
// transport security so it matches the cookie that was issued at login.
func (a *Authenticator) ClearSession(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

func isSecureRequest(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// VerifySession reports the signed-in username and whether the session is
// restricted (bookmark-token-issued: view/shelves only, see
// IssueRestrictedSession) if the request carries a valid, unexpired session
// cookie. A cookie issued before restricted sessions existed has no third
// field and is treated as full, matching its original (only) meaning.
func (a *Authenticator) VerifySession(r *http.Request) (username string, restricted bool, ok bool) {
	c, err := r.Cookie(CookieName)
	if err != nil || c.Value == "" {
		return "", false, false
	}

	payload, verified := a.verify(c.Value)
	if !verified {
		return "", false, false
	}

	parts := strings.SplitN(payload, "|", 3)
	if len(parts) < 2 || parts[0] == "" {
		return "", false, false
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return "", false, false
	}
	if time.Now().Unix() >= exp {
		return "", false, false
	}
	user, err := a.users.UserByUsername(parts[0])
	if err != nil || user == nil || !user.Enabled {
		return "", false, false
	}
	restricted = len(parts) == 3 && parts[2] == "restricted"
	return parts[0], restricted, true
}

func (a *Authenticator) sign(payload string) string {
	mac := hmac.New(sha256.New, a.secret)
	mac.Write([]byte(payload))
	sig := mac.Sum(nil)
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func (a *Authenticator) verify(token string) (string, bool) {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return "", false
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", false
	}
	sigBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", false
	}

	mac := hmac.New(sha256.New, a.secret)
	mac.Write(payloadBytes)
	expected := mac.Sum(nil)

	if !hmac.Equal(expected, sigBytes) {
		return "", false
	}
	return string(payloadBytes), true
}
