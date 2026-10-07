// Command taper runs the Taper learning platform.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/bradyloveland/taper/internal/auth"
	"github.com/bradyloveland/taper/internal/config"
	"github.com/bradyloveland/taper/internal/server"
	"github.com/bradyloveland/taper/internal/store"
	"github.com/bradyloveland/taper/internal/update"
	"github.com/bradyloveland/taper/internal/version"
)

const usage = `Taper %s: a learning platform for TJEd commonwealth schools.

Usage:
  taper serve               Run the web server
  taper setup-code          Show the one-time code for first-time setup
  taper passwd USERNAME     Give someone a new temporary password (and turn their account on)
  taper backup FILE         Write a copy of the database to FILE
  taper mfa-reset USERNAME  Turn off someone's two-step sign-in (a lost phone and recovery codes)
  taper rollback            Go back to the version from before the last update (with the service stopped)
  taper network             Show the network settings in use
  taper network --reset     Forget network settings changed in the web interface (then restart the service)
  taper version             Print the version

Settings come from TAPER_* environment variables, which the installer keeps in
/etc/taper/taper.conf. See https://github.com/%s/blob/main/docs/guide/install.md
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, usage, version.Version, version.Repo)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = serve()
	case "setup-code":
		err = setupCode()
	case "passwd":
		if len(os.Args) != 3 {
			err = errors.New("usage: taper passwd USERNAME")
		} else {
			err = passwd(os.Args[2])
		}
	case "backup":
		if len(os.Args) != 3 {
			err = errors.New("usage: taper backup FILE")
		} else {
			err = backup(os.Args[2])
		}
	case "mfa-reset":
		if len(os.Args) != 3 {
			err = errors.New("usage: taper mfa-reset USERNAME")
		} else {
			err = mfaReset(os.Args[2])
		}
	case "network":
		err = network(os.Args[2:])
	case "rollback":
		err = rollback(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println(version.Version)
	case "help", "--help", "-h":
		fmt.Printf(usage, version.Version, version.Repo)
	default:
		fmt.Fprintf(os.Stderr, "Unknown command %q.\n\n"+usage, os.Args[1], version.Version, version.Repo)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "taper:", err)
		os.Exit(1)
	}
}

func openStore(cfg *config.Config) (*store.Store, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("can't create the data folder %s: %w", cfg.DataDir, err)
	}
	st, err := store.Open(filepath.Join(cfg.DataDir, "taper.db"))
	if err != nil {
		return nil, fmt.Errorf("can't open the database in %s: %w", cfg.DataDir, err)
	}
	return st, nil
}

func setupCode() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	st, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	if n, err := st.CountUsers(); err != nil {
		return err
	} else if n > 0 {
		fmt.Println("Setup is already done. To get into an account, use: sudo taper passwd USERNAME")
		return nil
	}
	code, err := server.EnsureSetupCode(cfg.DataDir)
	if err != nil {
		return err
	}
	fmt.Println(code)
	return nil
}

func passwd(username string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	st, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	u, err := st.GetUserByUsername(username)
	if errors.Is(err, store.ErrNotFound) {
		admins, _ := st.ListUsers(store.UserFilter{Role: store.RoleAdmin, Status: "all"})
		msg := fmt.Sprintf("there's no one with the username %q", username)
		if len(admins) > 0 {
			msg += ". Admins are:"
			for _, a := range admins {
				msg += " " + a.Username
			}
		}
		return errors.New(msg)
	}
	if err != nil {
		return err
	}
	temp := auth.TempPassword()
	hash, err := auth.HashPassword(temp)
	if err != nil {
		return err
	}
	if err := st.SetPassword(u.ID, hash, true); err != nil {
		return err
	}
	if !u.Active {
		u.Active = true
		if err := st.UpdateUser(u); err != nil {
			return err
		}
	}
	if err := st.DeleteUserSessions(u.ID, ""); err != nil {
		return err
	}
	fmt.Printf("Temporary password for %s (%s): %s\nThey'll choose their own password when they sign in.\n", u.DisplayName, u.Username, temp)
	return nil
}

func backup(dest string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("%s already exists; choose a new file name", dest)
	}
	st, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Backup(dest); err != nil {
		return err
	}
	fmt.Println("Database copied to", dest)
	return nil
}

// brokenOnPurpose makes a build that refuses to start, for testing the
// automatic rollback of updates (set with -X main.brokenOnPurpose=yes).
var brokenOnPurpose string

// settle is how long a newly installed version must stay up to count as working.
const settle = time.Minute

func serve() error {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	rollback, err := run(cfg)
	if err != nil {
		// Recorded so that, if this version was just installed and keeps
		// failing, the rollback can say why.
		update.RecordStartFailure(cfg.DataDir, version.Version, err)
		return err
	}
	// Going back by hand replaces the database, so it waits until it's closed.
	if rollback != "" {
		if err := (update.Files{AppDir: cfg.AppDir, DataDir: cfg.DataDir}).Rollback(rollback, true); err != nil {
			slog.Error("couldn't go back to the previous version", "err", err)
		}
	}
	return nil
}

// run serves until it's stopped or restarted for an update. It returns the
// reason to go back to the previous version, if that was asked for.
func run(cfg *config.Config) (string, error) {
	if brokenOnPurpose == "yes" {
		return "", errors.New("this test build doesn't start, on purpose")
	}
	st, err := openStore(cfg)
	if err != nil {
		return "", err
	}
	defer st.Close()
	if n, err := st.CountUsers(); err == nil && n == 0 {
		code, err := server.EnsureSetupCode(cfg.DataDir)
		if err != nil {
			return "", err
		}
		slog.Info("waiting for first-time setup in the browser", "setup_code", code)
	}
	srv, err := server.New(server.Options{Config: cfg, Store: st})
	if err != nil {
		return "", err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go pruneSessions(ctx, st)
	go srv.Run(ctx)
	slog.Info("Taper started", "version", version.Version, "data", cfg.DataDir)
	srv.Started(settle)
	defer srv.Stop()
	return srv.Serve(ctx)
}

func mfaReset(username string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	st, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	u, err := st.GetUserByUsername(username)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("there's no one with the username %q", username)
	}
	if err != nil {
		return err
	}
	if err := st.DisableTOTP(u.ID); err != nil {
		return err
	}
	if err := st.DeleteUserSessions(u.ID, ""); err != nil {
		return err
	}
	fmt.Printf("Two-step sign-in is off for %s (%s). They can sign in with just their password", u.DisplayName, u.Username)
	fmt.Println(", and turn it on again under My account (or they'll be asked to, if your school requires it).")
	return nil
}

// network shows the network settings, as "MODE PORT BIND DOMAIN" with "-"
// for empty values (the installer reads it), or resets those saved from the
// web interface.
func network(args []string) error {
	env, err := config.FromEnv()
	if err != nil {
		return err
	}
	switch {
	case len(args) == 1 && args[0] == "--reset":
		if err := config.ResetNetwork(env.DataDir); err != nil {
			return err
		}
		fmt.Println("Network settings changed in the web interface are removed. Restart Taper to use the installer's settings: sudo systemctl restart taper")
		return nil
	case len(args) != 0:
		return errors.New("usage: taper network [--reset]")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	dash := func(s string) string {
		if s == "" {
			return "-"
		}
		return s
	}
	fmt.Println(cfg.Mode, cfg.Port, dash(cfg.Bind), dash(cfg.Domain))
	return nil
}

// rollback puts back the version from before the last update. The service
// runs "taper.prev rollback --after-failure" each time it stops, to undo an
// update that keeps failing (see update.Files.AfterStop).
func rollback(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	files := update.Files{AppDir: filepath.Dir(exe), DataDir: cfg.DataDir}
	if len(args) == 1 && args[0] == "--after-failure" {
		rolled, err := files.AfterStop(os.Getenv("SERVICE_RESULT"))
		if rolled {
			fmt.Println("The new version kept failing, so the previous one was put back.")
		}
		return err
	}
	if len(args) != 0 {
		return errors.New("usage: taper rollback")
	}
	s, err := update.ReadState(cfg.DataDir)
	if err != nil {
		return err
	}
	if s == nil || s.Phase == update.RolledBack {
		return errors.New("there's no update to undo")
	}
	if exec.Command("systemctl", "is-active", "--quiet", "taper").Run() == nil {
		return errors.New("stop the service first (sudo systemctl stop taper), or go back from the Updates page")
	}
	if err := files.Rollback("Rolled back with taper rollback.", true); err != nil {
		return err
	}
	fmt.Printf("Version %s is back, with the database from before the update. Start it with: sudo systemctl start taper\n", s.From)
	return nil
}

func pruneSessions(ctx context.Context, st *store.Store) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if err := st.PruneSessions(); err != nil {
			slog.Error("removing expired sessions", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
