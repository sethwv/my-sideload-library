package web

import (
	"archive/zip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sethwv/my-sideload-library/internal/auth"
	"github.com/sethwv/my-sideload-library/internal/index"
	"github.com/sethwv/my-sideload-library/internal/mail"
	"github.com/sethwv/my-sideload-library/internal/tasks"
	"github.com/sethwv/my-sideload-library/internal/users"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()

	store, err := users.Open(filepath.Join(t.TempDir(), "users.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	return &Server{
		Auth:     auth.New("test-session-secret", time.Hour, store),
		Users:    store,
		SiteName: "Test Library",
	}
}

func newAccountTestServer(t *testing.T) *Server {
	t.Helper()

	server := newTestServer(t)
	db, err := index.Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	server.DB = db
	return server
}

func authenticatedRequest(t *testing.T, server *Server, method, target, username string, form url.Values) *http.Request {
	t.Helper()

	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	req := httptest.NewRequest(method, target, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	session := httptest.NewRecorder()
	server.Auth.IssueSession(session, req, username)
	for _, cookie := range session.Result().Cookies() {
		req.AddCookie(cookie)
	}
	return req
}

func addTestTaskManager(t *testing.T, server *Server) {
	t.Helper()
	store, err := tasks.Open(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server.Tasks = tasks.New(store)
	server.Tasks.Register(tasks.Task{Key: "rescan", Name: "Rescan library", Kind: tasks.KindJob, Runnable: true}, func(context.Context) error { return nil })
}

func TestServerRescanQueuesTask(t *testing.T) {
	server := newTestServer(t)
	addTestTaskManager(t, server)
	recorder := httptest.NewRecorder()
	server.ServerRescan(recorder, httptest.NewRequest(http.MethodPost, "/admin/server/rescan", nil))
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusSeeOther)
	}
	if got := recorder.Header().Get("Location"); got != "/admin/tasks" {
		t.Errorf("Location = %q", got)
	}
	history, err := server.Tasks.History(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].TaskKey != "rescan" || history[0].Status != tasks.StatusQueued {
		t.Fatalf("history = %#v, want queued rescan", history)
	}
}

func TestServerInfoDataIncludesRuntimeDetails(t *testing.T) {
	server := newTestServer(t)
	db, err := index.Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	server.DB = db
	server.StartedAt = time.Now().Add(-time.Minute)
	server.BuildVersion = "v1.2.3"
	server.BuildDate = "2026-10-01"

	data, err := server.serverInfoData()
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"Platform", "BuildVersion", "BuildDate", "Goroutines", "MemoryAllocated", "MemoryReserved", "MemoryPercent"} {
		if data[key] == "" || data[key] == nil {
			t.Errorf("server info %s = %v, want a value", key, data[key])
		}
	}
	if got := data["MemoryPercent"].(int); got < 0 || got > 100 {
		t.Errorf("MemoryPercent = %d, want 0 through 100", got)
	}
}

func TestFormatBytes(t *testing.T) {
	for _, test := range []struct {
		value uint64
		want  string
	}{
		{1000, "1000 B"},
		{1024, "1.0 KiB"},
		{1024 * 1024, "1.0 MiB"},
	} {
		if got := formatBytes(test.value); got != test.want {
			t.Errorf("formatBytes(%d) = %q, want %q", test.value, got, test.want)
		}
	}
}

