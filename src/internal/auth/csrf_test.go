package auth

import (
	"bytes"
	"mime/multipart"
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

func TestRequireCSRFAcceptsMultipartFormToken(t *testing.T) {
	a := testAuthenticator(t)
	initial := csrfRequest(t, a, "reader", url.Values{})

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField(csrfFormField, a.CSRFToken(initial)); err != nil {
		t.Fatal(err)
	}
	file, err := writer.CreateFormFile("file", "goodreads.csv")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("Book Id,Title\n1,Example\n")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/account/connections/goodreads", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	for _, cookie := range initial.Cookies() {
		req.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	a.RequireCSRF(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Fatal(err)
		}
		file.Close()
		if header.Filename != "goodreads.csv" {
			t.Errorf("filename = %q, want goodreads.csv", header.Filename)
		}
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNoContent)
	}
}

func TestRequireCSRFAcceptsHeaderToken(t *testing.T) {
	a := testAuthenticator(t)
	req := csrfRequest(t, a, "reader", url.Values{})
	req.Header.Set("X-CSRF-Token", a.CSRFToken(req))

	w := httptest.NewRecorder()
	a.RequireCSRF(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNoContent)
	}
}

func TestRequireCSRFAcceptsHeaderTokenWithoutContentType(t *testing.T) {
	a := testAuthenticator(t)
	req := csrfRequest(t, a, "reader", url.Values{})
	req.Header.Del("Content-Type")
	req.Header.Set("X-CSRF-Token", a.CSRFToken(req))

	w := httptest.NewRecorder()
	a.RequireCSRF(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNoContent)
	}
}
