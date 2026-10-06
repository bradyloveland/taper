// Package server is Taper's web interface.
package server

import (
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bradyloveland/taper/docs"
	"github.com/bradyloveland/taper/internal/auth"
	"github.com/bradyloveland/taper/internal/config"
	"github.com/bradyloveland/taper/internal/release"
	"github.com/bradyloveland/taper/internal/secret"
	"github.com/bradyloveland/taper/internal/store"
	"github.com/bradyloveland/taper/internal/update"
	"github.com/bradyloveland/taper/internal/version"
	"github.com/bradyloveland/taper/web"
)

// Options configure a Server.
type Options struct {
	Config *config.Config
	Store  *store.Store

	// For tests: the release signing keys, GitHub API addresses, whether
	// systemd supervises the server, and the program's path.
	UpdateKeys []release.Key
	UpdateAPI  string // a repository's API, as update.DefaultAPI
	GitHubAPI  string // as github.DefaultAPI
	Supervised func() bool
	Executable string
}

// Server handles HTTP requests.
type Server struct {
	cfg     *config.Config
	store   *store.Store
	pages   map[string]*template.Template
	assets  *assets
	guide   *guide
	handler http.Handler

	userThrottle   *auth.Throttle // failed sign-ins per username
	ipThrottle     *auth.Throttle // failed sign-ins and setup codes per address
	reportThrottle *auth.Throttle // problem reports per person

	box       *secret.Box
	updater   *update.Updater
	githubAPI string

	mu          sync.Mutex
	rollback    string // set when the restart should go back to the previous version
	restart     chan struct{}
	restartOnce sync.Once

	setupMu   sync.Mutex
	setupDone atomic.Bool
}