func addTestBook(t *testing.T, db *index.DB) int64 {
	t.Helper()

	library := t.TempDir()
	path := filepath.Join(library, "book.epub")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, content := range map[string]string{
		"META-INF/container.xml": `<?xml version="1.0"?><container><rootfiles><rootfile full-path="content.opf"/></rootfiles></container>`,
		"content.opf":            `<?xml version="1.0"?><package><metadata><title>Test Book</title><creator>Test Author</creator></metadata></package>`,
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Scan([]string{library}, nil); err != nil {
		t.Fatal(err)
	}
	books, err := db.List(index.SortTitle, false, 1, 1, index.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 1 {
		t.Fatalf("indexed books = %d, want 1", len(books))
	}
	return books[0].ID
}

func TestLoginSubmit(t *testing.T) {
	server := newTestServer(t)
	if err := server.Users.Create("reader", "correct-password", users.RoleMember, true, ""); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name         string
		form         url.Values
		wantStatus   int
		wantLocation string
		wantBody     string
		wantSession  bool
	}{
		{
			name: "valid credentials redirect to same-site next",
			form: url.Values{
				"username": {"reader"},
				"password": {"correct-password"},
				"next":     {"/authors?name=Octavia+Butler"},
			},
			wantStatus:   http.StatusSeeOther,
			wantLocation: "/authors?name=Octavia+Butler",
			wantSession:  true,
		},
		{
			name: "valid credentials reject off-site next",
			form: url.Values{
				"username": {"reader"},
				"password": {"correct-password"},
				"next":     {"https://attacker.example"},
			},
			wantStatus:   http.StatusSeeOther,
			wantLocation: "/",
			wantSession:  true,
		},
		{
			name: "invalid credentials render generic error without session",
			form: url.Values{
				"username": {"reader"},
				"password": {"wrong-password"},
				"next":     {"/authors"},
			},
			wantStatus: http.StatusOK,
			wantBody:   "Incorrect username or password.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(tt.form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			recorder := httptest.NewRecorder()

			server.LoginSubmit(recorder, req)

			if recorder.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, tt.wantStatus, recorder.Body.String())
			}
			if got := recorder.Header().Get("Location"); got != tt.wantLocation {
				t.Errorf("Location = %q, want %q", got, tt.wantLocation)
			}
			if tt.wantBody != "" && !strings.Contains(recorder.Body.String(), tt.wantBody) {
				t.Errorf("response missing %q: %s", tt.wantBody, recorder.Body.String())
			}

			cookies := recorder.Result().Cookies()
			hasSession := false
			for _, cookie := range cookies {
				if cookie.Name == auth.CookieName {
					hasSession = true
				}
			}
			if hasSession != tt.wantSession {
				t.Errorf("session cookie = %t, want %t", hasSession, tt.wantSession)
			}
		})
	}
}

func TestSetupHandlers(t *testing.T) {
	t.Run("setup creates and signs in the first administrator", func(t *testing.T) {
		server := newTestServer(t)
		recorder := httptest.NewRecorder()
		server.SetupPage(recorder, httptest.NewRequest(http.MethodGet, "/setup", nil))
		if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `action="/setup"`) {
			t.Fatalf("setup page = %d: %s", recorder.Code, recorder.Body.String())
		}

		form := url.Values{"username": {"admin"}, "password": {"first-password"}}
		recorder = httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		server.SetupSubmit(recorder, req)
		if recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != "/" {
			t.Fatalf("setup submit = %d, location %q", recorder.Code, recorder.Header().Get("Location"))
		}
		if !server.Users.CheckPassword("admin", "first-password") || !server.Users.IsAdmin("admin") {
			t.Error("setup did not create an administrator")
		}
		if len(recorder.Result().Cookies()) == 0 {
			t.Error("setup did not issue a session")
		}
	})

	t.Run("setup rejects invalid input and initialized stores", func(t *testing.T) {
		server := newTestServer(t)
		recorder := httptest.NewRecorder()
		server.SetupSubmit(recorder, httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader("username=admin")))
		if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "username and password are required") {
			t.Fatalf("invalid setup = %d: %s", recorder.Code, recorder.Body.String())
		}
		if err := server.Users.Create("reader", "password", users.RoleMember, true, ""); err != nil {
			t.Fatal(err)
		}
		recorder = httptest.NewRecorder()
		server.SetupPage(recorder, httptest.NewRequest(http.MethodGet, "/setup", nil))
		if recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != "/login" {
			t.Fatalf("initialized setup page = %d, location %q", recorder.Code, recorder.Header().Get("Location"))
		}
		recorder = httptest.NewRecorder()
		server.LoginPage(recorder, httptest.NewRequest(http.MethodGet, "/login", nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("initialized login page = %d", recorder.Code)
		}
	})

	t.Run("login forwards an empty installation to setup", func(t *testing.T) {
		server := newTestServer(t)
		recorder := httptest.NewRecorder()
		server.LoginPage(recorder, httptest.NewRequest(http.MethodGet, "/login", nil))
		if recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != "/setup" {
			t.Fatalf("empty login page = %d, location %q", recorder.Code, recorder.Header().Get("Location"))
		}
	})
}

