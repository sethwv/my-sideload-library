package auth

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/sethwv/my-sideload-library/internal/users"
)

func csrfRequest(t *testing.T, a *Authenticator, username string, form url.Values) *http.Request {
	t.Helper()
	issue := httptest.NewRecorder()
	a.IssueSession(issue, httptest.NewRequest(http.MethodPost, "/login", nil), username)
	req := httptest.NewRequest(http.MethodPost, "/account", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(issue.Result().Cookies()[0])
	return req
}

func TestRequireCSRFAcceptsSessionBoundToken(t *testing.T) {
	a := testAuthenticator(t)
	req := csrfRequest(t, a, "reader", url.Values{})
	token := a.CSRFToken(req)
	req = csrfRequest(t, a, "reader", url.Values{csrfFormField: {token}})

	called := false
	a.RequireCSRF(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(httptest.NewRecorder(), req)
	if !called {
		t.Fatal("valid CSRF token did not reach handler")
	}
}

func TestRequireCSRFRejectsMissingAndForeignTokens(t *testing.T) {
	a := testAuthenticator(t)
	if err := a.users.Create("other", "s3cret", users.RoleMember, true, ""); err != nil {
		t.Fatal(err)
	}
	foreign := csrfRequest(t, a, "other", url.Values{})
	foreignToken := a.CSRFToken(foreign)

	for _, form := range []url.Values{
		{},
		{csrfFormField: {"not-a-token"}},
		{csrfFormField: {foreignToken}},
	} {
		req := csrfRequest(t, a, "reader", form)
		w := httptest.NewRecorder()
		a.RequireCSRF(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Error("invalid CSRF token reached handler")
		})).ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
		}
	}
}
