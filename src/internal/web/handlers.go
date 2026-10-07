package web

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/sethwv/my-sideload-library/internal/auth"
	"github.com/sethwv/my-sideload-library/internal/chaptarr"
	"github.com/sethwv/my-sideload-library/internal/connections"
	"github.com/sethwv/my-sideload-library/internal/hardcover"
	"github.com/sethwv/my-sideload-library/internal/index"
	"github.com/sethwv/my-sideload-library/internal/mail"
	"github.com/sethwv/my-sideload-library/internal/tasks"
	"github.com/sethwv/my-sideload-library/internal/thumbnail"
	"github.com/sethwv/my-sideload-library/internal/users"
)

type Server struct {
	Auth         *auth.Authenticator
	DB           *index.DB
	Covers       *thumbnail.Store
	Users        *users.Store
	Hardcover    *hardcover.Client // nil-safe: Enabled() is false with no token, callers check before use
	Chaptarr     *chaptarr.Client  // nil-safe: Enabled() is false with no URL/key, callers check before use
	LibraryPaths []string
	DataDir      string
	PageSize     int
	SiteName     string
	PublicURL    string // trusted base URL for emailed links; see config.Config.PublicURL
	StartedAt    time.Time
	BuildVersion string
	BuildDate    string
	Tasks        *tasks.Manager
	RateLimiter  *RateLimiter
}

const favoritesSlug = "favourites"
const favoritesName = "Favourites"

// baseData returns template data common to every page that renders the
// shared topbar (Username, IsAdmin, Shelves, SiteName, CurrentURL), ready
// to be merged into a handler's page-specific map via mergeInto. shelves is
// and is also returned for page-specific shelf state.
func (s *Server) baseData(r *http.Request) (data map[string]any, shelves []index.Shelf, err error) {
	username, _ := auth.UsernameFromContext(r.Context())
	isAdmin := false
	canManageUsers := false
	canManageServer := false
	canBookmark := false
	canOwnShelves := false
	canManageShelves := false
	adminURL := ""
	var visibleShelves []index.ShelfAccess
	if username != "" {
		// A restricted (bookmark-token) session shouldn't be offered admin
		// links even if the account has them, since reaching /admin/* still
		// redirects to a password step-up regardless, but hiding the links
		// keeps the UI honest about the "view & shelves only" state.
		full := !auth.IsRestricted(r.Context())
		isAdmin = s.Users.IsAdmin(username) && full
		canManageUsers = s.Users.CanManageUsers(username) && full
		canManageServer = s.Users.CanManageServer(username) && full
		canBookmark = s.Users.CanUseBookmark(username)
		canOwnShelves = s.Users.Can(username, users.PermissionOwnShelves) && full
		canManageShelves = s.Users.Can(username, users.PermissionManageShelves) && full
		switch {
		case canManageServer:
			adminURL = "/admin/server"
		case canManageUsers:
			adminURL = "/admin/users"
		case canManageShelves:
			adminURL = "/admin/shelves"
		}
		if _, err = s.DB.EnsureSystemShelf(username, favoritesSlug, favoritesName); err != nil {
			return nil, nil, err
		}
		if shelves, err = s.DB.ListEditableShelves(username); err != nil {
			return nil, nil, err
		}
		if visibleShelves, err = s.DB.ListVisibleShelves(username); err != nil {
			return nil, nil, err
		}
	}
	kepubSettings := users.KepubSettings{Enabled: true}
	if s.Users != nil {
		kepubSettings, err = s.Users.GetKepubSettings()
		if err != nil {
			return nil, nil, err
		}
	}

	csrfToken := ""
	if s.Auth != nil {
		csrfToken = s.Auth.CSRFToken(r)
	}
	data = map[string]any{
		"Username":         username,
		"IsAdmin":          isAdmin,
		"CanManageUsers":   canManageUsers,
		"CanManageServer":  canManageServer,
		"CanBookmark":      canBookmark,
		"CanOwnShelves":    canOwnShelves,
		"CanManageShelves": canManageShelves,
		"AdminURL":         adminURL,
		"Restricted":       auth.IsRestricted(r.Context()),
		"Shelves":          shelves,
		"VisibleShelves":   visibleShelves,
		"SiteName":         s.SiteName,
		"CurrentURL":       r.URL.RequestURI(),
		"BuildVersion":     s.BuildVersion,
		"BuildDate":        s.BuildDate,
		"KepubEnabled":     kepubSettings.Enabled,
		"CSRFToken":        csrfToken,
	}
	return data, shelves, nil
}

// mergeInto copies every key from src into dst, overwriting on conflict.
func mergeInto(dst, src map[string]any) {
	for k, v := range src {
		dst[k] = v
	}
}

// safeNext restricts post-login redirect targets to same-site relative paths.
func safeNext(next string) string {
	// Browsers treat backslashes as path separators in URLs, so normalize them
	// before checking for protocol-relative URLs such as /\example.com.
	next = strings.ReplaceAll(next, "\\", "/")
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || !strings.HasPrefix(u.Path, "/") || strings.HasPrefix(u.Path, "//") {
		return "/"
	}
	return u.String()
}