func TestResetPasswordHandlers(t *testing.T) {
	server := newTestServer(t)
	enablePasswordReset(t, server)
	if err := server.Users.Create("reader", "old-password", users.RoleMember, true, "reader@example.com"); err != nil {
		t.Fatal(err)
	}
	token, _, found, err := server.Users.RequestPasswordReset("reader@example.com")
	if err != nil || !found {
		t.Fatalf("RequestPasswordReset() = (%q, found=%t, err=%v)", token, found, err)
	}

	t.Run("page accepts a valid token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/reset-password?token="+url.QueryEscape(token), nil)
		recorder := httptest.NewRecorder()

		server.ResetPasswordPage(recorder, req)

		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
		}
		if !strings.Contains(recorder.Body.String(), `action="/reset-password"`) {
			t.Errorf("valid token page did not render the reset form: %s", recorder.Body.String())
		}
	})

	t.Run("page rejects an invalid token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/reset-password?token=invalid", nil)
		recorder := httptest.NewRecorder()

		server.ResetPasswordPage(recorder, req)

		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
		}
		if !strings.Contains(recorder.Body.String(), "This reset link is invalid or has expired.") {
			t.Errorf("invalid token page missing error: %s", recorder.Body.String())
		}
		if strings.Contains(recorder.Body.String(), `action="/reset-password"`) {
			t.Errorf("invalid token page rendered the reset form: %s", recorder.Body.String())
		}
	})

	t.Run("submit consumes token and redirects to login", func(t *testing.T) {
		form := url.Values{"token": {token}, "password": {"new-password"}}
		req := httptest.NewRequest(http.MethodPost, "/reset-password", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		recorder := httptest.NewRecorder()

		server.ResetPasswordSubmit(recorder, req)

		if recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != "/login" {
			t.Errorf("response = (%d, %q), want (%d, %q)", recorder.Code, recorder.Header().Get("Location"), http.StatusSeeOther, "/login")
		}
		if !server.Auth.CheckPassword("reader", "new-password") {
			t.Error("reset password was not applied")
		}
		if _, ok := server.Users.VerifyResetToken(token); ok {
			t.Error("reset token remains valid after submission")
		}
	})
}

func TestEmailDependentActionsRejectUnavailableConfiguration(t *testing.T) {
	server := newTestServer(t)
	if err := server.Users.Create("reader", "password", users.RoleMember, true, "reader@example.com"); err != nil {
		t.Fatal(err)
	}

	forgot := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/forgot-password", strings.NewReader(url.Values{"email": {"reader@example.com"}}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	server.ForgotPasswordSubmit(forgot, request)
	if forgot.Code != http.StatusNotFound {
		t.Errorf("forgot-password status = %d, want %d", forgot.Code, http.StatusNotFound)
	}

	invite := httptest.NewRecorder()
	inviteRequest := httptest.NewRequest(http.MethodPost, "/admin/users/invite", strings.NewReader(url.Values{"username": {"invitee"}, "email": {"invitee@example.com"}, "role": {"member"}}.Encode()))
	inviteRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	server.AdminUsersInvite(invite, inviteRequest)
	if _, err := server.Users.UserByUsername("invitee"); err != nil {
		t.Fatal(err)
	} else if user, _ := server.Users.UserByUsername("invitee"); user != nil {
		t.Error("unavailable email created an invited account")
	}
}

func TestInviteAcceptHandlers(t *testing.T) {
	server := newTestServer(t)
	token, err := server.Users.InviteUser("invitee", "invitee@example.com", users.RoleMember, true)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("page accepts a valid token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/invite/accept?token="+url.QueryEscape(token), nil)
		recorder := httptest.NewRecorder()

		server.InviteAcceptPage(recorder, req)

		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
		}
		if !strings.Contains(recorder.Body.String(), `action="/invite/accept"`) {
			t.Errorf("valid token page did not render the invite form: %s", recorder.Body.String())
		}
	})

	t.Run("page rejects an invalid token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/invite/accept?token=invalid", nil)
		recorder := httptest.NewRecorder()

		server.InviteAcceptPage(recorder, req)

		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
		}
		if !strings.Contains(recorder.Body.String(), "This invite link is invalid or has expired.") {
			t.Errorf("invalid token page missing error: %s", recorder.Body.String())
		}
		if strings.Contains(recorder.Body.String(), `action="/invite/accept"`) {
			t.Errorf("invalid token page rendered the invite form: %s", recorder.Body.String())
		}
	})

	t.Run("submit consumes token and redirects to login", func(t *testing.T) {
		form := url.Values{"token": {token}, "password": {"chosen-password"}}
		req := httptest.NewRequest(http.MethodPost, "/invite/accept", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		recorder := httptest.NewRecorder()

		server.InviteAcceptSubmit(recorder, req)

		if recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != "/login" {
			t.Errorf("response = (%d, %q), want (%d, %q)", recorder.Code, recorder.Header().Get("Location"), http.StatusSeeOther, "/login")
		}
		if !server.Auth.CheckPassword("invitee", "chosen-password") {
			t.Error("invite password was not applied")
		}
		if _, ok := server.Users.VerifyInviteToken(token); ok {
			t.Error("invite token remains valid after submission")
		}
	})
}

