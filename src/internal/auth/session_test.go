package auth

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sethwv/my-sideload-library/internal/users"
)

func testStore(t *testing.T) *users.Store {
	t.Helper()
	s, err := users.Open(filepath.Join(t.TempDir(), "users.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.Create("reader", "s3cret", users.RoleMember, true, ""); err != nil {
		t.Fatal(err)
	}
	return s
}

func testAuthenticator(t *testing.T) *Authenticator {
	t.Helper()
	return New("test-signing-secret", time.Hour, testStore(t))
}

func TestCheckPassword(t *testing.T) {
	a := testAuthenticator(t)

	if !a.CheckPassword("reader", "s3cret") {
		t.Error("expected correct credentials to pass")
	}
	if a.CheckPassword("reader", "wrong") {
		t.Error("expected wrong password to fail")
	}
	if a.CheckPassword("nope", "s3cret") {
		t.Error("expected wrong username to fail")
	}
}

func TestSessionRoundTrip(t *testing.T) {
	a := testAuthenticator(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/login", nil)
	a.IssueSession(w, req, "reader")

	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected 1 cookie, got %d", len(cookies))
	}

	verifyReq := httptest.NewRequest("GET", "/", nil)
	verifyReq.AddCookie(cookies[0])
	username, restricted, ok := a.VerifySession(verifyReq)
	if !ok {
		t.Error("expected valid session to verify")
	}
	if username != "reader" {
		t.Errorf("username = %q, want %q", username, "reader")
	}
	if restricted {
		t.Error("expected a password-issued session to be full, not restricted")
	}
	if cookies[0].Secure {
		t.Error("expected HTTP session cookie to allow direct LAN access")
	}
}

func TestSessionCookieRequiresHTTPSWhenForwarded(t *testing.T) {
	a := testAuthenticator(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/login", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	a.IssueSession(w, req, "reader")
	if !w.Result().Cookies()[0].Secure {
		t.Error("expected HTTPS-forwarded session cookie to require HTTPS")
	}
}

func TestDisabledUserSessionIsRejected(t *testing.T) {
	store := testStore(t)
	a := New("test-signing-secret", time.Hour, store)
	user, err := store.UserByUsername("reader")
	if err != nil || user == nil {
		t.Fatalf("UserByUsername() = (%+v, %v)", user, err)
	}

	issue := httptest.NewRecorder()
	a.IssueSession(issue, httptest.NewRequest(http.MethodPost, "/login", nil), "reader")
	restrictedIssue := httptest.NewRecorder()
	a.IssueRestrictedSession(restrictedIssue, httptest.NewRequest(http.MethodGet, "/", nil), "reader")
	if err := store.SetEnabled(user.ID, false); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(issue.Result().Cookies()[0])
	if _, _, ok := a.VerifySession(req); ok {
		t.Error("disabled user session verified")
	}
	restrictedReq := httptest.NewRequest(http.MethodGet, "/", nil)
	restrictedReq.AddCookie(restrictedIssue.Result().Cookies()[0])
	if _, _, ok := a.VerifySession(restrictedReq); ok {
		t.Error("disabled bookmark session verified")
	}

	w := httptest.NewRecorder()
	a.RequireAuth(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("disabled session reached handler") })).ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want login redirect", w.Code)
	}
}

