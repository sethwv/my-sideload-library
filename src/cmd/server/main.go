package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/sethwv/my-sideload-library/internal/auth"
	"github.com/sethwv/my-sideload-library/internal/chaptarr"
	"github.com/sethwv/my-sideload-library/internal/config"
	"github.com/sethwv/my-sideload-library/internal/hardcover"
	"github.com/sethwv/my-sideload-library/internal/index"
	"github.com/sethwv/my-sideload-library/internal/tasks"
	"github.com/sethwv/my-sideload-library/internal/thumbnail"
	"github.com/sethwv/my-sideload-library/internal/users"
	"github.com/sethwv/my-sideload-library/internal/web"
)

// buildVersion is set by release builds with -ldflags. The fallback keeps
// direct go build invocations usable when no build metadata is supplied.
var buildVersion = "dev"
var buildDate = "unknown"

// runHealthcheck is invoked as `server -healthcheck` from the Dockerfile's
// HEALTHCHECK instruction; distroless has no shell/curl for a CMD-SHELL probe.
func runHealthcheck() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	conn, err := net.DialTimeout("tcp", "localhost:"+port, 2*time.Second)
	if err != nil {
		os.Exit(1)
	}
	conn.Close()
	os.Exit(0)
}

func main() {
	if info, ok := debug.ReadBuildInfo(); ok {
		buildVersion, buildDate = developmentBuildMetadata(buildVersion, buildDate, currentBranch(), info.Settings)
	}

	if len(os.Args) > 1 && os.Args[1] == "-healthcheck" {
		runHealthcheck()
	}
	if len(os.Args) > 1 && os.Args[1] == "admin" {
		if err := runAdmin(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
		return
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		log.Fatalf("create data dir: %v", err)
	}

	db, err := index.Open(filepath.Join(cfg.DataDir, "index.db"))
	if err != nil {
		log.Fatalf("open index: %v", err)
	}
	defer db.Close()
	if err := db.BackfillLibraryRoot(cfg.LibraryPaths[0]); err != nil {
		log.Fatalf("backfill library root: %v", err)
	}

	userStore, err := users.Open(filepath.Join(cfg.DataDir, "users.db"))
	if err != nil {
		log.Fatalf("open users store: %v", err)
	}
	defer userStore.Close()
	sessionSecret, err := sessionSecret(userStore, cfg.SessionSecret)
	if err != nil {
		log.Fatalf("load session secret: %v", err)
	}

	// SiteName/PublicURL/CoverWidth/PageSize/SessionTTL are DB-backed (admin
	// Settings page) and take precedence over env vars at every run after
	// the first: if no general_settings row exists yet, the env-var config
	// seeds it once so existing deployments don't lose their configured
	// values on upgrade, then the DB is authoritative from then on (same
	// precedence pattern as integration_settings/HARDCOVER_API_TOKEN below).
	generalSettings, err := userStore.GetGeneralSettings()
	if err != nil {
		log.Fatalf("load general settings: %v", err)
	}
	if generalSettings == (users.GeneralSettings{}) {
		generalSettings = users.GeneralSettings{
			SiteName:   cfg.SiteName,
			PublicURL:  cfg.PublicURL,
			CoverWidth: cfg.CoverWidth,
			PageSize:   cfg.PageSize,
			ShelfLimit: 5,
			SessionTTL: cfg.SessionTTL,
		}
		if err := userStore.SaveGeneralSettings(generalSettings); err != nil {
			log.Fatalf("seed general settings from env vars: %v", err)
		}
	}

	covers, err := thumbnail.NewStore(filepath.Join(cfg.DataDir, "covers"), generalSettings.CoverWidth)
	if err != nil {
		log.Fatalf("open cover store: %v", err)
	}

	// Hardcover/Chaptarr settings are DB-backed (admin Integrations page)
	// and take precedence over env vars at every run after the first: if no
	// integration_settings row exists yet, HARDCOVER_API_TOKEN (the old
	// env-var-only config) seeds it once so existing deployments don't lose
	// their token on upgrade, then the DB is authoritative from then on.
	integrationSettings, err := userStore.GetIntegrationSettings()
	if err != nil {
		log.Fatalf("load integration settings: %v", err)
	}
	if integrationSettings == (users.IntegrationSettings{}) && cfg.HardcoverToken != "" {
		integrationSettings.HardcoverEnabled = true
		integrationSettings.HardcoverToken = cfg.HardcoverToken
		if err := userStore.SaveIntegrationSettings(integrationSettings); err != nil {
			log.Fatalf("seed integration settings from HARDCOVER_API_TOKEN: %v", err)
		}
	}

	authn := auth.New(sessionSecret, generalSettings.SessionTTL, userStore)
	hc := hardcover.New(integrationSettings.HardcoverEnabled, integrationSettings.HardcoverToken)
	ch := chaptarr.New(integrationSettings.ChaptarrEnabled, integrationSettings.ChaptarrURL, integrationSettings.ChaptarrAPIKey)
	if err := ch.SetCacheStore(db); err != nil {
		log.Fatalf("load chaptarr cache: %v", err)
	}
	srv := &web.Server{
		Auth:         authn,
		DB:           db,
		Covers:       covers,
		Users:        userStore,
		Hardcover:    hc,
		Chaptarr:     ch,
		LibraryPaths: cfg.LibraryPaths,
		DataDir:      cfg.DataDir,
		PageSize:     generalSettings.PageSize,
		SiteName:     generalSettings.SiteName,
		PublicURL:    generalSettings.PublicURL,
		StartedAt:    time.Now(),
		BuildVersion: buildVersion,
		BuildDate:    buildDate,
		RateLimiter:  web.NewRateLimiter(),
	}
	taskStore, err := tasks.Open(filepath.Join(cfg.DataDir, "tasks.db"))
	if err != nil {
		log.Fatalf("open task store: %v", err)
	}
	defer taskStore.Close()
	taskManager := tasks.New(taskStore)
	taskManager.Register(tasks.Task{Key: "rescan", Name: "Scan Library", Kind: tasks.KindJob, Runnable: true, NextRun: "Startup & On-Demand"}, func(context.Context) error {
		if err := db.Scan(cfg.LibraryPaths, covers); err != nil {
			return err
		}
		if n, err := db.Count(index.Filter{}); err == nil {
			log.Printf("indexed %d books", n)
		}
		return nil
	})
	taskManager.Register(tasks.Task{Key: "email-digest", Name: "Email digest", Kind: tasks.KindJob, Runnable: true, NextRun: "Weekly"}, func(context.Context) error {
		return srv.SendDigestNow()
	})
	taskManager.Register(tasks.Task{Key: "chaptarr-refresh", Name: "Refresh Chaptarr catalog", Kind: tasks.KindJob, Runnable: true, NextRun: "Every 12 hours"}, func(ctx context.Context) error {
		if !ch.Enabled() {
			return nil
		}
		_, err := ch.RefreshBooks(ctx)
		return err
	})
	taskManager.Register(tasks.Task{Key: "filesystem-watcher", Name: "Filesystem watcher", Kind: tasks.KindService}, nil)
	taskManager.Register(tasks.Task{Key: "enrichment-queue", Name: "Enrichment queue", Kind: tasks.KindService}, nil)
	taskManager.Register(tasks.Task{Key: "chaptarr-refresh-scheduler", Name: "Chaptarr refresh scheduler", Kind: tasks.KindService}, nil)
	taskManager.Register(tasks.Task{Key: "digest-scheduler", Name: "Digest scheduler", Kind: tasks.KindService}, nil)
	srv.Tasks = taskManager

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := taskManager.Start(ctx); err != nil {
		log.Fatalf("start task manager: %v", err)
	}
	if _, _, err := taskManager.Enqueue("rescan"); err != nil {
		log.Printf("queue initial library scan: %v", err)
	}
	log.Printf("queued library scan for %s", strings.Join(cfg.LibraryPaths, ", "))

	if err := taskManager.StartService(ctx, "filesystem-watcher", func(ctx context.Context) {
		watcher, err := index.NewWatcher(ctx, cfg.LibraryPaths, db, covers)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("start watcher: %v", err)
			}
			return
		}
		watcher.Run(ctx)
	}); err != nil {
		log.Fatalf("start filesystem watcher task: %v", err)
	}
	// Single background loop for both integrations (idles, rather than
	// exiting, while both are disabled) — see RunEnrichmentQueue's doc
	// comment for why Chaptarr and Hardcover are tried in one deterministic
	// per-book step in the same goroutine rather than two independently
	// polling ones, which enabling either from the admin Integrations page
	// later still works without a restart.
	if err := taskManager.StartService(ctx, "enrichment-queue", srv.RunEnrichmentQueue); err != nil {
		log.Fatalf("start enrichment task: %v", err)
	}
	if err := taskManager.StartService(ctx, "chaptarr-refresh-scheduler", srv.RunChaptarrRefreshScheduler); err != nil {
		log.Fatalf("start chaptarr refresh scheduler: %v", err)
	}
	if generalSettings.PublicURL == "" {
		log.Printf("warning: Public URL is not set; password-reset and invite emails are disabled")
	}
	if err := taskManager.StartService(ctx, "digest-scheduler", srv.RunDigestScheduler); err != nil {
		log.Fatalf("start digest task: %v", err)
	}

	mux := http.NewServeMux()
	protectedPost := func(handler http.Handler) http.Handler {
		return authn.RequireCSRF(handler)
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	mux.Handle("GET /static/", web.StaticHandler())
	mux.HandleFunc("GET /setup", srv.SetupPage)
	mux.HandleFunc("POST /setup", srv.SetupSubmit)
	mux.HandleFunc("GET /login", srv.LoginPage)
	mux.HandleFunc("POST /login", srv.LoginSubmit)
	mux.HandleFunc("GET /forgot-password", srv.ForgotPasswordPage)
	mux.HandleFunc("POST /forgot-password", srv.ForgotPasswordSubmit)
	mux.HandleFunc("GET /reset-password", srv.ResetPasswordPage)
	mux.HandleFunc("POST /reset-password", srv.ResetPasswordSubmit)
	mux.HandleFunc("GET /invite/accept", srv.InviteAcceptPage)
	mux.HandleFunc("POST /invite/accept", srv.InviteAcceptSubmit)
	mux.Handle("POST /logout", authn.RequireAuth(protectedPost(http.HandlerFunc(srv.Logout))))
	mux.Handle("GET /", authn.RequireAuth(http.HandlerFunc(srv.LibraryGrid)))
	mux.Handle("GET /authors", authn.RequireAuth(http.HandlerFunc(srv.AuthorsHandler)))
	mux.Handle("GET /series", authn.RequireAuth(http.HandlerFunc(srv.SeriesHandler)))
	mux.Handle("GET /favorites", authn.RequireAuth(http.HandlerFunc(srv.FavoritesHandler)))
	mux.Handle("GET /shelves/{id}", authn.RequireAuth(http.HandlerFunc(srv.ShelfHandler)))
	mux.Handle("GET /shelves/{id}/settings", authn.RequireFull(http.HandlerFunc(srv.ShelfSettings)))
	mux.Handle("POST /shelves/{id}/settings/visibility", authn.RequireFull(protectedPost(http.HandlerFunc(srv.ShelfSettingsVisibility))))
	mux.Handle("POST /shelves/{id}/settings/rename", authn.RequireFull(protectedPost(http.HandlerFunc(srv.ShelfSettingsRename))))
	mux.Handle("POST /shelves/{id}/settings/delete", authn.RequireFull(protectedPost(http.HandlerFunc(srv.ShelfSettingsDelete))))
	mux.Handle("POST /shelves/{id}/settings/members", authn.RequirePermission(users.PermissionOwnShelves, protectedPost(http.HandlerFunc(srv.ShelfSettingsMemberAdd))))
	mux.Handle("POST /shelves/{id}/settings/members/{username}/delete", authn.RequirePermission(users.PermissionOwnShelves, protectedPost(http.HandlerFunc(srv.ShelfSettingsMemberDelete))))
	mux.Handle("GET /covers/{id}", authn.RequireAuth(http.HandlerFunc(srv.Cover)))
	mux.Handle("GET /cover", authn.RequireAuth(http.HandlerFunc(srv.CachedCover)))
	mux.Handle("GET /books/{id}/download", authn.RequireAuth(http.HandlerFunc(srv.DownloadEPUB)))
	mux.Handle("GET /books/{id}/download.kepub", authn.RequireAuth(http.HandlerFunc(srv.DownloadKepub)))
	mux.Handle("POST /books/{id}/shelves/{shelfID}", authn.RequireAuth(protectedPost(http.HandlerFunc(srv.ShelfToggle))))
	mux.Handle("GET /admin/users", authn.RequireManageUsers(http.HandlerFunc(srv.AdminUsers)))
	mux.Handle("GET /admin/shelves", authn.RequirePermission(users.PermissionManageShelves, http.HandlerFunc(srv.AdminShelves)))
	mux.Handle("POST /admin/users", authn.RequireManageUsers(protectedPost(http.HandlerFunc(srv.AdminUsersCreate))))
	mux.Handle("POST /admin/users/{id}/role", authn.RequireManageUsers(protectedPost(http.HandlerFunc(srv.AdminUsersSetRole))))
	mux.Handle("POST /admin/users/{id}/permissions", authn.RequireManageUsers(protectedPost(http.HandlerFunc(srv.AdminUsersSetPermission))))
	mux.Handle("POST /admin/users/{id}/enabled", authn.RequireManageUsers(protectedPost(http.HandlerFunc(srv.AdminUsersSetEnabled))))
	mux.Handle("POST /admin/users/{id}/delete", authn.RequireManageUsers(protectedPost(http.HandlerFunc(srv.AdminUsersDelete))))
	mux.Handle("POST /admin/users/{id}/reset-password", authn.RequireManageUsers(protectedPost(http.HandlerFunc(srv.AdminUsersResetPassword))))
	mux.Handle("POST /admin/users/invite", authn.RequireManageUsers(protectedPost(http.HandlerFunc(srv.AdminUsersInvite))))
	mux.Handle("POST /admin/users/{id}/invite/resend", authn.RequireManageUsers(protectedPost(http.HandlerFunc(srv.AdminUsersResendInvite))))
	mux.Handle("POST /admin/users/{id}/email", authn.RequireManageUsers(protectedPost(http.HandlerFunc(srv.AdminUsersSetEmail))))
	mux.Handle("GET /admin/server", authn.RequireManageServer(http.HandlerFunc(srv.ServerInfo)))
	mux.Handle("GET /admin/tasks", authn.RequireManageServer(http.HandlerFunc(srv.AdminTasks)))
	mux.Handle("POST /admin/tasks/{task}/run", authn.RequireManageServer(protectedPost(http.HandlerFunc(srv.AdminTaskRun))))
	mux.Handle("POST /admin/server/rescan", authn.RequireManageServer(protectedPost(http.HandlerFunc(srv.ServerRescan))))
	mux.Handle("POST /admin/server/clear", authn.RequireManageServer(protectedPost(http.HandlerFunc(srv.ServerLibraryClear))))
	mux.Handle("GET /admin/settings", authn.RequireManageServer(http.HandlerFunc(srv.AdminSettings)))
	mux.Handle("POST /admin/settings/general", authn.RequireManageServer(protectedPost(http.HandlerFunc(srv.AdminSettingsGeneralSave))))
	mux.Handle("POST /admin/settings/kepub", authn.RequireManageServer(protectedPost(http.HandlerFunc(srv.AdminSettingsKepubSave))))
	mux.Handle("GET /admin/smtp", authn.RequireManageServer(http.HandlerFunc(srv.AdminSMTP)))
	mux.Handle("POST /admin/settings/smtp", authn.RequireManageServer(protectedPost(http.HandlerFunc(srv.ServerSMTPSave))))
	mux.Handle("POST /admin/settings/smtp/test", authn.RequireManageServer(protectedPost(http.HandlerFunc(srv.ServerSMTPTest))))
	mux.Handle("GET /admin/integrations", authn.RequireManageServer(http.HandlerFunc(srv.ServerIntegrations)))
	mux.Handle("POST /admin/integrations/enrichment-reset", authn.RequireManageServer(protectedPost(http.HandlerFunc(srv.ServerEnrichmentReset))))
	mux.Handle("POST /admin/integrations/enrichment-retry", authn.RequireManageServer(protectedPost(http.HandlerFunc(srv.ServerEnrichmentRetry))))
	mux.Handle("POST /admin/integrations/connection-enrichment-retry", authn.RequireManageServer(protectedPost(http.HandlerFunc(srv.ServerConnectionEnrichmentRetry))))
	mux.Handle("POST /admin/integrations/connection-cover-cache-retry", authn.RequireManageServer(protectedPost(http.HandlerFunc(srv.ServerConnectionCoverCacheRetry))))
	mux.Handle("POST /admin/integrations/connection-errors-retry", authn.RequireManageServer(protectedPost(http.HandlerFunc(srv.ServerConnectionErrorsRetry))))
	mux.Handle("POST /admin/integrations/connection-promotion-retry", authn.RequireManageServer(protectedPost(http.HandlerFunc(srv.ServerConnectionPromotionRetry))))
	mux.Handle("POST /admin/integrations/hardcover", authn.RequireManageServer(protectedPost(http.HandlerFunc(srv.ServerIntegrationsHardcoverSave))))
	mux.Handle("POST /admin/integrations/chaptarr", authn.RequireManageServer(protectedPost(http.HandlerFunc(srv.ServerIntegrationsChaptarrSave))))
	mux.Handle("GET /account", authn.RequireFull(http.HandlerFunc(srv.Account)))
	mux.Handle("GET /account/connections", authn.RequireFull(http.HandlerFunc(srv.AccountConnections)))
	mux.Handle("GET /account/connections/goodreads", authn.RequireFull(http.HandlerFunc(srv.AccountConnections)))
	mux.Handle("POST /account/connections/goodreads", authn.RequireFull(protectedPost(http.HandlerFunc(srv.AccountGoodreadsUpload))))
	mux.Handle("POST /account/connections/goodreads/shelves", authn.RequireFull(protectedPost(http.HandlerFunc(srv.AccountGoodreadsShelves))))
	mux.Handle("GET /account/shelves", authn.RequirePermission(users.PermissionOwnShelves, http.HandlerFunc(srv.AccountShelves)))
	mux.Handle("POST /account/shelves", authn.RequirePermission(users.PermissionOwnShelves, protectedPost(http.HandlerFunc(srv.AccountShelvesCreate))))
	mux.Handle("POST /account/shelves/{id}/rename", authn.RequirePermission(users.PermissionOwnShelves, protectedPost(http.HandlerFunc(srv.AccountShelvesRename))))
	mux.Handle("POST /account/shelves/{id}/delete", authn.RequireFull(protectedPost(http.HandlerFunc(srv.AccountShelvesDelete))))
	mux.Handle("GET /account/email", authn.RequireFull(http.HandlerFunc(srv.AccountEmail)))
	mux.Handle("GET /account/bookmark", authn.RequireFull(http.HandlerFunc(srv.AccountRedirect)))
	mux.Handle("POST /account/bookmark/regenerate", authn.RequireFull(protectedPost(http.HandlerFunc(srv.AccountBookmarkRegenerate))))
	mux.Handle("GET /account/password", authn.RequireFull(http.HandlerFunc(srv.AccountRedirect)))
	mux.Handle("POST /account/email", authn.RequireFull(protectedPost(http.HandlerFunc(srv.AccountEmailSubmit))))
	mux.Handle("POST /account/password", authn.RequireFull(protectedPost(http.HandlerFunc(srv.AccountPasswordSubmit))))
	mux.Handle("POST /account/digest", authn.RequireFull(protectedPost(http.HandlerFunc(srv.AccountDigestToggle))))
	mux.Handle("GET /books/{id}/edit-metadata", authn.RequireManageServer(http.HandlerFunc(srv.BookEditMetadata)))
	mux.Handle("POST /books/{id}/edit-metadata", authn.RequireManageServer(protectedPost(http.HandlerFunc(srv.BookEditMetadataSave))))

	httpSrv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           mux,
		ReadTimeout:       15 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		// No WriteTimeout: the kepub download handler streams a live
		// conversion of potentially large books; a write deadline could
		// truncate legitimate downloads.
	}

	serveErr := make(chan error, 1)
	go func() {
		log.Printf("sideload-library starting on :%s (library=%s data=%s)", cfg.Port, strings.Join(cfg.LibraryPaths, ", "), cfg.DataDir)
		serveErr <- httpSrv.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		if err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	case <-ctx.Done():
		log.Println("shutting down...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown error: %v", err)
		}
	}
}

func developmentBuildMetadata(version, date, branch string, settings []debug.BuildSetting) (string, string) {
	if version != "dev" || date != "unknown" {
		return version, date
	}

	values := make(map[string]string, len(settings))
	for _, setting := range settings {
		values[setting.Key] = setting.Value
	}
	revision, timestamp := values["vcs.revision"], values["vcs.time"]
	if branch == "" || revision == "" || timestamp == "" {
		return version, date
	}
	builtAt, err := time.Parse(time.RFC3339, timestamp)
	if err != nil {
		return version, date
	}
	if len(revision) > 7 {
		revision = revision[:7]
	}
	version = branch + "-" + revision
	if values["vcs.modified"] == "true" {
		version += " (dirty)"
	}
	return version, builtAt.Format("2006-01-02")
}

func currentBranch() string {
	output, err := exec.Command("git", "branch", "--show-current").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}