func TestShelfToggleRejectsAnotherUsersShelf(t *testing.T) {
	server := newTestServer(t)
	if err := server.Users.Create("reader", "reader-password", users.RoleMember, true, ""); err != nil {
		t.Fatal(err)
	}

	db, err := index.Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	server.DB = db

	otherShelfID, err := db.EnsureSystemShelf("other-reader", "favourites", "Favourites")
	if err != nil {
		t.Fatal(err)
	}

	sessionRecorder := httptest.NewRecorder()
	sessionRequest := httptest.NewRequest(http.MethodPost, "/login", nil)
	server.Auth.IssueSession(sessionRecorder, sessionRequest, "reader")
	cookies := sessionRecorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("issued cookies = %d, want 1", len(cookies))
	}

	req := httptest.NewRequest(http.MethodPost, "/books/99/shelves/1", strings.NewReader("next=%2Fauthors"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookies[0])
	req.SetPathValue("id", "99")
	req.SetPathValue("shelfID", strconv.FormatInt(otherShelfID, 10))
	recorder := httptest.NewRecorder()

	server.Auth.RequireAuth(http.HandlerFunc(server.ShelfToggle)).ServeHTTP(recorder, req)

	if recorder.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
}

func TestShelfToggleAddsBookAndRedirects(t *testing.T) {
	server := newTestServer(t)
	if err := server.Users.Create("reader", "reader-password", users.RoleMember, true, ""); err != nil {
		t.Fatal(err)
	}

	db, err := index.Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	server.DB = db
	bookID := addTestBook(t, db)
	shelfID, err := db.EnsureSystemShelf("reader", "favourites", "Favourites")
	if err != nil {
		t.Fatal(err)
	}

	sessionRecorder := httptest.NewRecorder()
	server.Auth.IssueSession(sessionRecorder, httptest.NewRequest(http.MethodPost, "/login", nil), "reader")
	cookies := sessionRecorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("issued cookies = %d, want 1", len(cookies))
	}

	req := httptest.NewRequest(http.MethodPost, "/books/1/shelves/1", strings.NewReader("next=%2Fauthors"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookies[0])
	req.SetPathValue("id", strconv.FormatInt(bookID, 10))
	req.SetPathValue("shelfID", strconv.FormatInt(shelfID, 10))
	recorder := httptest.NewRecorder()

	server.Auth.RequireAuth(http.HandlerFunc(server.ShelfToggle)).ServeHTTP(recorder, req)

	if recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != "/authors" {
		t.Errorf("response = (%d, %q), want (%d, %q)", recorder.Code, recorder.Header().Get("Location"), http.StatusSeeOther, "/authors")
	}
	onShelf, err := db.IsBookOnShelf(shelfID, bookID)
	if err != nil {
		t.Fatal(err)
	}
	if !onShelf {
		t.Error("book was not added to the requested shelf")
	}
}