func TestClearSessionMatchesRequestTransport(t *testing.T) {
	a := testAuthenticator(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	a.ClearSession(w, req)

	cookie := w.Result().Cookies()[0]
	if cookie.Secure {
		t.Error("expected cleared HTTP session cookie to allow direct LAN access")
	}
	if cookie.MaxAge != -1 {
		t.Errorf("MaxAge = %d, want -1", cookie.MaxAge)
	}
}

func TestSessionExpired(t *testing.T) {
	a := New("test-signing-secret", -time.Hour, testStore(t))

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/login", nil)
	a.IssueSession(w, req, "reader")

	verifyReq := httptest.NewRequest("GET", "/", nil)
	verifyReq.AddCookie(w.Result().Cookies()[0])
	if _, _, ok := a.VerifySession(verifyReq); ok {
		t.Error("expected expired session to fail verification")
	}
}

func TestSessionTamperedRejected(t *testing.T) {
	a := testAuthenticator(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/login", nil)
	a.IssueSession(w, req, "reader")
	cookie := w.Result().Cookies()[0]
	cookie.Value = cookie.Value + "x"

	verifyReq := httptest.NewRequest("GET", "/", nil)
	verifyReq.AddCookie(cookie)
	if _, _, ok := a.VerifySession(verifyReq); ok {
		t.Error("expected tampered cookie to fail verification")
	}
}

func TestSessionWrongSecretRejected(t *testing.T) {
	store := testStore(t)
	a1 := New("secret-one", time.Hour, store)
	a2 := New("secret-two", time.Hour, store)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/login", nil)
	a1.IssueSession(w, req, "reader")

	verifyReq := httptest.NewRequest("GET", "/", nil)
	verifyReq.AddCookie(w.Result().Cookies()[0])
	if _, _, ok := a2.VerifySession(verifyReq); ok {
		t.Error("expected session signed with different secret to fail verification")
	}
}

func TestRequireAuth_RedirectsUnauthenticated(t *testing.T) {
	a := testAuthenticator(t)
	handler := a.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want %d", w.Code, http.StatusSeeOther)
	}
	loc := w.Header().Get("Location")
	if loc == "" || loc[:6] != "/login" {
		t.Errorf("Location = %q, want redirect to /login", loc)
	}
}

func TestRequireAuth_AllowsAuthenticated(t *testing.T) {
	a := testAuthenticator(t)
	var gotUsername string
	handler := a.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUsername, _ = UsernameFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	issueW := httptest.NewRecorder()
	a.IssueSession(issueW, httptest.NewRequest("POST", "/login", nil), "reader")

	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(issueW.Result().Cookies()[0])
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if gotUsername != "reader" {
		t.Errorf("username in context = %q, want %q", gotUsername, "reader")
	}
}

func TestRequireManageUsers_ForbidsNonManager(t *testing.T) {
	store := testStore(t) // "reader" created as non-admin
	a := New("test-signing-secret", time.Hour, store)
	handler := a.RequireManageUsers(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	issueW := httptest.NewRecorder()
	a.IssueSession(issueW, httptest.NewRequest("POST", "/login", nil), "reader")

	req := httptest.NewRequest("GET", "/admin/users", nil)
	req.AddCookie(issueW.Result().Cookies()[0])
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestRequireAuth_AllowsValidBookmarkToken(t *testing.T) {
	store := testStore(t) // "reader" created as non-admin
	a := New("test-signing-secret", time.Hour, store)
	var gotUsername string
	var gotRestricted bool
	handler := a.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUsername, _ = UsernameFromContext(r.Context())
		gotRestricted = IsRestricted(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	token, err := store.GenerateBookmarkToken("reader")
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/?token="+token, nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if gotUsername != "reader" {
		t.Errorf("username in context = %q, want %q", gotUsername, "reader")
	}
	if !gotRestricted {
		t.Error("expected a bookmark-token-authenticated request to be marked restricted")
	}
	if len(w.Result().Cookies()) != 1 {
		t.Error("expected a session cookie to be issued alongside a valid token, so cookie-capable navigation doesn't need the token on every link")
	}
	if w.Result().Cookies()[0].Secure {
		t.Error("expected restricted HTTP session cookie to allow direct LAN access")
	}
}

func TestRequireFull_RedirectsRestrictedSessionToLogin(t *testing.T) {
	store := testStore(t)
	a := New("test-signing-secret", time.Hour, store)
	handler := a.RequireFull(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	token, err := store.GenerateBookmarkToken("reader")
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/admin/server?token="+token, nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want %d (redirect to login for password step-up)", w.Code, http.StatusSeeOther)
	}
	loc := w.Header().Get("Location")
	if loc == "" || loc[:6] != "/login" {
		t.Errorf("Location = %q, want redirect to /login", loc)
	}
}

func TestRequireFull_AllowsFullSession(t *testing.T) {
	a := testAuthenticator(t)
	handler := a.RequireFull(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	issueW := httptest.NewRecorder()
	a.IssueSession(issueW, httptest.NewRequest("POST", "/login", nil), "reader")

	req := httptest.NewRequest("GET", "/admin/server", nil)
	req.AddCookie(issueW.Result().Cookies()[0])
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestRequireManageServer_RedirectsRestrictedSessionToLoginInsteadOf403(t *testing.T) {
	store := testStore(t)
	if err := store.Create("admin", "s3cret", users.RoleAdmin, true, ""); err != nil {
		t.Fatal(err)
	}
	a := New("test-signing-secret", time.Hour, store)
	handler := a.RequireManageServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Even though "admin" really is an admin, a bookmark-token (restricted)
	// session for that account must still be sent through a password
	// step-up rather than let through or flatly 403'd.
	token, err := store.GenerateBookmarkToken("admin")
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/admin/server?token="+token, nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want %d (redirect to login for password step-up)", w.Code, http.StatusSeeOther)
	}
}

func TestRequireAuth_RejectsInvalidBookmarkToken(t *testing.T) {
	a := testAuthenticator(t)
	handler := a.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/?token=not-a-real-token", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want %d (redirect to login)", w.Code, http.StatusSeeOther)
	}
}

func TestRequireManageUsers_AllowsAdmin(t *testing.T) {
	store := testStore(t)
	if err := store.Create("admin", "s3cret", users.RoleAdmin, true, ""); err != nil {
		t.Fatal(err)
	}
	a := New("test-signing-secret", time.Hour, store)
	handler := a.RequireManageUsers(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	issueW := httptest.NewRecorder()
	a.IssueSession(issueW, httptest.NewRequest("POST", "/login", nil), "admin")

	req := httptest.NewRequest("GET", "/admin/users", nil)
	req.AddCookie(issueW.Result().Cookies()[0])
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestRequirePermission_RedirectsRestrictedSessionBeforeCheckingCapability(t *testing.T) {
	store := testStore(t)
	a := New("test-signing-secret", time.Hour, store)
	handler := a.RequirePermission(users.PermissionOwnShelves, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	token, err := store.GenerateBookmarkToken("reader")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/account/shelves?token="+token, nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want password step-up redirect", w.Code)
	}
}
