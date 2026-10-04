package web

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sethwv/my-sideload-library/internal/tasks"
)

func TestRenderIncludesBuildVersion(t *testing.T) {
	recorder := httptest.NewRecorder()
	render(recorder, "login.html", map[string]any{
		"Title":        "Log in",
		"SiteName":     "sideload-library",
		"BuildVersion": "v1.2.3",
		"BuildDate":    "2026-08-15",
	})

	if recorder.Code != 200 {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), `href="https://github.com/sethwv/my-sideload-library/tree/v1.2.3"`) || !strings.Contains(recorder.Body.String(), "v1.2.3</a> 08-15-2026") || !strings.Contains(recorder.Body.String(), `rel="icon" type="image/svg+xml" href="/static/favicon.svg"`) || strings.Contains(recorder.Body.String(), ">Source<") {
		t.Errorf("response does not contain the build metadata: %s", recorder.Body.String())
	}
}

func TestShortAuthorName(t *testing.T) {
	for _, test := range []struct {
		name string
		want string
	}{
		{name: "William Wordsworth", want: "W. Wordsworth"},
		{name: "Octavia E. Butler", want: "O. E. Butler"},
		{name: "J. R. R. Tolkien", want: "J. R. R. Tolkien"},
		{name: "Plato", want: "Plato"},
	} {
		if got := shortAuthorName(test.name); got != test.want {
			t.Errorf("shortAuthorName(%q) = %q, want %q", test.name, got, test.want)
		}
	}
}

func TestPageTemplateClonesCachedBase(t *testing.T) {
	login, err := pageTemplate("login.html")
	if err != nil {
		t.Fatal(err)
	}
	library, err := pageTemplate("library.html")
	if err != nil {
		t.Fatal(err)
	}
	if login == library {
		t.Fatal("pageTemplate returned a shared template")
	}
	if login.Lookup("content") == library.Lookup("content") {
		t.Fatal("page templates share the content definition")
	}
}

func TestRenderConcurrentPages(t *testing.T) {
	var wg sync.WaitGroup
	errs := make(chan string, 20)
	for i := 0; i < cap(errs); i++ {
		wg.Add(1)
		go func(page string) {
			defer wg.Done()
			recorder := httptest.NewRecorder()
			render(recorder, page, map[string]any{"Title": "Test", "SiteName": "Test"})
			if recorder.Code != 200 {
				errs <- recorder.Body.String()
			}
		}("login.html")
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent render failed: %s", err)
	}
}

