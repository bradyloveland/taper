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
	mfaThrottle    *auth.Throttle // wrong two-step codes per person
	resetThrottle  *auth.Throttle // password reset emails per account
	resetIPLimit   *auth.Throttle // password reset requests per address (a school shares one)
	chatThrottle   *auth.Throttle // chat messages per person

	chat        *chatHub
	streamCheck atomic.Int64 // how often chat streams recheck access (a time.Duration)

	box       *secret.Box
	updater   *update.Updater
	githubAPI string

	mu          sync.Mutex
	rollback    string // set when the restart should go back to the previous version
	restart     chan struct{}
	restartOnce sync.Once

	wg sync.WaitGroup // background email

	netMu     sync.Mutex
	netCur    *listenerSet // nil before Serve, and while swapping listeners
	netPend   *pendingNet
	netNotice string // why the last network change was undone
	netFatal  chan error

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
		mfaThrottle:    auth.NewThrottle(5, 15*time.Minute),
		resetThrottle:  auth.NewThrottle(3, time.Hour),
		resetIPLimit:   auth.NewThrottle(20, time.Hour),
		chatThrottle:   auth.NewThrottle(20, time.Minute),
		chat:           newChatHub(),
		githubAPI:      opts.GitHubAPI,
		restart:        make(chan struct{}),
		netFatal:       make(chan error, 1),
	}
	s.streamCheck.Store(int64(defaultStreamCheck))
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
	mux.HandleFunc("GET /apple-touch-icon-precomposed.png", s.assets.file("icons/apple-touch-icon.png"))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprintln(w, "ok "+version.Version)
	})
	mux.HandleFunc("GET /setup", s.handleSetupForm)
	mux.HandleFunc("POST /setup", s.handleSetup)
	mux.HandleFunc("GET /login", s.handleLoginForm)
	mux.HandleFunc("POST /login", s.handleLogin)
	mux.HandleFunc("GET /forgot", s.handleForgotForm)
	mux.HandleFunc("POST /forgot", s.handleForgot)
	mux.HandleFunc("GET /reset/{token}", s.handleResetForm)
	mux.HandleFunc("POST /reset/{token}", s.handleReset)
	mux.HandleFunc("GET /login/verify", s.handleVerifyForm)
	mux.HandleFunc("POST /login/verify", s.handleVerify)

	// Everyone signed in.
	mux.HandleFunc("POST /logout", s.signedIn(s.handleLogout))
	mux.HandleFunc("GET /password", s.signedIn(s.handlePasswordForm))
	mux.HandleFunc("POST /password", s.signedIn(s.handlePassword))
	mux.HandleFunc("GET /{$}", s.signedIn(s.handleHome))
	mux.HandleFunc("GET /account", s.signedIn(s.handleAccount))
	mux.HandleFunc("POST /account/profile", s.signedIn(s.handleAccountProfile))
	mux.HandleFunc("POST /account/password", s.signedIn(s.handleAccountPassword))
	mux.HandleFunc("POST /account/sessions/revoke", s.signedIn(s.handleRevokeSessions))
	mux.HandleFunc("GET /account/two-step", s.signedIn(s.handleTwoStep))
	mux.HandleFunc("POST /account/two-step/enable", s.signedIn(s.handleTwoStepEnable))
	mux.HandleFunc("POST /account/two-step/recovery", s.signedIn(s.handleTwoStepRecovery))
	mux.HandleFunc("POST /account/two-step/disable", s.signedIn(s.handleTwoStepDisable))
	mux.HandleFunc("GET /guide", s.signedIn(s.handleGuide))
	mux.HandleFunc("GET /guide/{page}", s.signedIn(s.handleGuide))
	mux.HandleFunc("GET /classes", s.signedIn(s.handleClasses))
	mux.HandleFunc("GET /classes/new", s.admin(s.handleClassNewForm))
	mux.HandleFunc("POST /classes/new", s.admin(s.handleClassCreate))
	mux.HandleFunc("GET /classes/{id}", s.signedIn(s.handleClass))
	mux.HandleFunc("GET /classes/{id}/edit", s.signedIn(s.handleClassEditForm))
	mux.HandleFunc("POST /classes/{id}/edit", s.signedIn(s.handleClassUpdate))
	mux.HandleFunc("POST /classes/{id}/{action}", s.signedIn(s.handleClassAction))
	mux.HandleFunc("GET /classes/{id}/members", s.signedIn(s.handleClassMembers))
	mux.HandleFunc("POST /classes/{id}/members/add", s.signedIn(s.handleClassMembersAdd))
	mux.HandleFunc("POST /classes/{id}/members/{uid}/remove", s.signedIn(s.handleClassMemberRemove))
	mux.HandleFunc("GET /classes/{id}/assignments", s.signedIn(s.handleClassAssignments))
	mux.HandleFunc("GET /classes/{id}/assignments/new", s.signedIn(s.handleAssignmentNewForm))
	mux.HandleFunc("POST /classes/{id}/assignments/new", s.signedIn(s.handleAssignmentCreate))
	mux.HandleFunc("GET /assignments/{id}", s.signedIn(s.handleAssignment))
	mux.HandleFunc("GET /assignments/{id}/edit", s.signedIn(s.handleAssignmentEditForm))
	mux.HandleFunc("POST /assignments/{id}/edit", s.signedIn(s.handleAssignmentUpdate))
	mux.HandleFunc("POST /assignments/{id}/delete", s.signedIn(s.handleAssignmentDelete))
	mux.HandleFunc("POST /assignments/{id}/files/{fid}/delete", s.signedIn(s.handleAssignmentFileDelete))
	mux.HandleFunc("POST /assignments/{id}/work", s.signedIn(s.handleWork))
	mux.HandleFunc("POST /assignments/{id}/work/autosave", s.signedIn(s.handleWorkAutosave))
	mux.HandleFunc("POST /assignments/{id}/work/take-back", s.signedIn(s.handleWorkTakeBack))
	mux.HandleFunc("POST /assignments/{id}/work/files/{fid}/delete", s.signedIn(s.handleWorkFileDelete))
	mux.HandleFunc("GET /assignments/{id}/work/{sid}", s.signedIn(s.handleReview))
	mux.HandleFunc("POST /assignments/{id}/work/{sid}", s.signedIn(s.handleReviewSave))
	mux.HandleFunc("GET /files/{id}/{name}", s.signedIn(s.handleFile))
	mux.HandleFunc("GET /chat", s.signedIn(s.handleChatList))
	mux.HandleFunc("GET /chat/unread", s.signedIn(s.handleChatUnread))
	mux.HandleFunc("GET /chat/{id}", s.signedIn(s.handleChannel))
	mux.HandleFunc("POST /chat/{id}", s.signedIn(s.handleChatPost))
	mux.HandleFunc("GET /chat/{id}/events", s.signedIn(s.handleChatEvents))
	mux.HandleFunc("POST /chat/{id}/read", s.signedIn(s.handleChatRead))
	mux.HandleFunc("POST /chat/{id}/messages/{mid}/delete", s.signedIn(s.handleChatDelete))
	mux.HandleFunc("POST /chat/{id}/mute", s.signedIn(s.handleChatMute))
	mux.HandleFunc("POST /chat/{id}/unmute", s.signedIn(s.handleChatUnmute))
	mux.HandleFunc("POST /chat/{id}/enabled", s.signedIn(s.handleChatEnabled))
	mux.HandleFunc("GET /calendar", s.signedIn(s.handleCalendar))
	mux.HandleFunc("GET /calendar/subscribe", s.signedIn(s.handleSubscribe))
	mux.HandleFunc("POST /calendar/subscribe/reset", s.signedIn(s.handleSubscribeReset))
	mux.HandleFunc("GET /events/new", s.signedIn(s.handleEventNewForm))
	mux.HandleFunc("POST /events/new", s.signedIn(s.handleEventCreate))
	mux.HandleFunc("GET /events/{id}", s.signedIn(s.handleEvent))
	mux.HandleFunc("GET /events/{id}/edit", s.signedIn(s.handleEventEditForm))
	mux.HandleFunc("POST /events/{id}/edit", s.signedIn(s.handleEventUpdate))
	mux.HandleFunc("POST /events/{id}/delete", s.signedIn(s.handleEventDelete))
	mux.HandleFunc("GET /ical/{file}", s.handleFeed)
	mux.HandleFunc("GET /report", s.signedIn(s.handleReportForm))
	mux.HandleFunc("POST /report", s.signedIn(s.handleReport))

	// Admins.
	mux.HandleFunc("GET /admin/people", s.admin(s.handlePeople))
	mux.HandleFunc("GET /admin/people/import", s.admin(s.handleImportForm))
	mux.HandleFunc("POST /admin/people/import", s.admin(s.handleImportPreview))
	mux.HandleFunc("POST /admin/people/import/create", s.admin(s.handleImportCreate))
	mux.HandleFunc("POST /admin/people/import/accounts.csv", s.admin(s.handleImportCredentials))
	mux.HandleFunc("GET /admin/people/new", s.admin(s.handlePersonNewForm))
	mux.HandleFunc("POST /admin/people/new", s.admin(s.handlePersonCreate))
	mux.HandleFunc("GET /admin/people/{id}", s.admin(s.handlePersonForm))
	mux.HandleFunc("POST /admin/people/{id}", s.admin(s.handlePersonUpdate))
	mux.HandleFunc("POST /admin/people/{id}/reset-password", s.admin(s.handlePersonResetPassword))
	mux.HandleFunc("POST /admin/people/{id}/two-step-off", s.admin(s.handlePersonTwoStepOff))
	mux.HandleFunc("GET /admin/settings", s.admin(s.handleSettings))
	mux.HandleFunc("POST /admin/settings", s.admin(s.handleSettingsSave))
	mux.HandleFunc("POST /admin/settings/reports", s.admin(s.handleReportSettingsSave))
	mux.HandleFunc("POST /admin/settings/security", s.admin(s.handleSecuritySave))
	mux.HandleFunc("GET /admin/backup", s.admin(s.handleBackupDownload))
	mux.HandleFunc("GET /admin/email", s.admin(s.handleEmail))
	mux.HandleFunc("POST /admin/email", s.admin(s.handleEmailSave))
	mux.HandleFunc("POST /admin/email/test", s.admin(s.handleEmailTest))
	mux.HandleFunc("GET /admin/network", s.admin(s.handleNetwork))
	mux.HandleFunc("POST /admin/network", s.admin(s.handleNetworkSave))
	mux.HandleFunc("POST /admin/network/confirm", s.admin(s.handleNetworkConfirm))
	mux.HandleFunc("POST /admin/network/cancel", s.admin(s.handleNetworkCancel))
	mux.HandleFunc("POST /admin/network/public", s.admin(s.handlePublicURLSave))
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