func TestShelfToggleJSONReturnsRecalculatedRecentShelf(t *testing.T) {
	server := newTestServer(t)
	if err := server.Users.Create("reader", "reader-password", users.RoleMember, true, ""); err != nil {
		t.Fatal(err)
	}

	db, err := index.Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	server.DB = db
	bookID := addTestBook(t, db)
	shelf, err := db.CreateShelf("reader", "Reading", 25)
	if err != nil {
		t.Fatal(err)
	}

	sessionRecorder := httptest.NewRecorder()
	server.Auth.IssueSession(sessionRecorder, httptest.NewRequest(http.MethodPost, "/login", nil), "reader")
	req := httptest.NewRequest(http.MethodPost, "/books/1/shelves/1", nil)
	req.Header.Set("Accept", "application/json")
	req.AddCookie(sessionRecorder.Result().Cookies()[0])
	req.SetPathValue("id", strconv.FormatInt(bookID, 10))
	req.SetPathValue("shelfID", strconv.FormatInt(shelf.ID, 10))
	recorder := httptest.NewRecorder()

	server.Auth.RequireAuth(http.HandlerFunc(server.ShelfToggle)).ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	var response struct {
		OnShelf bool `json:"onShelf"`
		Recent  struct {
			ID      int64  `json:"id"`
			Name    string `json:"name"`
			OnShelf bool   `json:"onShelf"`
		} `json:"recent"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if !response.OnShelf || response.Recent.ID != shelf.ID || response.Recent.Name != "Reading" || !response.Recent.OnShelf {
		t.Errorf("response = %+v, want selected Reading as the recent shelf", response)
	}
}

func TestBookEditMetadataSavePersistsSubmittedFields(t *testing.T) {
	server := newTestServer(t)
	db, err := index.Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	server.DB = db
	bookID := addTestBook(t, db)

	form := url.Values{
		"title":          {"Edited Book"},
		"series":         {"Test Series"},
		"series_index":   {"2.5"},
		"published_date": {"2024-01-02"},
		"description":    {"A complete test description."},
		"genres":         {"Fantasy, Science Fiction"},
		"publisher":      {"Test Publisher"},
		"pages":          {"321"},
		"isbn":           {"9781234567897"},
		"rating":         {"4.5"},
	}
	req := httptest.NewRequest(http.MethodPost, "/books/1/edit-metadata", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", strconv.FormatInt(bookID, 10))
	recorder := httptest.NewRecorder()

	server.BookEditMetadataSave(recorder, req)

	wantLocation := "/books/" + strconv.FormatInt(bookID, 10) + "/edit-metadata"
	if recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != wantLocation {
		t.Errorf("response = (%d, %q), want (%d, %q)", recorder.Code, recorder.Header().Get("Location"), http.StatusSeeOther, wantLocation)
	}
	book, err := db.Get(bookID)
	if err != nil {
		t.Fatal(err)
	}
	if book == nil {
		t.Fatal("saved book was not found")
	}
	if book.Title != "Edited Book" || book.Series != "Test Series" || book.SeriesIndex != 2.5 {
		t.Errorf("saved title/series = (%q, %q, %v)", book.Title, book.Series, book.SeriesIndex)
	}
	if book.PublishedAt != "2024-01-02" || book.Description != "A complete test description." || book.Publisher != "Test Publisher" {
		t.Errorf("saved metadata = (%q, %q, %q)", book.PublishedAt, book.Description, book.Publisher)
	}
	if got, want := strings.Join(book.Genres, ","), "Fantasy,Science Fiction"; got != want {
		t.Errorf("genres = %q, want %q", got, want)
	}
	if book.Pages != 321 || book.ISBN != "9781234567897" || book.Rating != 4.5 {
		t.Errorf("saved numeric metadata = (%d, %q, %v)", book.Pages, book.ISBN, book.Rating)
	}
}

func TestAdminUsersCreateRendersValidationError(t *testing.T) {
	server := newTestServer(t)
	form := url.Values{
		"username": {"reader"},
		"password": {"reader-password"},
		"role":     {"not-a-role"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/users", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()

	server.AdminUsersCreate(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `invalid role &#34;not-a-role&#34;`) {
		t.Errorf("response missing validation error: %s", recorder.Body.String())
	}
	if server.Auth.CheckPassword("reader", "reader-password") {
		t.Error("invalid role submission created a user")
	}
}