func TestRenderAdminIntegrationsTabsAndForms(t *testing.T) {
	tests := []struct {
		name           string
		provider       string
		wantTab        string
		wantPanel      string
		wantFormAction string
		wantInput      string
	}{
		{
			name:           "hardcover",
			provider:       "hardcover",
			wantTab:        `href="/admin/integrations?provider=hardcover" aria-current="page" class="is-active"`,
			wantPanel:      `class="integration-card" aria-labelledby="hardcover-heading"`,
			wantFormAction: `action="/admin/integrations/hardcover"`,
			wantInput:      `name="hardcover_token"`,
		},
		{
			name:           "chaptarr",
			provider:       "chaptarr",
			wantTab:        `href="/admin/integrations?provider=chaptarr" aria-current="page" class="is-active"`,
			wantPanel:      `class="integration-card is-first-tab-active" aria-labelledby="chaptarr-heading"`,
			wantFormAction: `action="/admin/integrations/chaptarr"`,
			wantInput:      `name="chaptarr_api_key"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			render(recorder, "admin_integrations.html", map[string]any{
				"Title":         "Enrichment",
				"SiteName":      "Test Library",
				"EnrichmentTab": tt.provider,
			})

			if recorder.Code != 200 {
				t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
			}
			body := recorder.Body.String()
			for _, want := range []string{`aria-label="Enhancement providers"`, tt.wantTab, tt.wantPanel, tt.wantFormAction, tt.wantInput} {
				if !strings.Contains(body, want) {
					t.Errorf("response missing %q: %s", want, body)
				}
			}
			if strings.Contains(body, "Back to library") {
				t.Errorf("response retains removed navigation: %s", body)
			}
		})
	}
}

func TestRenderAdminServerIncludesRuntimeDetails(t *testing.T) {
	recorder := httptest.NewRecorder()
	render(recorder, "admin_server.html", map[string]any{
		"Title": "Manage Server", "SiteName": "Test Library", "CanManageServer": true,
		"Platform": "darwin/arm64", "BuildVersion": "v1.2.3", "BuildDate": "2026-10-01",
		"GoVersion": "go1.25.7", "Goroutines": 12, "MemoryAllocated": "12.0 MiB",
		"MemoryReserved": "20.0 MiB", "MemoryPercent": 60,
	})
	if recorder.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, want := range []string{"Platform", "Build version", "Goroutines", "Memory (Go runtime)", "resource-bar-fill"} {
		if !strings.Contains(body, want) {
			t.Errorf("response missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, "Last scan") {
		t.Errorf("response retains removed last-scan row: %s", body)
	}
}

func TestTemplatesDoNotContainBackToLibraryLinks(t *testing.T) {
	for _, name := range []string{
		"account.html",
		"account_email.html",
		"admin_integrations.html",
		"admin_server.html",
		"admin_tasks.html",
		"admin_settings.html",
		"admin_smtp.html",
		"admin_users.html",
		"book_edit_metadata.html",
		"name_index.html",
	} {
		t.Run(name, func(t *testing.T) {
			body, err := templatesFS.ReadFile("templates/" + name)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(body), "Back to library") {
				t.Errorf("%s retains Back to library link", name)
			}
		})
	}
}

func TestAdminTabsIncludeTasks(t *testing.T) {
	recorder := httptest.NewRecorder()
	render(recorder, "admin_tasks.html", map[string]any{"Title": "Tasks", "SiteName": "Test Library", "CanManageServer": true})
	if recorder.Code != 200 {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	body := recorder.Body.String()
	for _, want := range []string{"href=\"/admin/tasks\"", "History", "Next run", "No task runs yet."} {
		if !strings.Contains(body, want) {
			t.Errorf("response missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, "Services") {
		t.Errorf("response retains removed services table: %s", body)
	}
}

func TestAdminTabsUseRequestedOrderAndLabels(t *testing.T) {
	recorder := httptest.NewRecorder()
	render(recorder, "admin_tasks.html", map[string]any{
		"Title": "Tasks", "SiteName": "Test Library", "CanManageServer": true, "CanManageUsers": true,
	})
	if recorder.Code != 200 {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	body := recorder.Body.String()
	for _, want := range []string{"Server", "Setup", "Users", "Tasks", "SMTP", "Enhancement"} {
		if !strings.Contains(body, ">"+want+"<") {
			t.Errorf("response missing %q: %s", want, body)
		}
	}
	last := -1
	for _, want := range []string{">Server<", ">Setup<", ">Users<", ">Tasks<", ">SMTP<", ">Enhancement<"} {
		position := strings.Index(body, want)
		if position <= last {
			t.Errorf("admin tab %q is out of order: %s", want, body)
		}
		last = position
	}
}

func TestRelativeTime(t *testing.T) {
	if got := relativeTime(time.Now().Add(-2 * time.Hour)); got != "2h ago" {
		t.Errorf("relativeTime() = %q, want 2h ago", got)
	}
	if got := relativeTime(time.Now().Add(2 * time.Hour)); got != "in 2h" {
		t.Errorf("relativeTime() = %q, want in 2h", got)
	}
}

func TestRenderTasksUsesTextScheduleWithoutNextRunTime(t *testing.T) {
	recorder := httptest.NewRecorder()
	render(recorder, "admin_tasks.html", map[string]any{
		"Title": "Tasks", "SiteName": "Test Library", "CanManageServer": true,
		"Tasks": []tasks.Summary{{Task: tasks.Task{Name: "Scan Library", NextRun: "Startup & On-Demand", Runnable: true}, Status: "idle"}},
	})
	if !strings.Contains(recorder.Body.String(), "Startup &amp; On-Demand") {
		t.Errorf("response missing text schedule: %s", recorder.Body.String())
	}
}

func TestButtonLinksHaveNoTextDecorationAndVisibleFocus(t *testing.T) {
	body, err := staticFS.ReadFile("static/style.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(body)
	for _, want := range []string{
		".btn, .btn:hover, .btn:focus, .btn:active,",
		".btn-download, .btn-download:hover, .btn-download:focus, .btn-download:active,",
		".btn-hardcover, .btn-hardcover:hover, .btn-hardcover:focus, .btn-hardcover:active,",
		".shelf-toggle, .shelf-toggle:hover, .shelf-toggle:focus, .shelf-toggle:active {",
		"text-decoration: none;",
		".btn:focus, .btn-download:focus, .btn-hardcover:focus, .shelf-toggle:focus {",
		"outline: 2px solid #111;",
		".topnav a:hover { color: #111; text-decoration: none; }",
		".admin-tabs a.is-active, .account-tabs a.is-active {\n  color: #111;\n  font-weight: 600;\n  border-color: #ddd;\n  border-bottom-color: #fff;\n  background: transparent;",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("stylesheet missing %q", want)
		}
	}
}

func TestModalDisablesLibrarySearchAndShelfQuickControlsUseEditableShelves(t *testing.T) {
	modal, err := staticFS.ReadFile("static/modal.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`function setLibrarySearchDisabled(disabled)`,
		`document.getElementById("search-submit")`,
		`document.getElementById("q")`,
		`setLibrarySearchDisabled(true)`,
		`setLibrarySearchDisabled(false)`,
		`librarySearchParent.removeChild(search)`,
		`document.createElement("div")`,
		`search.getAttribute("placeholder")`,
		`submit ? submit.offsetHeight : search.offsetHeight`,
		`librarySearchParent.insertBefore(librarySearch, librarySearchPlaceholder)`,
		`recentButton.getAttribute("data-shelf-id") === String(shelfId)`,
	} {
		if !strings.Contains(string(modal), want) {
			t.Errorf("modal script missing %q", want)
		}
	}

	partial, err := templatesFS.ReadFile("templates/partials.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`EditableShelfCount`,
		`class="shelf-quick-action" disabled>Recent`,
		`shelf-selection-icon`,
		`aria-label="More shelves"`,
	} {
		if !strings.Contains(string(partial), want) {
			t.Errorf("shelf controls missing %q", want)
		}
	}
}

func TestBookPopupUsesAnAccessibleMetadataEditIcon(t *testing.T) {
	body, err := templatesFS.ReadFile("templates/partials.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`class="modal-title-action"`,
		`aria-label="Edit metadata for {{.Title}}"`,
		`href="/books/{{.ID}}/edit-metadata"`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("book popup template missing %q", want)
		}
	}
}

func TestBaseDataIncludesBuildVersion(t *testing.T) {
	server := &Server{BuildVersion: "v1.2.3", BuildDate: "2026-08-15"}
	data, _, err := server.baseData(httptest.NewRequest("GET", "/login", nil))
	if err != nil {
		t.Fatalf("baseData() error = %v", err)
	}
	if got := data["BuildVersion"]; got != "v1.2.3" {
		t.Errorf("BuildVersion = %v, want v1.2.3", got)
	}
	if got := data["BuildDate"]; got != "2026-08-15" {
		t.Errorf("BuildDate = %v, want 2026-08-15", got)
	}
}

func TestFormatBuildDate(t *testing.T) {
	if got := formatBuildDate("2026-08-15"); got != "08-15-2026" {
		t.Errorf("formatBuildDate() = %q, want 08-15-2026", got)
	}
}

func TestSourceURL(t *testing.T) {
	if got := sourceURL("main-8edeb01"); got != "https://github.com/sethwv/my-sideload-library/tree/8edeb01" {
		t.Errorf("sourceURL() = %q", got)
	}
	if got := sourceURL("main-8edeb01 (dirty)"); got != "https://github.com/sethwv/my-sideload-library/tree/8edeb01" {
		t.Errorf("sourceURL() = %q", got)
	}
}

func TestWithQueryParam(t *testing.T) {
	tests := []struct {
		name     string
		rawURL   string
		key      string
		value    any
		wantPath string
		wantVals map[string]string
	}{
		{
			name:     "adds param to a bare path",
			rawURL:   "/",
			key:      "book",
			value:    42,
			wantPath: "/",
			wantVals: map[string]string{"book": "42"},
		},
		{
			name:     "adds param alongside existing query string",
			rawURL:   "/authors?name=P.C.+Cast&sort=title",
			key:      "book",
			value:    7,
			wantPath: "/authors",
			wantVals: map[string]string{"book": "7", "name": "P.C. Cast", "sort": "title"},
		},
		{
			name:     "replaces an existing value for the same key",
			rawURL:   "/?book=1",
			key:      "book",
			value:    2,
			wantPath: "/",
			wantVals: map[string]string{"book": "2"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := withQueryParam(tt.rawURL, tt.key, tt.value)
			parsed, err := url.Parse(got)
			if err != nil {
				t.Fatalf("withQueryParam(%q, %q, %v) = %q, not a valid URL: %v", tt.rawURL, tt.key, tt.value, got, err)
			}
			if parsed.Path != tt.wantPath {
				t.Errorf("path = %q, want %q (from %q)", parsed.Path, tt.wantPath, got)
			}
			for k, want := range tt.wantVals {
				if v := parsed.Query().Get(k); v != want {
					t.Errorf("query[%q] = %q, want %q (from %q)", k, v, want, got)
				}
			}
		})
	}
}

func TestWithQueryParam_InvalidURLReturnsUnchanged(t *testing.T) {
	// A control character makes url.Parse fail — withQueryParam must not
	// panic, just hand back the input unchanged.
	bad := "/\x7f"
	if got := withQueryParam(bad, "book", 1); got != bad {
		t.Errorf("withQueryParam(%q, ...) = %q, want the input unchanged on a parse failure", bad, got)
	}
}

func TestSafeNext(t *testing.T) {
	for _, tt := range []struct {
		next string
		want string
	}{
		{"/authors?name=Octavia+Butler", "/authors?name=Octavia+Butler"},
		{"", "/"},
		{"https://attacker.example", "/"},
		{"//attacker.example", "/"},
		{"/\\attacker.example", "/"},
		{"\\\\attacker.example", "/"},
		{"/%2f%2fattacker.example", "/"},
	} {
		if got := safeNext(tt.next); got != tt.want {
			t.Errorf("safeNext(%q) = %q, want %q", tt.next, got, tt.want)
		}
	}
}

func TestRenderLibraryPager(t *testing.T) {
	tests := []struct {
		name            string
		page            int
		totalPages      int
		pages           []int
		wantPrevious    bool
		wantNext        bool
		wantSelectedOpt string
	}{
		{
			name:            "first page",
			page:            1,
			totalPages:      3,
			pages:           []int{1, 2, 3},
			wantNext:        true,
			wantSelectedOpt: `<option value="1" selected>Page 1 of 3</option>`,
		},
		{
			name:            "last page",
			page:            3,
			totalPages:      3,
			pages:           []int{1, 2, 3},
			wantPrevious:    true,
			wantSelectedOpt: `<option value="3" selected>Page 3 of 3</option>`,
		},
		{
			name:            "empty list has one page",
			page:            1,
			totalPages:      1,
			pages:           []int{1},
			wantSelectedOpt: `<option value="1" selected>Page 1 of 1</option>`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			render(recorder, "library.html", map[string]any{
				"Title":      "Library",
				"SiteName":   "sideload-library",
				"Action":     "/authors",
				"Sort":       "author",
				"Dir":        "asc",
				"Query":      "Moby Dick",
				"Name":       "Herman Melville",
				"Page":       tt.page,
				"PrevPage":   tt.page - 1,
				"NextPage":   tt.page + 1,
				"HasNext":    tt.wantNext,
				"TotalPages": tt.totalPages,
				"Pages":      tt.pages,
			})

			if recorder.Code != 200 {
				t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
			}
			body := recorder.Body.String()
			if !strings.Contains(body, tt.wantSelectedOpt) {
				t.Errorf("page select missing selected option %q", tt.wantSelectedOpt)
			}
			if got := strings.Contains(body, `aria-label="Previous page"`); got != tt.wantPrevious {
				t.Errorf("previous link = %t, want %t", got, tt.wantPrevious)
			}
			if got := strings.Contains(body, `aria-label="Next page"`); got != tt.wantNext {
				t.Errorf("next link = %t, want %t", got, tt.wantNext)
			}
			if !strings.Contains(body, `onchange="this.form.submit()"`) {
				t.Error("page select does not submit on selection")
			}
			if tt.wantPrevious || tt.wantNext {
				if !strings.Contains(body, `class="pagination-icon"`) {
					t.Error("pager does not render SVG chevrons")
				}
				if !strings.Contains(body, `href="/authors?`) {
					t.Error("pager does not render a navigation link")
				}
			}
		})
	}
}

func TestPageURLPreservesListView(t *testing.T) {
	raw := pageURL("/authors", "author", "asc", 2, "Moby Dick", "Herman Melville")
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("pageURL() = %q, not a valid URL: %v", raw, err)
	}
	if parsed.Path != "/authors" {
		t.Errorf("path = %q, want /authors", parsed.Path)
	}
	for key, want := range map[string]string{
		"dir":  "asc",
		"name": "Herman Melville",
		"page": "2",
		"q":    "Moby Dick",
		"sort": "author",
	} {
		if got := parsed.Query().Get(key); got != want {
			t.Errorf("query[%q] = %q, want %q", key, got, want)
		}
	}
}