func (s *Server) LoginPage(w http.ResponseWriter, r *http.Request) {
	hasUsers, err := s.Users.HasUsers()
	if err != nil {
		http.Error(w, "failed to load users", http.StatusInternalServerError)
		return
	}
	if !hasUsers {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	base, _, err := s.baseData(r)
	if err != nil {
		http.Error(w, "failed to load page", http.StatusInternalServerError)
		return
	}
	data := map[string]any{
		"Title":                  "Log in",
		"Next":                   safeNext(r.URL.Query().Get("next")),
		"PasswordResetAvailable": s.passwordResetAvailable(),
	}
	mergeInto(data, base)
	render(w, "login.html", data)
}

func (s *Server) LoginSubmit(w http.ResponseWriter, r *http.Request) {
	hasUsers, err := s.Users.HasUsers()
	if err != nil {
		http.Error(w, "failed to load users", http.StatusInternalServerError)
		return
	}
	if !hasUsers {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	username := r.FormValue("username")
	password := r.FormValue("password")
	next := safeNext(r.FormValue("next"))
	usernameKey := rateLimitIdentity(username)
	ip := requestIP(r)
	if !s.RateLimiter.allowed("login-ip", ip, loginAttemptLimit, loginAttemptWindow) || !s.RateLimiter.allowed("login-username", usernameKey, loginAttemptLimit, loginAttemptWindow) {
		http.Error(w, "too many login attempts", http.StatusTooManyRequests)
		return
	}

	if !s.Auth.CheckPassword(username, password) {
		s.RateLimiter.record("login-ip", ip, loginAttemptWindow)
		s.RateLimiter.record("login-username", usernameKey, loginAttemptWindow)
		base, _, _ := s.baseData(r)
		data := map[string]any{
			"Title":                  "Log in",
			"Error":                  "Incorrect username or password.",
			"Next":                   next,
			"PasswordResetAvailable": s.passwordResetAvailable(),
		}
		mergeInto(data, base)
		render(w, "login.html", data)
		return
	}
	s.RateLimiter.clear("login-ip", ip)
	s.RateLimiter.clear("login-username", usernameKey)

	s.Auth.IssueSession(w, r, username)

	target, err := url.Parse(next)
	if err != nil || target.Scheme != "" || target.Hostname() != "" || target.User != nil || !strings.HasPrefix(target.Path, "/") || strings.HasPrefix(target.Path, "//") {
		target = &url.URL{Path: "/"}
	}
	http.Redirect(w, r, target.String(), http.StatusSeeOther)
}

func (s *Server) emailAvailable() bool {
	settings, err := s.Users.GetSMTPSettings()
	return err == nil && settings.Enabled() && s.PublicURL != ""
}

func (s *Server) passwordResetAvailable() bool {
	settings, err := s.Users.GetGeneralSettings()
	return err == nil && settings.PasswordResetEnabled && s.emailAvailable()
}

func (s *Server) smtpAvailable() bool {
	settings, err := s.Users.GetSMTPSettings()
	return err == nil && settings.Enabled()
}

func emailUnavailableMessage() string {
	return "Email is unavailable. Configure SMTP and the Public URL in Server settings."
}

func (s *Server) SetupPage(w http.ResponseWriter, r *http.Request) {
	hasUsers, err := s.Users.HasUsers()
	if err != nil {
		http.Error(w, "failed to load users", http.StatusInternalServerError)
		return
	}
	if hasUsers {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	base, _, err := s.baseData(r)
	if err != nil {
		http.Error(w, "failed to load page", http.StatusInternalServerError)
		return
	}
	data := map[string]any{"Title": "Set up administrator"}
	mergeInto(data, base)
	render(w, "setup.html", data)
}

func (s *Server) SetupSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	username := r.FormValue("username")
	password := r.FormValue("password")
	created, err := s.Users.CreateFirstAdmin(username, password)
	if err != nil {
		base, _, _ := s.baseData(r)
		data := map[string]any{"Title": "Set up administrator", "Error": err.Error(), "Username": username}
		mergeInto(data, base)
		render(w, "setup.html", data)
		return
	}
	if !created {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	s.Auth.IssueSession(w, r, username)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) Logout(w http.ResponseWriter, r *http.Request) {
	s.Auth.ClearSession(w, r)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) AdminUsers(w http.ResponseWriter, r *http.Request) {
	list, err := s.Users.List()
	if err != nil {
		http.Error(w, "failed to load users", http.StatusInternalServerError)
		return
	}

	base, _, err := s.baseData(r)
	if err != nil {
		http.Error(w, "failed to load page", http.StatusInternalServerError)
		return
	}

	data := map[string]any{
		"Title":          "Manage Users",
		"AdminTab":       "users",
		"Users":          list,
		"EmailAvailable": s.emailAvailable(),
		"EmailMessage":   emailUnavailableMessage(),
	}
	mergeInto(data, base)
	render(w, "admin_users.html", data)
}

func (s *Server) renderAdminUsersError(w http.ResponseWriter, r *http.Request, errMsg string) {
	list, _ := s.Users.List()
	base, _, _ := s.baseData(r)
	data := map[string]any{
		"Title":          "Manage Users",
		"AdminTab":       "users",
		"Users":          list,
		"Error":          errMsg,
		"EmailAvailable": s.emailAvailable(),
		"EmailMessage":   emailUnavailableMessage(),
	}
	mergeInto(data, base)
	render(w, "admin_users.html", data)
}

func (s *Server) AdminUsersSetEnabled(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if err := s.Users.SetEnabled(id, r.FormValue("enabled") == "true"); err != nil {
		s.renderAdminUsersError(w, r, err.Error())
		return
	}
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

func (s *Server) AdminUsersCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	username := r.FormValue("username")
	password := r.FormValue("password")
	role := r.FormValue("role")
	email := r.FormValue("email")

	if err := s.Users.Create(username, password, role, true, email); err != nil {
		s.renderAdminUsersError(w, r, err.Error())
		return
	}

	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

// AdminUsersSetRole updates an existing user's role and bookmark-link
// permission in place, without deleting/recreating the account.
func (s *Server) AdminUsersSetRole(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	role := r.FormValue("role")
	user, err := s.Users.UserByID(id)
	if err != nil || user == nil {
		http.NotFound(w, r)
		return
	}
	if err := s.Users.SetRole(id, role, s.Users.CanUseBookmark(user.Username)); err != nil {
		s.renderAdminUsersError(w, r, err.Error())
		return
	}

	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

// AdminUsersSetPermission changes an explicit capability override. Only an
// administrator may change overrides, avoiding a user-manager privilege path.
func (s *Server) AdminUsersSetPermission(w http.ResponseWriter, r *http.Request) {
	username, _ := auth.UsernameFromContext(r.Context())
	if !s.Users.IsAdmin(username) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	states, err := s.Users.PermissionStates(id)
	if err != nil {
		s.renderAdminUsersError(w, r, err.Error())
		return
	}
	overrides := make(map[users.Permission]*bool, len(states))
	for _, state := range states {
		var override *bool
		switch r.FormValue("permission_" + string(state.Permission)) {
		case "default":
		case "grant":
			value := true
			override = &value
		case "revoke":
			value := false
			override = &value
		default:
			s.renderAdminUsersError(w, r, "invalid permission override")
			return
		}
		overrides[state.Permission] = override
	}
	if err := s.Users.SetPermissionOverrides(id, overrides); err != nil {
		s.renderAdminUsersError(w, r, err.Error())
		return
	}
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

// AdminUsersSetEmail updates an existing user's email address in place.
func (s *Server) AdminUsersSetEmail(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	if err := s.Users.SetEmail(id, r.FormValue("email")); err != nil {
		s.renderAdminUsersError(w, r, err.Error())
		return
	}

	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

// AdminUsersInvite creates a pending account and emails the invitee a link
// to set their own password.
func (s *Server) AdminUsersInvite(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.emailAvailable() {
		s.renderAdminUsersError(w, r, emailUnavailableMessage())
		return
	}

	username := r.FormValue("username")
	email := r.FormValue("email")
	role := r.FormValue("role")

	token, err := s.Users.InviteUser(username, email, role, true)
	if err != nil {
		s.renderAdminUsersError(w, r, err.Error())
		return
	}

	if err := s.sendInviteEmail(r, email, token); err != nil {
		log.Printf("send invite email to %s: %v", email, err)
		s.renderAdminUsersError(w, r, "user created, but the invite email failed to send: "+err.Error())
		return
	}

	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

// AdminUsersResendInvite reissues a fresh invite token/email for a user
// whose invite is still pending.
func (s *Server) AdminUsersResendInvite(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if !s.emailAvailable() {
		s.renderAdminUsersError(w, r, emailUnavailableMessage())
		return
	}

	user, err := s.Users.UserByID(id)
	if err != nil {
		http.Error(w, "failed to load users", http.StatusInternalServerError)
		return
	}
	if user == nil || user.Email == "" {
		s.renderAdminUsersError(w, r, "user has no email on file")
		return
	}

	token, err := s.Users.ResendInvite(id)
	if err != nil {
		s.renderAdminUsersError(w, r, err.Error())
		return
	}
	if err := s.sendInviteEmail(r, user.Email, token); err != nil {
		log.Printf("resend invite email to %s: %v", user.Email, err)
		s.renderAdminUsersError(w, r, "invite reissued, but the email failed to send: "+err.Error())
		return
	}

	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

func (s *Server) sendInviteEmail(r *http.Request, to, token string) error {
	settings, err := s.Users.GetSMTPSettings()
	if err != nil {
		return err
	}
	if !settings.Enabled() {
		return fmt.Errorf("SMTP is not configured")
	}
	origin, err := s.emailOrigin()
	if err != nil {
		return err
	}
	link := origin + "/invite/accept?token=" + token
	body := fmt.Sprintf(
		"You've been invited to %s.\n\nSet your password to finish creating your account:\n%s\n\nThis link expires in 7 days.",
		s.SiteName, link,
	)
	return mail.Send(settings, to, "You're invited to "+s.SiteName, body)
}

// emailOrigin returns the base URL to use when building a link that leaves
// the server via email (password reset, invite). Unlike siteOrigin, this
// never falls back to the request's Host header: Host is client-controlled,
// and a spoofed Host on an unauthenticated request like /forgot-password
// would otherwise let an attacker put their own domain into a victim's
// password-reset email. Refuses to send rather than guess.
func (s *Server) emailOrigin() (string, error) {
	if s.PublicURL == "" {
		return "", fmt.Errorf("PUBLIC_URL is not configured, refusing to send an email with a login link")
	}
	return s.PublicURL, nil
}

// siteOrigin returns the base URL to use for a link handed straight back to
// the same browser that requested it (e.g. a bookmark-link redirect) rather
// than emailed elsewhere. Falling back to the request's Host header is safe
// here since the result only ever reaches the request's own client, never a
// third party's inbox; still prefers the admin-configured PublicURL when set.
func (s *Server) siteOrigin(r *http.Request) string {
	if s.PublicURL != "" {
		return s.PublicURL
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (s *Server) AdminUsersDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	user, err := s.Users.UserByID(id)
	if err != nil || user == nil {
		http.NotFound(w, r)
		return
	}
	if user.IsAdmin && user.Enabled {
		allUsers, err := s.Users.List()
		if err != nil {
			http.Error(w, "failed to load users", http.StatusInternalServerError)
			return
		}
		enabledAdmins := 0
		for _, candidate := range allUsers {
			if candidate.IsAdmin && candidate.Enabled {
				enabledAdmins++
			}
		}
		if enabledAdmins <= 1 {
			s.renderAdminUsersError(w, r, "cannot delete the last remaining admin")
			return
		}
	}
	if err := s.DB.DeleteUserShelves(user.Username); err != nil {
		s.renderAdminUsersError(w, r, "failed to remove user shelves: "+err.Error())
		return
	}
	if err := s.Users.Delete(id); err != nil {
		s.renderAdminUsersError(w, r, err.Error())
		return
	}

	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

func (s *Server) AdminUsersResetPassword(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	if err := s.Users.ResetPassword(id, r.FormValue("password")); err != nil {
		s.renderAdminUsersError(w, r, err.Error())
		return
	}

	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

// ServerInfo shows admin-only read-only server/library stats.
func (s *Server) serverInfoData() (map[string]any, error) {
	bookCount, err := s.DB.Count(index.Filter{})
	if err != nil {
		return nil, err
	}
	authors, err := s.DB.ListAuthors(index.Filter{})
	if err != nil {
		return nil, err
	}
	series, err := s.DB.ListSeries(index.Filter{})
	if err != nil {
		return nil, err
	}
	userList, err := s.Users.List()
	if err != nil {
		return nil, err
	}
	adminCount := 0
	for _, u := range userList {
		if u.IsAdmin {
			adminCount++
		}
	}

	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	memoryPercent := 0
	if memory.Sys > 0 {
		memoryPercent = int(memory.HeapAlloc * 100 / memory.Sys)
	}

	return map[string]any{
		"Title":           "Manage Server",
		"AdminTab":        "server",
		"Uptime":          time.Since(s.StartedAt).Round(time.Second).String(),
		"Platform":        runtime.GOOS + "/" + runtime.GOARCH,
		"BuildVersion":    s.BuildVersion,
		"BuildDate":       s.BuildDate,
		"GoVersion":       runtime.Version(),
		"Goroutines":      runtime.NumGoroutine(),
		"MemoryAllocated": formatBytes(memory.HeapAlloc),
		"MemoryReserved":  formatBytes(memory.Sys),
		"MemoryPercent":   memoryPercent,
		"LibraryPath":     strings.Join(s.LibraryPaths, ", "),
		"DataDir":         s.DataDir,
		"BookCount":       bookCount,
		"AuthorCount":     len(authors),
		"SeriesCount":     len(series),
		"UserCount":       len(userList),
		"AdminCount":      adminCount,
	}, nil
}

func formatBytes(value uint64) string {
	if value < 1024 {
		return strconv.FormatUint(value, 10) + " B"
	}

	units := []string{"KiB", "MiB", "GiB", "TiB"}
	size := float64(value)
	unit := -1
	for size >= 1024 && unit < len(units)-1 {
		size /= 1024
		unit++
	}
	return fmt.Sprintf("%.1f %s", size, units[unit])
}

func (s *Server) adminSettingsData() (map[string]any, error) {
	general, err := s.Users.GetGeneralSettings()
	if err != nil {
		return nil, err
	}
	kepubSettings, err := s.Users.GetKepubSettings()
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"Title":                     "Setup",
		"AdminTab":                  "settings",
		"SiteName":                  general.SiteName,
		"PublicURL":                 general.PublicURL,
		"CoverWidth":                general.CoverWidth,
		"PageSize":                  general.PageSize,
		"ShelfLimit":                general.ShelfLimit,
		"SessionTTL":                general.SessionTTL.String(),
		"PasswordResetEnabled":      general.PasswordResetEnabled,
		"PasswordResetConfigurable": s.emailAvailable(),
		"KepubEnabled":              kepubSettings.Enabled,
		"KepubWriteCalibreMetadata": kepubSettings.WriteCalibreMetadata,
	}, nil
}

func (s *Server) adminSMTPData() (map[string]any, error) {
	smtp, err := s.Users.GetSMTPSettings()
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"Title":           "SMTP",
		"AdminTab":        "smtp",
		"SMTPHost":        smtp.Host,
		"SMTPPort":        smtp.Port,
		"SMTPEncryption":  smtp.Encryption,
		"SMTPUsername":    smtp.Username,
		"SMTPFromName":    smtp.FromName,
		"SMTPFromAddress": smtp.FromAddress,
		"SMTPConfigured":  smtp.Enabled(),
	}, nil
}

func (s *Server) serverIntegrationsData(provider string) (map[string]any, error) {
	settings, err := s.Users.GetIntegrationSettings()
	if err != nil {
		return nil, err
	}
	enrichmentStats, err := s.DB.GetEnrichmentStats()
	if err != nil {
		return nil, err
	}
	connectionPending, err := s.Users.ConnectionEnrichmentPending()
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"Title":                   "Enhancement",
		"AdminTab":                "integrations",
		"EnrichmentTab":           provider,
		"HardcoverEnabled":        settings.HardcoverEnabled,
		"HardcoverActive":         s.Hardcover.Enabled(),
		"HardcoverConfigured":     settings.HardcoverToken != "",
		"HideNoHardcoverMatch":    settings.HideNoHardcoverMatch,
		"HardcoverOverwriteCover": settings.HardcoverOverwriteCover,
		"ChaptarrEnabled":         settings.ChaptarrEnabled,
		"ChaptarrActive":          s.Chaptarr.Enabled(),
		"ChaptarrURL":             settings.ChaptarrURL,
		"ChaptarrConfigured":      settings.ChaptarrAPIKey != "",
		"HideNoChaptarrMatch":     settings.HideNoChaptarrMatch,
		"EnrichmentPending":       enrichmentStats.Pending,
		"ConnectionPending":       connectionPending,
		"PendingTotal":             enrichmentStats.Pending + connectionPending,
		"EnrichmentDone":          enrichmentStats.Done,
		"EnrichmentNoMatch":       enrichmentStats.NoMatch,
		"EnrichmentErrored":       enrichmentStats.Errored,
	}, nil
}

func (s *Server) ServerInfo(w http.ResponseWriter, r *http.Request) {
	base, _, err := s.baseData(r)
	if err != nil {
		http.Error(w, "failed to load page", http.StatusInternalServerError)
		return
	}
	data, err := s.serverInfoData()
	if err != nil {
		http.Error(w, "failed to load stats", http.StatusInternalServerError)
		return
	}
	mergeInto(data, base)
	render(w, "admin_server.html", data)
}

func (s *Server) AdminSettings(w http.ResponseWriter, r *http.Request) {
	base, _, err := s.baseData(r)
	if err != nil {
		http.Error(w, "failed to load page", http.StatusInternalServerError)
		return
	}
	data, err := s.adminSettingsData()
	if err != nil {
		http.Error(w, "failed to load settings", http.StatusInternalServerError)
		return
	}
	mergeInto(data, base)
	render(w, "admin_settings.html", data)
}

func (s *Server) AdminSMTP(w http.ResponseWriter, r *http.Request) {
	base, _, err := s.baseData(r)
	if err != nil {
		http.Error(w, "failed to load page", http.StatusInternalServerError)
		return
	}
	data, err := s.adminSMTPData()
	if err != nil {
		http.Error(w, "failed to load settings", http.StatusInternalServerError)
		return
	}
	mergeInto(data, base)
	render(w, "admin_smtp.html", data)
}

func (s *Server) renderAdminSettingsError(w http.ResponseWriter, r *http.Request, errMsg, statusMsg string) {
	base, _, err := s.baseData(r)
	if err != nil {
		http.Error(w, "failed to load page", http.StatusInternalServerError)
		return
	}
	data, err := s.adminSettingsData()
	if err != nil {
		http.Error(w, "failed to load settings", http.StatusInternalServerError)
		return
	}
	if errMsg != "" {
		data["Error"] = errMsg
	}
	if statusMsg != "" {
		data["Status"] = statusMsg
	}
	mergeInto(data, base)
	render(w, "admin_settings.html", data)
}

func (s *Server) renderAdminSMTPError(w http.ResponseWriter, r *http.Request, errMsg, statusMsg string) {
	base, _, err := s.baseData(r)
	if err != nil {
		http.Error(w, "failed to load page", http.StatusInternalServerError)
		return
	}
	data, err := s.adminSMTPData()
	if err != nil {
		http.Error(w, "failed to load settings", http.StatusInternalServerError)
		return
	}
	if errMsg != "" {
		data["Error"] = errMsg
	}
	if statusMsg != "" {
		data["Status"] = statusMsg
	}
	mergeInto(data, base)
	render(w, "admin_smtp.html", data)
}

func (s *Server) AdminSettingsGeneralSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	coverWidth, err := strconv.Atoi(r.FormValue("cover_width"))
	if err != nil || coverWidth < 1 {
		s.renderAdminSettingsError(w, r, "cover width must be a positive number", "")
		return
	}
	pageSize, err := strconv.Atoi(r.FormValue("page_size"))
	if err != nil || pageSize < 1 {
		s.renderAdminSettingsError(w, r, "page size must be a positive number", "")
		return
	}
	shelfLimit, err := strconv.Atoi(r.FormValue("shelf_limit"))
	if err != nil || shelfLimit < 1 {
		s.renderAdminSettingsError(w, r, "shelves per user must be a positive number", "")
		return
	}
	sessionTTL, err := time.ParseDuration(r.FormValue("session_ttl"))
	if err != nil || sessionTTL <= 0 {
		s.renderAdminSettingsError(w, r, "session TTL must be a valid duration like 720h", "")
		return
	}

	current, err := s.Users.GetGeneralSettings()
	if err != nil {
		s.renderAdminSettingsError(w, r, "failed to load settings: "+err.Error(), "")
		return
	}
	settings := users.GeneralSettings{
		SiteName:             r.FormValue("site_name"),
		PublicURL:            strings.TrimRight(r.FormValue("public_url"), "/"),
		CoverWidth:           coverWidth,
		PageSize:             pageSize,
		ShelfLimit:           shelfLimit,
		SessionTTL:           sessionTTL,
		PasswordResetEnabled: current.PasswordResetEnabled,
	}
	if s.emailAvailable() {
		settings.PasswordResetEnabled = r.FormValue("password_reset_enabled") == "on"
	}
	if err := s.Users.SaveGeneralSettings(settings); err != nil {
		s.renderAdminSettingsError(w, r, "failed to save settings: "+err.Error(), "")
		return
	}

	s.SiteName = settings.SiteName
	s.PublicURL = settings.PublicURL
	s.PageSize = settings.PageSize
	s.Covers.SetWidth(settings.CoverWidth)
	s.Auth.SetTTL(settings.SessionTTL)

	http.Redirect(w, r, "/admin/settings", http.StatusSeeOther)
}

func (s *Server) AdminSettingsKepubSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if err := s.Users.SaveKepubSettings(users.KepubSettings{
		Enabled:              r.FormValue("enabled") == "on",
		WriteCalibreMetadata: r.FormValue("write_calibre_metadata") == "on",
	}); err != nil {
		s.renderAdminSettingsError(w, r, "failed to save KEPUB settings: "+err.Error(), "")
		return
	}
	http.Redirect(w, r, "/admin/settings", http.StatusSeeOther)
}

func (s *Server) ServerSMTPSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	current, err := s.Users.GetSMTPSettings()
	if err != nil {
		http.Error(w, "failed to load settings", http.StatusInternalServerError)
		return
	}

	port, _ := strconv.Atoi(r.FormValue("port"))
	password := r.FormValue("password")
	if password == "" {
		password = current.Password
	}

	settings := mail.Settings{
		Host:        r.FormValue("host"),
		Port:        port,
		Encryption:  r.FormValue("encryption"),
		Username:    r.FormValue("username"),
		Password:    password,
		FromName:    r.FormValue("from_name"),
		FromAddress: r.FormValue("from_addr"),
	}

	if err := s.Users.SaveSMTPSettings(settings); err != nil {
		s.renderAdminSMTPError(w, r, "failed to save SMTP settings: "+err.Error(), "")
		return
	}

	http.Redirect(w, r, "/admin/smtp", http.StatusSeeOther)
}

func (s *Server) ServerSMTPTest(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	to := r.FormValue("test_email")

	settings, err := s.Users.GetSMTPSettings()
	if err != nil {
		http.Error(w, "failed to load settings", http.StatusInternalServerError)
		return
	}
	if !settings.Enabled() {
		s.renderAdminSMTPError(w, r, "SMTP is not configured yet. Save settings first.", "")
		return
	}

	body := fmt.Sprintf("This is a test email from %s, confirming your SMTP settings work.", s.SiteName)
	if err := mail.Send(settings, to, "Test email from "+s.SiteName, body); err != nil {
		s.renderAdminSMTPError(w, r, "test email failed: "+err.Error(), "")
		return
	}

	s.renderAdminSMTPError(w, r, "", "Test email sent to "+to+".")
}

func (s *Server) ServerRescan(w http.ResponseWriter, r *http.Request) {
	s.enqueueTask(w, r, "rescan")
}

func (s *Server) ServerReimport(w http.ResponseWriter, r *http.Request) {
	s.ServerLibraryClear(w, r)
}

// ServerLibraryClear clears derived library data, then queues a fresh scan.
func (s *Server) ServerLibraryClear(w http.ResponseWriter, r *http.Request) {
	if s.Tasks == nil {
		http.Error(w, "task manager is unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := s.DB.ClearLibrary(); err != nil {
		http.Error(w, "clear library failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if _, _, err := s.Tasks.Enqueue("rescan"); err != nil {
		http.Error(w, "scan could not be queued", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/server", http.StatusSeeOther)
}

func (s *Server) ServerEnrichmentReset(w http.ResponseWriter, r *http.Request) {
	if err := s.DB.ResetEnrichment(); err != nil {
		http.Error(w, "enrichment reset failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/integrations", http.StatusSeeOther)
}

func (s *Server) enqueueTask(w http.ResponseWriter, r *http.Request, key string) {
	if s.Tasks == nil {
		http.Error(w, "task manager is unavailable", http.StatusServiceUnavailable)
		return
	}
	_, _, err := s.Tasks.Enqueue(key)
	if err != nil {
		http.Error(w, "task could not be queued", http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/admin/tasks", http.StatusSeeOther)
}

func (s *Server) AdminTaskRun(w http.ResponseWriter, r *http.Request) {
	s.enqueueTask(w, r, r.PathValue("task"))
}

func (s *Server) AdminTasks(w http.ResponseWriter, r *http.Request) {
	if s.Tasks == nil {
		http.Error(w, "task manager is unavailable", http.StatusServiceUnavailable)
		return
	}
	base, _, err := s.baseData(r)
	if err != nil {
		http.Error(w, "failed to load page", http.StatusInternalServerError)
		return
	}
	summaries, err := s.Tasks.Summaries()
	if err != nil {
		http.Error(w, "failed to load tasks", http.StatusInternalServerError)
		return
	}
	var jobs []tasks.Summary
	for _, summary := range summaries {
		if summary.Kind == tasks.KindService {
			continue
		}
		s.populateTaskNextRun(&summary)
		jobs = append(jobs, summary)
	}
	history, err := s.Tasks.History(10)
	if err != nil {
		http.Error(w, "failed to load tasks", http.StatusInternalServerError)
		return
	}
	data := map[string]any{"Title": "Tasks", "AdminTab": "tasks", "TaskPage": true, "Tasks": jobs, "History": history}
	mergeInto(data, base)
	render(w, "admin_tasks.html", data)
}

func (s *Server) populateTaskNextRun(summary *tasks.Summary) {
	switch summary.Key {
	case "chaptarr-refresh":
		if !s.Chaptarr.Enabled() {
			summary.NextRun = "Disabled"
			return
		}
		summary.NextRunAt = s.Chaptarr.NextRefreshAt()
		if summary.NextRunAt.IsZero() {
			summary.NextRun = "Pending initial refresh"
		}
		return
	case "email-digest":
	default:
		return
	}
	settings, err := s.Users.GetSMTPSettings()
	if err != nil || !settings.Enabled() {
		summary.NextRun = "Disabled"
		return
	}
	value, ok, err := s.DB.GetMeta(lastDigestMetaKey)
	if err != nil || !ok {
		return
	}
	lastSent, err := strconv.ParseInt(value, 10, 64)
	if err == nil {
		summary.NextRunAt = time.Unix(lastSent, 0).Add(digestInterval)
	}
}

func (s *Server) ServerIntegrations(w http.ResponseWriter, r *http.Request) {
	base, _, err := s.baseData(r)
	if err != nil {
		http.Error(w, "failed to load page", http.StatusInternalServerError)
		return
	}
	provider := r.URL.Query().Get("provider")
	if provider != "hardcover" {
		provider = "chaptarr"
	}
	data, err := s.serverIntegrationsData(provider)
	if err != nil {
		http.Error(w, "failed to load settings", http.StatusInternalServerError)
		return
	}
	mergeInto(data, base)
	render(w, "admin_integrations.html", data)
}

func (s *Server) renderAdminIntegrationsError(w http.ResponseWriter, r *http.Request, provider, errMsg, statusMsg string) {
	base, _, err := s.baseData(r)
	if err != nil {
		http.Error(w, "failed to load page", http.StatusInternalServerError)
		return
	}
	data, err := s.serverIntegrationsData(provider)
	if err != nil {
		http.Error(w, "failed to load settings", http.StatusInternalServerError)
		return
	}
	if errMsg != "" {
		data["Error"] = errMsg
	}
	if statusMsg != "" {
		data["Status"] = statusMsg
	}
	mergeInto(data, base)
	render(w, "admin_integrations.html", data)
}

func (s *Server) ServerIntegrationsHardcoverSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	current, err := s.Users.GetIntegrationSettings()
	if err != nil {
		http.Error(w, "failed to load settings", http.StatusInternalServerError)
		return
	}

	token := r.FormValue("hardcover_token")
	if token == "" {
		token = current.HardcoverToken
	}
	current.HardcoverEnabled = r.FormValue("hardcover_enabled") == "on"
	current.HardcoverToken = token
	current.HideNoHardcoverMatch = r.FormValue("hide_no_hardcover_match") == "on"
	current.HardcoverOverwriteCover = r.FormValue("hardcover_overwrite_cover") == "on"

	if err := s.Users.SaveIntegrationSettings(current); err != nil {
		s.renderAdminIntegrationsError(w, r, "hardcover", "failed to save Hardcover settings: "+err.Error(), "")
		return
	}
	s.Hardcover.SetConfig(current.HardcoverEnabled, current.HardcoverToken)

	http.Redirect(w, r, "/admin/integrations?provider=hardcover", http.StatusSeeOther)
}

func (s *Server) ServerIntegrationsChaptarrSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	current, err := s.Users.GetIntegrationSettings()
	if err != nil {
		http.Error(w, "failed to load settings", http.StatusInternalServerError)
		return
	}

	apiKey := r.FormValue("chaptarr_api_key")
	if apiKey == "" {
		apiKey = current.ChaptarrAPIKey
	}
	current.ChaptarrEnabled = r.FormValue("chaptarr_enabled") == "on"
	current.ChaptarrURL = r.FormValue("chaptarr_url")
	current.ChaptarrAPIKey = apiKey
	current.HideNoChaptarrMatch = r.FormValue("hide_no_chaptarr_match") == "on"

	if err := s.Users.SaveIntegrationSettings(current); err != nil {
		s.renderAdminIntegrationsError(w, r, "chaptarr", "failed to save Chaptarr settings: "+err.Error(), "")
		return
	}
	s.Chaptarr.SetConfig(current.ChaptarrEnabled, current.ChaptarrURL, current.ChaptarrAPIKey)

	http.Redirect(w, r, "/admin/integrations?provider=chaptarr", http.StatusSeeOther)
}

// Account shows password and permitted bookmark-link settings.
func (s *Server) Account(w http.ResponseWriter, r *http.Request) {
	s.renderAccount(w, r, "account.html", "account", "", "")
}

func (s *Server) AccountShelves(w http.ResponseWriter, r *http.Request) {
	s.renderAccountShelves(w, r, "", "")
}

func (s *Server) ShelfSettings(w http.ResponseWriter, r *http.Request) {
	s.renderShelfSettings(w, r, 0, "")
}

func (s *Server) AdminShelves(w http.ResponseWriter, r *http.Request) {
	shelves, err := s.DB.ListUserShelves()
	if err != nil {
		http.Error(w, "failed to load shelves", http.StatusInternalServerError)
		return
	}
	var privateShelves, sharedShelves, publicShelves []index.Shelf
	for _, shelf := range shelves {
		switch shelf.Visibility {
		case index.ShelfVisibilityShared:
			sharedShelves = append(sharedShelves, shelf)
		case index.ShelfVisibilityPublic:
			publicShelves = append(publicShelves, shelf)
		default:
			privateShelves = append(privateShelves, shelf)
		}
	}
	base, _, err := s.baseData(r)
	if err != nil {
		http.Error(w, "failed to load page", http.StatusInternalServerError)
		return
	}
	data := map[string]any{"Title": "Manage Shelves", "AdminTab": "shelves", "PrivateShelves": privateShelves, "SharedShelves": sharedShelves, "PublicShelves": publicShelves}
	mergeInto(data, base)
	render(w, "admin_shelves.html", data)
}

func (s *Server) renderShelfSettings(w http.ResponseWriter, r *http.Request, requestedID int64, errMsg string) {
	id := requestedID
	if id == 0 {
		var err error
		id, err = strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
	}
	username, _ := auth.UsernameFromContext(r.Context())
	shelf, err := s.DB.GetOwnedShelf(username, id)
	if err != nil {
		http.Error(w, "failed to load shelf", http.StatusInternalServerError)
		return
	}
	canManage := s.Users.Can(username, users.PermissionManageShelves)
	if shelf == nil && canManage {
		shelf, err = s.DB.GetShelf(id)
		if err != nil {
			http.Error(w, "failed to load shelf", http.StatusInternalServerError)
			return
		}
	}
	if shelf == nil || shelf.IsSystem || shelf.IsIntegration() {
		http.NotFound(w, r)
		return
	}
	var members []index.ShelfAccess
	if canManage && shelf.Username != username {
		members, err = s.DB.ListShelfMembersForManager(id)
	} else {
		members, err = s.DB.ListShelfMembers(username, id)
	}
	if err != nil {
		http.Error(w, "failed to load shelf members", http.StatusInternalServerError)
		return
	}
	base, _, err := s.baseData(r)
	if err != nil {
		http.Error(w, "failed to load shelf", http.StatusInternalServerError)
		return
	}
	data := map[string]any{"Title": "Shelf settings", "Shelf": shelf, "Members": members, "Error": errMsg, "ManageShelf": canManage && shelf.Username != username}
	mergeInto(data, base)
	render(w, "shelf_settings.html", data)
}

func (s *Server) ShelfSettingsVisibility(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	username, _ := auth.UsernameFromContext(r.Context())
	if r.FormValue("visibility") == index.ShelfVisibilityPublic && !s.Users.Can(username, users.PermissionCreatePublicShelves) && !s.Users.Can(username, users.PermissionManageShelves) {
		s.renderShelfSettings(w, r, id, "you are not allowed to make shelves public")
		return
	}
	var setErr error
	if s.Users.Can(username, users.PermissionManageShelves) {
		setErr = s.DB.SetShelfVisibilityForManager(id, r.FormValue("visibility"))
	} else {
		setErr = s.DB.SetShelfVisibility(username, id, r.FormValue("visibility"))
	}
	if setErr != nil {
		s.renderShelfSettings(w, r, id, setErr.Error())
		return
	}
	http.Redirect(w, r, "/shelves/"+strconv.FormatInt(id, 10)+"/settings", http.StatusSeeOther)
}

func (s *Server) ShelfSettingsRename(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	username, _ := auth.UsernameFromContext(r.Context())
	var renameErr error
	if s.Users.Can(username, users.PermissionManageShelves) {
		renameErr = s.DB.RenameShelfForManager(id, r.FormValue("name"))
	} else {
		renameErr = s.DB.RenameShelf(username, id, r.FormValue("name"))
	}
	if renameErr != nil {
		s.renderShelfSettings(w, r, id, renameErr.Error())
		return
	}
	http.Redirect(w, r, "/shelves/"+strconv.FormatInt(id, 10)+"/settings", http.StatusSeeOther)
}

func (s *Server) ShelfSettingsDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	username, _ := auth.UsernameFromContext(r.Context())
	var deleteErr error
	if s.Users.Can(username, users.PermissionManageShelves) {
		deleteErr = s.DB.DeleteShelfForManager(id)
	} else {
		deleteErr = s.DB.DeleteShelf(username, id)
	}
	if deleteErr != nil {
		s.renderShelfSettings(w, r, id, deleteErr.Error())
		return
	}
	http.Redirect(w, r, "/account/shelves", http.StatusSeeOther)
}

func (s *Server) ShelfSettingsMemberAdd(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	member := strings.TrimSpace(r.FormValue("username"))
	if user, err := s.Users.UserByUsername(member); err != nil || user == nil || !user.Enabled {
		s.renderShelfSettings(w, r, id, "member must be an enabled user")
		return
	}
	username, _ := auth.UsernameFromContext(r.Context())
	var addErr error
	if s.Users.Can(username, users.PermissionManageShelves) {
		addErr = s.DB.AddShelfMemberForManager(id, member)
	} else {
		addErr = s.DB.AddShelfMember(username, id, member)
	}
	if addErr != nil {
		s.renderShelfSettings(w, r, id, addErr.Error())
		return
	}
	http.Redirect(w, r, "/shelves/"+strconv.FormatInt(id, 10)+"/settings", http.StatusSeeOther)
}

func (s *Server) ShelfSettingsMemberDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	username, _ := auth.UsernameFromContext(r.Context())
	var removeErr error
	if s.Users.Can(username, users.PermissionManageShelves) {
		removeErr = s.DB.RemoveShelfMemberForManager(id, r.PathValue("username"))
	} else {
		removeErr = s.DB.RemoveShelfMember(username, id, r.PathValue("username"))
	}
	if removeErr != nil {
		s.renderShelfSettings(w, r, id, removeErr.Error())
		return
	}
	http.Redirect(w, r, "/shelves/"+strconv.FormatInt(id, 10)+"/settings", http.StatusSeeOther)
}

func (s *Server) renderAccountShelves(w http.ResponseWriter, r *http.Request, errMsg, status string) {
	base, _, err := s.baseData(r)
	if err != nil {
		http.Error(w, "failed to load shelves", http.StatusInternalServerError)
		return
	}
	username, _ := auth.UsernameFromContext(r.Context())
	shelves, err := s.DB.ListShelves(username)
	if err != nil {
		http.Error(w, "failed to load shelves", http.StatusInternalServerError)
		return
	}
	visibleShelves, err := s.DB.ListVisibleShelves(username)
	if err != nil {
		http.Error(w, "failed to load shelves", http.StatusInternalServerError)
		return
	}
	var memberShelves, publicShelves []index.ShelfAccess
	for _, shelf := range visibleShelves {
		switch shelf.Role {
		case "member":
			memberShelves = append(memberShelves, shelf)
		case "reader":
			publicShelves = append(publicShelves, shelf)
		}
	}
	settings, err := s.Users.GetGeneralSettings()
	if err != nil {
		http.Error(w, "failed to load shelf settings", http.StatusInternalServerError)
		return
	}
	data := map[string]any{"Title": "Account", "AccountTab": "shelves", "Shelves": shelves, "MemberShelves": memberShelves, "PublicShelves": publicShelves, "Error": errMsg, "Status": status, "ShelfLimit": settings.ShelfLimit, "UnlimitedShelves": s.Users.Can(username, users.PermissionManageShelves)}
	mergeInto(data, base)
	// baseData supplies editable shelves for book controls. Account management
	// must instead show only shelves owned by the current user.
	data["Shelves"] = shelves
	render(w, "account_shelves.html", data)
}

func (s *Server) AccountShelvesCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	username, _ := auth.UsernameFromContext(r.Context())
	settings, err := s.Users.GetGeneralSettings()
	if err != nil {
		http.Error(w, "failed to load shelf settings", http.StatusInternalServerError)
		return
	}
	limit := settings.ShelfLimit
	if s.Users.Can(username, users.PermissionManageShelves) {
		limit = 0
	}
	if _, err := s.DB.CreateShelf(username, r.FormValue("name"), limit); err != nil {
		s.renderAccountShelves(w, r, err.Error(), "")
		return
	}
	http.Redirect(w, r, "/account/shelves", http.StatusSeeOther)
}

func (s *Server) AccountShelvesRename(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	username, _ := auth.UsernameFromContext(r.Context())
	if err := s.DB.RenameShelf(username, id, r.FormValue("name")); err != nil {
		s.renderAccountShelves(w, r, err.Error(), "")
		return
	}
	http.Redirect(w, r, "/account/shelves", http.StatusSeeOther)
}

func (s *Server) AccountShelvesDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	username, _ := auth.UsernameFromContext(r.Context())
	var deleteErr error
	if s.Users.Can(username, users.PermissionManageShelves) {
		deleteErr = s.DB.DeleteShelfForManager(id)
	} else {
		deleteErr = s.DB.DeleteShelf(username, id)
	}
	if deleteErr != nil {
		s.renderAccountShelves(w, r, deleteErr.Error(), "")
		return
	}
	http.Redirect(w, r, "/account/shelves", http.StatusSeeOther)
}

// AccountEmail shows email and digest settings when email is configured.
func (s *Server) AccountEmail(w http.ResponseWriter, r *http.Request) {
	if !s.smtpAvailable() {
		http.Redirect(w, r, "/account", http.StatusSeeOther)
		return
	}
	s.renderAccount(w, r, "account_email.html", "email", "", "")
}

// AccountRedirect preserves incoming links to the account pages that were
// consolidated under /account.
func (s *Server) AccountRedirect(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/account", http.StatusSeeOther)
}

func (s *Server) renderAccount(w http.ResponseWriter, r *http.Request, page, tab, errMsg, status string) {
	base, _, err := s.baseData(r)
	if err != nil {
		http.Error(w, "failed to load page", http.StatusInternalServerError)
		return
	}
	username, _ := auth.UsernameFromContext(r.Context())
	user, err := s.Users.UserByUsername(username)
	if err != nil || user == nil {
		http.Error(w, "failed to load account", http.StatusInternalServerError)
		return
	}

	data := map[string]any{
		"Title":            "Account",
		"AccountTab":       tab,
		"Error":            errMsg,
		"Status":           status,
		"Email":            user.Email,
		"DigestSubscribed": s.Users.IsDigestSubscribed(username),
		"EmailAvailable":   s.smtpAvailable(),
		"EmailMessage":     "Email is unavailable. Configure SMTP in Server settings.",
		"CanBookmark":      s.Users.CanUseBookmark(username),
		"HasToken":         s.Users.HasBookmarkToken(username),
	}
	mergeInto(data, base)
	render(w, page, data)
}

func (s *Server) AccountConnections(w http.ResponseWriter, r *http.Request) {
	s.renderConnections(w, r, "", "")
}

func (s *Server) renderConnections(w http.ResponseWriter, r *http.Request, errMsg, status string) {
	base, _, err := s.baseData(r)
	if err != nil {
		http.Error(w, "failed to load page", http.StatusInternalServerError)
		return
	}
	username, _ := auth.UsernameFromContext(r.Context())
	providerConnections, err := s.Users.ListConnections(username)
	if err != nil {
		http.Error(w, "failed to load connections", http.StatusInternalServerError)
		return
	}
	shelves, err := s.Users.ConnectionShelves(username, "goodreads")
	if err != nil {
		http.Error(w, "failed to load Goodreads shelves", http.StatusInternalServerError)
		return
	}
	data := map[string]any{"Title": "Connections", "AccountTab": "connections", "Error": errMsg, "Status": status, "Connections": providerConnections, "GoodreadsShelves": shelves}
	mergeInto(data, base)
	render(w, "account_connections.html", data)
}

func (s *Server) AccountGoodreadsUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 10<<20)
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		s.renderConnections(w, r, "upload a Goodreads CSV smaller than 10 MB", "")
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		s.renderConnections(w, r, "choose a Goodreads CSV file", "")
		return
	}
	defer file.Close()
	snapshot, err := connections.ParseGoodreadsCSV(file)
	if err != nil {
		s.renderConnections(w, r, err.Error(), "")
		return
	}
	username, _ := auth.UsernameFromContext(r.Context())
	if err := s.Users.SaveConnection(username, "goodreads", "", true); err != nil {
		http.Error(w, "failed to save connection", http.StatusInternalServerError)
		return
	}
	if err := connections.Reconcile(username, "goodreads", snapshot, s.Users, s.DB); err != nil {
		http.Error(w, "failed to import Goodreads library", http.StatusInternalServerError)
		return
	}
	s.renderConnections(w, r, "", "Goodreads import saved. Select shelves to sync.")
}

func (s *Server) AccountGoodreadsShelves(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	username, _ := auth.UsernameFromContext(r.Context())
	shelves, err := s.Users.ConnectionShelves(username, "goodreads")
	if err != nil {
		http.Error(w, "failed to load Goodreads shelves", http.StatusInternalServerError)
		return
	}
	for _, shelf := range shelves {
		if err := s.Users.SetConnectionShelfSelection(username, "goodreads", shelf.RemoteKey, r.FormValue("shelf-"+shelf.RemoteKey) == "on"); err != nil {
			http.Error(w, "failed to save Goodreads shelves", http.StatusInternalServerError)
			return
		}
	}
	var snapshot connections.Snapshot
	for _, shelf := range shelves {
		snapshot.Shelves = append(snapshot.Shelves, connections.Shelf{Key: shelf.RemoteKey, Name: shelf.Name})
		items, err := s.Users.ConnectionItems(username, "goodreads", shelf.RemoteKey)
		if err != nil {
			http.Error(w, "failed to load Goodreads items", http.StatusInternalServerError)
			return
		}
		for _, item := range items {
			snapshot.Items = append(snapshot.Items, connections.Item{ShelfKey: item.RemoteShelfKey, ExternalID: item.ExternalID, Title: item.Title, Author: item.Author, ISBN: item.ISBN, AddedAt: item.AddedAt, Position: item.SourcePosition})
		}
	}
	if err := connections.Reconcile(username, "goodreads", snapshot, s.Users, s.DB); err != nil {
		http.Error(w, "failed to sync Goodreads shelves", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/account/connections", http.StatusSeeOther)
}

// AccountEmailSubmit updates the logged-in user's optional email address.
func (s *Server) AccountEmailSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.smtpAvailable() {
		http.Redirect(w, r, "/account", http.StatusSeeOther)
		return
	}
	username, _ := auth.UsernameFromContext(r.Context())
	user, err := s.Users.UserByUsername(username)
	if err != nil || user == nil {
		http.Error(w, "failed to load account", http.StatusInternalServerError)
		return
	}
	if err := s.Users.SetEmail(user.ID, r.FormValue("email")); err != nil {
		s.renderAccount(w, r, "account_email.html", "email", err.Error(), "")
		return
	}
	s.renderAccount(w, r, "account_email.html", "email", "", "Email updated.")
}

// AccountBookmarkRegenerate revokes any existing bookmark token, issues a
// new one, and redirects the browser straight to the tokened library URL
// (not back to an informational page) — copy/paste is unreliable on e-reader
// browsers, so the address bar itself needs to show the bookmarkable URL,
// ready for the browser's own "bookmark this page" action.
func (s *Server) AccountBookmarkRegenerate(w http.ResponseWriter, r *http.Request) {
	username, _ := auth.UsernameFromContext(r.Context())
	if !s.Users.CanUseBookmark(username) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	token, err := s.Users.GenerateBookmarkToken(username)
	if err != nil {
		http.Error(w, "failed to generate bookmark link", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, s.bookmarkURL(r, token), http.StatusSeeOther)
}

// AccountPasswordSubmit changes the logged-in user's password after
// verifying their current one.
func (s *Server) AccountPasswordSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	username, _ := auth.UsernameFromContext(r.Context())
	current := r.FormValue("current_password")
	newPassword := r.FormValue("new_password")

	if !s.Auth.CheckPassword(username, current) {
		s.renderAccount(w, r, "account.html", "account", "Current password is incorrect.", "")
		return
	}

	user, err := s.Users.UserByUsername(username)
	if err != nil {
		http.Error(w, "failed to load account", http.StatusInternalServerError)
		return
	}
	if user == nil {
		http.Error(w, "failed to load account", http.StatusInternalServerError)
		return
	}
	if err := s.Users.ResetPassword(user.ID, newPassword); err != nil {
		s.renderAccount(w, r, "account.html", "account", err.Error(), "")
		return
	}

	s.renderAccount(w, r, "account.html", "account", "", "Password updated.")
}

// AccountDigestToggle flips the logged-in user's opt-in to the weekly
// new-book digest email.
func (s *Server) AccountDigestToggle(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.smtpAvailable() {
		http.Redirect(w, r, "/account", http.StatusSeeOther)
		return
	}
	username, _ := auth.UsernameFromContext(r.Context())
	if err := s.Users.SetDigestSubscribed(username, r.FormValue("subscribed") == "on"); err != nil {
		http.Error(w, "failed to update preference", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/account/email", http.StatusSeeOther)
}

// ForgotPasswordPage shows the "request a reset link" form.
func (s *Server) ForgotPasswordPage(w http.ResponseWriter, r *http.Request) {
	if !s.passwordResetAvailable() {
		http.NotFound(w, r)
		return
	}
	base, _, err := s.baseData(r)
	if err != nil {
		http.Error(w, "failed to load page", http.StatusInternalServerError)
		return
	}
	data := map[string]any{"Title": "Forgot Password"}
	mergeInto(data, base)
	render(w, "forgot_password.html", data)
}

// ForgotPasswordSubmit emails a reset link if the address is on file, but
// always shows the same generic confirmation either way so the response
// can't be used to enumerate registered email addresses.
func (s *Server) ForgotPasswordSubmit(w http.ResponseWriter, r *http.Request) {
	if !s.passwordResetAvailable() {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	email := r.FormValue("email")
	emailKey := rateLimitIdentity(email)
	ip := requestIP(r)
	limited := !s.RateLimiter.allowed("reset-ip", ip, resetAttemptLimit, resetAttemptWindow) || !s.RateLimiter.allowed("reset-email", emailKey, resetAttemptLimit, resetAttemptWindow)
	if limited {
		s.renderForgotPasswordConfirmation(w, r)
		return
	}
	s.RateLimiter.record("reset-ip", ip, resetAttemptWindow)
	s.RateLimiter.record("reset-email", emailKey, resetAttemptWindow)
	token, _, found, err := s.Users.RequestPasswordReset(email)
	if err != nil {
		http.Error(w, "failed to process request", http.StatusInternalServerError)
		return
	}
	if found {
		settings, err := s.Users.GetSMTPSettings()
		if err != nil {
			log.Printf("forgot password: load smtp settings: %v", err)
		} else if !settings.Enabled() {
			log.Printf("forgot password: SMTP not configured, cannot email reset link to %s", email)
		} else if origin, err := s.emailOrigin(); err != nil {
			log.Printf("forgot password: %v", err)
		} else {
			link := origin + "/reset-password?token=" + token
			body := fmt.Sprintf("Someone requested a password reset for your %s account.\n\nReset your password:\n%s\n\nThis link expires in 1 hour. If you didn't request this, you can ignore this email.", s.SiteName, link)
			if err := mail.Send(settings, email, "Reset your "+s.SiteName+" password", body); err != nil {
				log.Printf("forgot password: send email: %v", err)
			}
		}
	}

	s.renderForgotPasswordConfirmation(w, r)
}

func (s *Server) renderForgotPasswordConfirmation(w http.ResponseWriter, r *http.Request) {
	base, _, err := s.baseData(r)
	if err != nil {
		http.Error(w, "failed to load page", http.StatusInternalServerError)
		return
	}
	data := map[string]any{
		"Title":  "Forgot Password",
		"Status": "If that address is on file, a reset link has been sent.",
	}
	mergeInto(data, base)
	render(w, "forgot_password.html", data)
}

// ResetPasswordPage shows the "set a new password" form for a token from a
// forgot-password email.
func (s *Server) ResetPasswordPage(w http.ResponseWriter, r *http.Request) {
	if !s.passwordResetAvailable() {
		http.NotFound(w, r)
		return
	}
	base, _, err := s.baseData(r)
	if err != nil {
		http.Error(w, "failed to load page", http.StatusInternalServerError)
		return
	}
	token := r.URL.Query().Get("token")
	data := map[string]any{"Title": "Reset Password", "Token": token}
	if _, ok := s.Users.VerifyResetToken(token); !ok {
		data["Error"] = "This reset link is invalid or has expired."
		data["Invalid"] = true
	}
	mergeInto(data, base)
	render(w, "reset_password.html", data)
}

// ResetPasswordSubmit completes a password reset from a forgot-password
// email link.
func (s *Server) ResetPasswordSubmit(w http.ResponseWriter, r *http.Request) {
	if !s.passwordResetAvailable() {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	token := r.FormValue("token")
	password := r.FormValue("password")

	if err := s.Users.CompletePasswordReset(token, password); err != nil {
		base, _, berr := s.baseData(r)
		if berr != nil {
			http.Error(w, "failed to load page", http.StatusInternalServerError)
			return
		}
		data := map[string]any{"Title": "Reset Password", "Token": token, "Error": err.Error()}
		mergeInto(data, base)
		render(w, "reset_password.html", data)
		return
	}

	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// InviteAcceptPage shows the "set your password" form for a new-account
// invite link.
func (s *Server) InviteAcceptPage(w http.ResponseWriter, r *http.Request) {
	base, _, err := s.baseData(r)
	if err != nil {
		http.Error(w, "failed to load page", http.StatusInternalServerError)
		return
	}
	token := r.URL.Query().Get("token")
	data := map[string]any{"Title": "Accept Invite", "Token": token}
	if _, ok := s.Users.VerifyInviteToken(token); !ok {
		data["Error"] = "This invite link is invalid or has expired."
		data["Invalid"] = true
	}
	mergeInto(data, base)
	render(w, "invite_accept.html", data)
}

// InviteAcceptSubmit sets the invited account's password, activating it.
func (s *Server) InviteAcceptSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	token := r.FormValue("token")
	password := r.FormValue("password")

	if err := s.Users.AcceptInvite(token, password); err != nil {
		base, _, berr := s.baseData(r)
		if berr != nil {
			http.Error(w, "failed to load page", http.StatusInternalServerError)
			return
		}
		data := map[string]any{"Title": "Accept Invite", "Token": token, "Error": err.Error()}
		mergeInto(data, base)
		render(w, "invite_accept.html", data)
		return
	}

	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) bookmarkURL(r *http.Request, token string) string {
	return s.siteOrigin(r) + "/?token=" + token
}