func TestAccountPageCombinesPermittedSettings(t *testing.T) {
	server := newAccountTestServer(t)
	if err := server.Users.Create("reader", "reader-password", users.RoleMember, true, ""); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	request := authenticatedRequest(t, server, http.MethodGet, "/account", "reader", nil)
	server.Auth.RequireFull(http.HandlerFunc(server.Account)).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, want := range []string{">Account<", "Change password", "Bookmark link", `action="/account/bookmark/regenerate"`} {
		if !strings.Contains(body, want) {
			t.Errorf("account page missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, `href="/account/email"`) {
		t.Errorf("email tab shown without SMTP: %s", body)
	}
}

func TestAccountShelvesLifecycleRequiresOwnerCapability(t *testing.T) {
	server := newAccountTestServer(t)
	if err := server.Users.Create("reader", "password", users.RoleMember, true, ""); err != nil {
		t.Fatal(err)
	}
	create := server.Auth.RequirePermission(users.PermissionOwnShelves, http.HandlerFunc(server.AccountShelvesCreate))
	form := url.Values{"name": {"Reading Soon"}}
	recorder := httptest.NewRecorder()
	create.ServeHTTP(recorder, authenticatedRequest(t, server, http.MethodPost, "/account/shelves", "reader", form))
	if recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != "/account/shelves" {
		t.Fatalf("create response = (%d, %q), want redirect to account shelves", recorder.Code, recorder.Header().Get("Location"))
	}
	shelves, err := server.DB.ListShelves("reader")
	if err != nil || len(shelves) != 1 || shelves[0].Name != "Reading Soon" || shelves[0].Visibility != "private" {
		t.Fatalf("ListShelves = %+v, %v; want private Reading Soon", shelves, err)
	}
	page := httptest.NewRecorder()
	server.Auth.RequirePermission(users.PermissionOwnShelves, http.HandlerFunc(server.AccountShelves)).ServeHTTP(page, authenticatedRequest(t, server, http.MethodGet, "/account/shelves", "reader", nil))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Your shelves") || !strings.Contains(page.Body.String(), "Reading Soon") || !strings.Contains(page.Body.String(), `aria-label="Manage shelves"`) {
		t.Fatalf("account shelves page did not render created shelf: (%d) %s", page.Code, page.Body.String())
	}
	reader, err := server.Users.UserByUsername("reader")
	if err != nil || reader == nil {
		t.Fatal("reader account missing")
	}
	revoke := false
	if err := server.Users.SetPermissionOverride(reader.ID, users.PermissionOwnShelves, &revoke); err != nil {
		t.Fatal(err)
	}
	forbidden := httptest.NewRecorder()
	create.ServeHTTP(forbidden, authenticatedRequest(t, server, http.MethodPost, "/account/shelves", "reader", form))
	if forbidden.Code != http.StatusForbidden {
		t.Errorf("revoked owner capability response = %d, want forbidden", forbidden.Code)
	}
}

func TestShelfManagerCanManageAnotherUsersMembers(t *testing.T) {
	server := newAccountTestServer(t)
	for _, username := range []string{"owner", "manager", "member"} {
		if err := server.Users.Create(username, "password", users.RoleMember, true, ""); err != nil {
			t.Fatal(err)
		}
	}
	manager, err := server.Users.UserByUsername("manager")
	if err != nil || manager == nil {
		t.Fatal("manager account missing")
	}
	grant := true
	if err := server.Users.SetPermissionOverride(manager.ID, users.PermissionManageShelves, &grant); err != nil {
		t.Fatal(err)
	}
	shelf, err := server.DB.CreateShelf("owner", "Club Picks", 25)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.DB.SetShelfVisibility("owner", shelf.ID, index.ShelfVisibilityShared); err != nil {
		t.Fatal(err)
	}

	add := authenticatedRequest(t, server, http.MethodPost, "/shelves/1/settings/members", "manager", url.Values{"username": {"member"}})
	add.SetPathValue("id", strconv.FormatInt(shelf.ID, 10))
	addRecorder := httptest.NewRecorder()
	server.Auth.RequireAuth(http.HandlerFunc(server.ShelfSettingsMemberAdd)).ServeHTTP(addRecorder, add)
	if addRecorder.Code != http.StatusSeeOther {
		t.Fatalf("manager add status = %d, want %d: %s", addRecorder.Code, http.StatusSeeOther, addRecorder.Body.String())
	}

	remove := authenticatedRequest(t, server, http.MethodPost, "/shelves/1/settings/members/member/delete", "manager", nil)
	remove.SetPathValue("id", strconv.FormatInt(shelf.ID, 10))
	remove.SetPathValue("username", "member")
	removeRecorder := httptest.NewRecorder()
	server.Auth.RequireAuth(http.HandlerFunc(server.ShelfSettingsMemberDelete)).ServeHTTP(removeRecorder, remove)
	if removeRecorder.Code != http.StatusSeeOther {
		t.Fatalf("manager remove status = %d, want %d: %s", removeRecorder.Code, http.StatusSeeOther, removeRecorder.Body.String())
	}
	members, err := server.DB.ListShelfMembersForManager(shelf.ID)
	if err != nil || len(members) != 0 {
		t.Errorf("members after manager remove = %+v, %v; want none", members, err)
	}
}

func enableTestSMTP(t *testing.T, server *Server) {
	t.Helper()
	if err := server.Users.SaveSMTPSettings(mail.Settings{Host: "smtp.example.com", FromAddress: "library@example.com"}); err != nil {
		t.Fatal(err)
	}
}

func enablePasswordReset(t *testing.T, server *Server) {
	t.Helper()
	enableTestSMTP(t, server)
	server.PublicURL = "https://library.example.com"
	if err := server.Users.SaveGeneralSettings(users.GeneralSettings{PasswordResetEnabled: true}); err != nil {
		t.Fatal(err)
	}
}

func TestLoginOnlyShowsEnabledPasswordReset(t *testing.T) {
	server := newTestServer(t)
	if err := server.Users.Create("reader", "reader-password", users.RoleMember, true, "reader@example.com"); err != nil {
		t.Fatal(err)
	}

	hidden := httptest.NewRecorder()
	server.LoginPage(hidden, httptest.NewRequest(http.MethodGet, "/login", nil))
	if strings.Contains(hidden.Body.String(), "Forgot password?") {
		t.Errorf("disabled password reset shown on login: %s", hidden.Body.String())
	}

	enablePasswordReset(t, server)
	enabled := httptest.NewRecorder()
	server.LoginPage(enabled, httptest.NewRequest(http.MethodGet, "/login", nil))
	if !strings.Contains(enabled.Body.String(), "Forgot password?") {
		t.Errorf("enabled password reset missing from login: %s", enabled.Body.String())
	}
}

func TestAccountEmailPageRequiresSMTP(t *testing.T) {
	server := newAccountTestServer(t)
	if err := server.Users.Create("reader", "reader-password", users.RoleMember, true, ""); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	request := authenticatedRequest(t, server, http.MethodGet, "/account/email", "reader", nil)
	server.Auth.RequireFull(http.HandlerFunc(server.AccountEmail)).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != "/account" {
		t.Errorf("response = (%d, %q), want redirect to Account", recorder.Code, recorder.Header().Get("Location"))
	}
}

func TestAccountEmailUpdatesOwnAddress(t *testing.T) {
	server := newAccountTestServer(t)
	enableTestSMTP(t, server)
	if err := server.Users.Create("reader", "reader-password", users.RoleMember, true, "reader@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := server.Users.Create("other", "other-password", users.RoleMember, true, "other@example.com"); err != nil {
		t.Fatal(err)
	}

	page := httptest.NewRecorder()
	server.Auth.RequireFull(http.HandlerFunc(server.AccountEmail)).ServeHTTP(page, authenticatedRequest(t, server, http.MethodGet, "/account/email", "reader", nil))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `href="/account/email" class="is-active"`) || !strings.Contains(page.Body.String(), "New-book digest") {
		t.Errorf("email page = status %d, body %s", page.Code, page.Body.String())
	}

	handler := server.Auth.RequireFull(http.HandlerFunc(server.AccountEmailSubmit))
	success := httptest.NewRecorder()
	handler.ServeHTTP(success, authenticatedRequest(t, server, http.MethodPost, "/account/email", "reader", url.Values{"email": {"new@example.com"}}))
	if success.Code != http.StatusOK || !strings.Contains(success.Body.String(), "Email updated.") {
		t.Errorf("email success response = status %d, body %s", success.Code, success.Body.String())
	}
	reader, err := server.Users.UserByUsername("reader")
	if err != nil || reader == nil || reader.Email != "new@example.com" {
		t.Errorf("reader email = %#v, %v; want new@example.com", reader, err)
	}

	duplicate := httptest.NewRecorder()
	handler.ServeHTTP(duplicate, authenticatedRequest(t, server, http.MethodPost, "/account/email", "reader", url.Values{"email": {"other@example.com"}}))
	if duplicate.Code != http.StatusOK || !strings.Contains(duplicate.Body.String(), "is already in use") {
		t.Errorf("duplicate email response = status %d, body %s", duplicate.Code, duplicate.Body.String())
	}

	clear := httptest.NewRecorder()
	handler.ServeHTTP(clear, authenticatedRequest(t, server, http.MethodPost, "/account/email", "reader", url.Values{"email": {""}}))
	reader, err = server.Users.UserByUsername("reader")
	if clear.Code != http.StatusOK || err != nil || reader == nil || reader.Email != "" {
		t.Errorf("clear email response = status %d, user = %#v, error = %v", clear.Code, reader, err)
	}
}