// New builds a server.
func New(opts Options) (*Server, error) {
	if opts.Config == nil || opts.Store == nil {
		return nil, errors.New("server: Config and Store are required")
	}
	s := &Server{
		cfg:   opts.Config,
		store: opts.Store,
		// A whole school can share one public address, so the per-address
		// limit is much higher than the per-account one.
		userThrottle:   auth.NewThrottle(5, 15*time.Minute),
		ipThrottle:     auth.NewThrottle(50, 15*time.Minute),
		reportThrottle: auth.NewThrottle(5, time.Hour),
		githubAPI:      opts.GitHubAPI,
		restart:        make(chan struct{}),
	}
	var err error
	if s.box, err = secret.LoadOrCreate(filepath.Join(s.cfg.DataDir, "secret.key")); err != nil {
		return nil, err
	}
	supervised := opts.Supervised
	if supervised == nil {
		// systemd sets INVOCATION_ID for the services it runs.
		supervised = func() bool { return os.Getenv("INVOCATION_ID") != "" }
	}
	s.updater = &update.Updater{
		Files:      update.Files{AppDir: s.cfg.AppDir, DataDir: s.cfg.DataDir},
		Store:      s.store,
		Version:    version.Version,
		Keys:       opts.UpdateKeys,
		API:        opts.UpdateAPI,
		Supervised: supervised,
		Executable: opts.Executable,
	}
	if s.assets, err = loadAssets(web.Static); err != nil {
		return nil, fmt.Errorf("loading static files: %w", err)
	}
	if s.pages, err = s.parseTemplates(web.Templates); err != nil {
		return nil, fmt.Errorf("parsing templates: %w", err)
	}
	if s.guide, err = loadGuide(docs.Guide); err != nil {
		return nil, fmt.Errorf("loading the guide: %w", err)
	}
	n, err := s.store.CountUsers()
	if err != nil {
		return nil, err
	}
	s.setupDone.Store(n > 0)
	s.handler = s.routes()
	return s, nil
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	// Public: no sign-in needed.
	mux.Handle("GET /static/", s.assets.handler())
	mux.HandleFunc("GET /manifest.webmanifest", s.handleManifest)
	mux.HandleFunc("GET /sw.js", s.handleServiceWorker)
	mux.HandleFunc("GET /offline", s.handleOffline)
	mux.HandleFunc("GET /favicon.ico", s.assets.file("icons/favicon-32.png"))
	mux.HandleFunc("GET /apple-touch-icon.png", s.assets.file("icons/apple-touch-icon.png"))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprintln(w, "ok "+version.Version)
	})
	mux.HandleFunc("GET /setup", s.handleSetupForm)
	mux.HandleFunc("POST /setup", s.handleSetup)
	mux.HandleFunc("GET /login", s.handleLoginForm)
	mux.HandleFunc("POST /login", s.handleLogin)

	// Everyone signed in.
	mux.HandleFunc("POST /logout", s.signedIn(s.handleLogout))
	mux.HandleFunc("GET /password", s.signedIn(s.handlePasswordForm))
	mux.HandleFunc("POST /password", s.signedIn(s.handlePassword))
	mux.HandleFunc("GET /{$}", s.signedIn(s.handleHome))
	mux.HandleFunc("GET /account", s.signedIn(s.handleAccount))
	mux.HandleFunc("POST /account/profile", s.signedIn(s.handleAccountProfile))
	mux.HandleFunc("POST /account/password", s.signedIn(s.handleAccountPassword))
	mux.HandleFunc("POST /account/sessions/revoke", s.signedIn(s.handleRevokeSessions))
	mux.HandleFunc("GET /guide", s.signedIn(s.handleGuide))
	mux.HandleFunc("GET /guide/{page}", s.signedIn(s.handleGuide))
	mux.HandleFunc("GET /report", s.signedIn(s.handleReportForm))
	mux.HandleFunc("POST /report", s.signedIn(s.handleReport))

	// Admins.
	mux.HandleFunc("GET /admin/people", s.admin(s.handlePeople))
	mux.HandleFunc("GET /admin/people/new", s.admin(s.handlePersonNewForm))
	mux.HandleFunc("POST /admin/people/new", s.admin(s.handlePersonCreate))
	mux.HandleFunc("GET /admin/people/{id}", s.admin(s.handlePersonForm))
	mux.HandleFunc("POST /admin/people/{id}", s.admin(s.handlePersonUpdate))
	mux.HandleFunc("POST /admin/people/{id}/reset-password", s.admin(s.handlePersonResetPassword))
	mux.HandleFunc("GET /admin/settings", s.admin(s.handleSettings))
	mux.HandleFunc("POST /admin/settings", s.admin(s.handleSettingsSave))
	mux.HandleFunc("POST /admin/settings/reports", s.admin(s.handleReportSettingsSave))
	mux.HandleFunc("GET /admin/backup", s.admin(s.handleBackupDownload))
	mux.HandleFunc("GET /admin/reports", s.admin(s.handleReports))
	mux.HandleFunc("POST /admin/reports/{id}/send", s.admin(s.handleReportSend))
	mux.HandleFunc("GET /admin/updates", s.admin(s.handleUpdates))
	mux.HandleFunc("POST /admin/updates/check", s.admin(s.handleUpdateCheck))
	mux.HandleFunc("POST /admin/updates/settings", s.admin(s.handleUpdateSettings))
	mux.HandleFunc("POST /admin/updates/download", s.admin(s.handleUpdateDownload))
	mux.HandleFunc("POST /admin/updates/upload", s.admin(s.handleUpdateUpload))
	mux.HandleFunc("POST /admin/updates/install", s.admin(s.handleUpdateInstall))
	mux.HandleFunc("POST /admin/updates/discard", s.admin(s.handleUpdateDiscard))
	mux.HandleFunc("POST /admin/updates/rollback", s.admin(s.handleUpdateRollback))
	mux.HandleFunc("POST /admin/updates/dismiss", s.admin(s.handleUpdateDismiss))

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.notFound(w, r)
	})

	var h http.Handler = mux
	h = s.gate(h)
	h = s.loadSession(h)
	h = http.NewCrossOriginProtection().Handler(h)
	h = s.securityHeaders(h)
	h = s.recoverer(h)
	return h
}

// -------------------------------------------------------------- setup code

// SetupCodePath is where the one-time setup code is kept until setup is done.
func SetupCodePath(dataDir string) string { return filepath.Join(dataDir, "setup-code") }

// EnsureSetupCode returns the setup code, creating it if needed.
func EnsureSetupCode(dataDir string) (string, error) {
	path := SetupCodePath(dataDir)
	if b, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(b)) != "" {
		return strings.TrimSpace(string(b)), nil
	}
	code := auth.Code(2)
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(code+"\n"), 0o600); err != nil {
		return "", err
	}
	return code, nil
}

func (s *Server) setupCode() (string, error) { return EnsureSetupCode(s.cfg.DataDir) }

// --------------------------------------------------------------- utilities

func (s *Server) logError(r *http.Request, msg string, err error) {
	slog.Error(msg, "err", err, "method", r.Method, "path", r.URL.Path)
}

// mustFS panics if a sub-filesystem can't be made (only with a bad embed).
func mustFS(f fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(f, dir)
	if err != nil {
		panic(err)
	}
	return sub
}