func TestAccountDigestRedirectsToEmail(t *testing.T) {
	server := newAccountTestServer(t)
	enableTestSMTP(t, server)
	if err := server.Users.Create("reader", "reader-password", users.RoleMember, true, "reader@example.com"); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	handler := server.Auth.RequireFull(http.HandlerFunc(server.AccountDigestToggle))
	handler.ServeHTTP(recorder, authenticatedRequest(t, server, http.MethodPost, "/account/digest", "reader", url.Values{"subscribed": {"on"}}))
	if recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != "/account/email" {
		t.Errorf("response = (%d, %q), want redirect to Email", recorder.Code, recorder.Header().Get("Location"))
	}
	if !server.Users.IsDigestSubscribed("reader") {
		t.Error("digest subscription was not saved")
	}
}

func TestAccountPageHidesBookmarkSettingsWithoutPermission(t *testing.T) {
	server := newAccountTestServer(t)
	if err := server.Users.Create("reader", "reader-password", users.RoleMember, false, ""); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	request := authenticatedRequest(t, server, http.MethodGet, "/account", "reader", nil)
	server.Auth.RequireFull(http.HandlerFunc(server.Account)).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if strings.Contains(recorder.Body.String(), "<h2>Bookmark link</h2>") {
		t.Errorf("bookmark settings shown without permission: %s", recorder.Body.String())
	}
}

func TestAccountPasswordSubmitShowsValidationAndSuccess(t *testing.T) {
	server := newAccountTestServer(t)
	if err := server.Users.Create("reader", "reader-password", users.RoleMember, true, ""); err != nil {
		t.Fatal(err)
	}
	handler := server.Auth.RequireFull(http.HandlerFunc(server.AccountPasswordSubmit))

	incorrect := httptest.NewRecorder()
	handler.ServeHTTP(incorrect, authenticatedRequest(t, server, http.MethodPost, "/account/password", "reader", url.Values{
		"current_password": {"wrong-password"},
		"new_password":     {"new-reader-password"},
	}))
	if incorrect.Code != http.StatusOK || !strings.Contains(incorrect.Body.String(), "Current password is incorrect.") {
		t.Errorf("incorrect password response = status %d, body %s", incorrect.Code, incorrect.Body.String())
	}

	success := httptest.NewRecorder()
	handler.ServeHTTP(success, authenticatedRequest(t, server, http.MethodPost, "/account/password", "reader", url.Values{
		"current_password": {"reader-password"},
		"new_password":     {"new-reader-password"},
	}))
	if success.Code != http.StatusOK || !strings.Contains(success.Body.String(), "Password updated.") {
		t.Errorf("successful password response = status %d, body %s", success.Code, success.Body.String())
	}
	if !server.Auth.CheckPassword("reader", "new-reader-password") {
		t.Error("password was not updated")
	}
}

func TestAccountRoutesRequireFullSession(t *testing.T) {
	server := newAccountTestServer(t)
	if err := server.Users.Create("reader", "reader-password", users.RoleMember, true, ""); err != nil {
		t.Fatal(err)
	}
	token, err := server.Users.GenerateBookmarkToken("reader")
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name    string
		method  string
		target  string
		handler http.Handler
	}{
		{"account", http.MethodGet, "/account", server.Auth.RequireFull(http.HandlerFunc(server.Account))},
		{"email", http.MethodGet, "/account/email", server.Auth.RequireFull(http.HandlerFunc(server.AccountEmail))},
		{"legacy password", http.MethodGet, "/account/password", server.Auth.RequireFull(http.HandlerFunc(server.AccountRedirect))},
		{"legacy bookmark", http.MethodGet, "/account/bookmark", server.Auth.RequireFull(http.HandlerFunc(server.AccountRedirect))},
		{"email submit", http.MethodPost, "/account/email", server.Auth.RequireFull(http.HandlerFunc(server.AccountEmailSubmit))},
		{"password submit", http.MethodPost, "/account/password", server.Auth.RequireFull(http.HandlerFunc(server.AccountPasswordSubmit))},
		{"digest", http.MethodPost, "/account/digest", server.Auth.RequireFull(http.HandlerFunc(server.AccountDigestToggle))},
		{"bookmark regenerate", http.MethodPost, "/account/bookmark/regenerate", server.Auth.RequireFull(http.HandlerFunc(server.AccountBookmarkRegenerate))},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			req := httptest.NewRequest(test.method, test.target+"?token="+url.QueryEscape(token), nil)
			test.handler.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusSeeOther || !strings.HasPrefix(recorder.Header().Get("Location"), "/login?next=") {
				t.Errorf("status = %d, location = %q; want login step-up", recorder.Code, recorder.Header().Get("Location"))
			}
		})
	}
}

func TestAccountRedirect(t *testing.T) {
	server := newAccountTestServer(t)
	recorder := httptest.NewRecorder()
	server.AccountRedirect(recorder, httptest.NewRequest(http.MethodGet, "/account/password", nil))
	if recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != "/account" {
		t.Errorf("status = %d, location = %q; want redirect to /account", recorder.Code, recorder.Header().Get("Location"))
	}
}
