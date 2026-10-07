package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/crypto/acme/autocert"

	"github.com/bradyloveland/taper/internal/config"
)

// Network settings changed in the web interface could lock everyone out, so a
// change that moves the listeners is tried first: the new listeners run
// alongside the old ones until an admin confirms the change from the new
// address. If nobody does in time, the new listeners close and nothing is
// saved. Only a confirmed change is written to network.json.

type netKey struct{}

// netInfo is attached to every request: the settings of the listeners it
// arrived on.
type netInfo struct {
	cfg     atomic.Pointer[config.Config]
	pending atomic.Bool // these listeners are on trial
}

// listenerSet is the HTTP servers for one set of network settings.
type listenerSet struct {
	info    *netInfo
	servers []*http.Server
}

func (ls *listenerSet) cfg() *config.Config { return ls.info.cfg.Load() }

func (ls *listenerSet) shutdown(timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for _, hs := range ls.servers {
		if err := hs.Shutdown(ctx); err != nil {
			hs.Close()
		}
	}
}

// pendingNet is a network change waiting to be confirmed.
type pendingNet struct {
	set        *listenerSet // nil until it's started (when swapping)
	cfg        *config.Config
	expires    time.Time
	timer      *time.Timer
	stoppedOld bool // the old listeners had to stop first (same port)
}

// netcfg returns the network settings for a request: those of the listeners
// it arrived on.
func (s *Server) netcfg(r *http.Request) *config.Config {
	if ni, ok := r.Context().Value(netKey{}).(*netInfo); ok {
		return ni.cfg.Load()
	}
	return s.currentNet()
}

// viaPending reports whether a request arrived on listeners that are on trial.
func viaPending(r *http.Request) bool {
	ni, ok := r.Context().Value(netKey{}).(*netInfo)
	return ok && ni.pending.Load()
}

// currentNet returns the network settings in use (not counting a trial).
func (s *Server) currentNet() *config.Config {
	s.netMu.Lock()
	defer s.netMu.Unlock()
	if s.netCur != nil {
		return s.netCur.cfg()
	}
	return s.cfg
}

// quietLog drops the noise of TLS handshakes from scanners.
var quietLog = log.New(discard{}, "", 0)

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// startSet opens the listeners for cfg. Ports are opened before it returns,
// so a port that's in use is an error here.
func (s *Server) startSet(cfg *config.Config, pending bool) (*listenerSet, error) {
	info := &netInfo{}
	info.cfg.Store(cfg)
	info.pending.Store(pending)
	ls := &listenerSet{info: info}
	base := func(net.Listener) context.Context { return context.WithValue(context.Background(), netKey{}, info) }
	mk := func(h http.Handler) *http.Server {
		hs := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute,
			MaxHeaderBytes: 64 << 10, BaseContext: base}
		if cfg.Mode == config.ModeHTTPS {
			hs.ErrorLog = quietLog
		}
		return hs
	}
	type pair struct {
		hs *http.Server
		l  net.Listener
	}
	var pairs []pair
	fail := func(err error) (*listenerSet, error) {
		for _, p := range pairs {
			p.l.Close()
		}
		return nil, err
	}
	listen := func(port int) (net.Listener, error) {
		addr := net.JoinHostPort(cfg.Bind, fmt.Sprint(port))
		l, err := net.Listen("tcp", addr)
		if err != nil {
			return nil, &listenError{addr: addr, err: err}
		}
		return l, nil
	}
	switch cfg.Mode {
	case config.ModeHTTPS:
		m := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(cfg.Domain),
			Cache:      autocert.DirCache(filepath.Join(s.cfg.DataDir, "certs")),
			Email:      cfg.Email,
		}
		l443, err := listen(443)
		if err != nil {
			return fail(err)
		}
		pairs = append(pairs, pair{mk(s), tls.NewListener(l443, &tls.Config{GetCertificate: m.GetCertificate,
			MinVersion: tls.VersionTLS12, NextProtos: []string{"h2", "http/1.1", "acme-tls/1"}})})
		l80, err := listen(80)
		if err != nil {
			return fail(err)
		}
		// Port 80 answers Let's Encrypt's checks and sends everyone else to HTTPS.
		pairs = append(pairs, pair{mk(m.HTTPHandler(nil)), l80})
	default:
		l, err := listen(cfg.Port)
		if err != nil {
			return fail(err)
		}
		pairs = append(pairs, pair{mk(s), l})
	}
	for _, p := range pairs {
		ls.servers = append(ls.servers, p.hs)
		go func(hs *http.Server, l net.Listener) {
			if err := hs.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
				s.listenerFailed(ls, err)
			}
		}(p.hs, p.l)
	}
	return ls, nil
}

// listenError says which address couldn't be opened.
type listenError struct {
	addr string
	err  error
}

func (e *listenError) Error() string {
	if errors.Is(e.err, syscall.EADDRINUSE) {
		return "another program is already using " + e.addr
	}
	if errors.Is(e.err, syscall.EACCES) {
		return "Taper isn't allowed to listen on " + e.addr
	}
	if errors.Is(e.err, syscall.EADDRNOTAVAIL) {
		return e.addr + " isn't an address of this server"
	}
	return "couldn't listen on " + e.addr + ": " + e.err.Error()
}

func (e *listenError) Unwrap() error { return e.err }

// listenerFailed handles a listener that stopped with an error.
func (s *Server) listenerFailed(ls *listenerSet, err error) {
	s.netMu.Lock()
	isPending := s.netPend != nil && s.netPend.set == ls
	isCurrent := s.netCur == ls
	s.netMu.Unlock()
	switch {
	case isPending:
		s.revertNetwork("The new settings stopped working: " + err.Error())
	case isCurrent:
		select {
		case s.netFatal <- err:
		default:
		}
	}
}

// Serve listens with the configured network settings until ctx ends or the
// server restarts for an update. It returns the reason to go back to the
// previous version, if that was asked for.
func (s *Server) Serve(ctx context.Context) (string, error) {
	set, err := s.startSet(s.cfg, false)
	if err != nil {
		return "", err
	}
	s.netMu.Lock()
	s.netCur = set
	s.netMu.Unlock()
	slog.Info("listening", "address", s.cfg.Describe())

	var fatal error
	select {
	case <-ctx.Done():
		slog.Info("shutting down")
	case <-s.Restarting():
		slog.Info("restarting for an update")
	case fatal = <-s.netFatal:
	}
	s.netMu.Lock()
	sets := []*listenerSet{s.netCur}
	if p := s.netPend; p != nil {
		if p.timer != nil {
			p.timer.Stop()
		}
		sets = append(sets, p.set)
		s.netPend = nil
	}
	s.netMu.Unlock()
	for _, ls := range sets {
		if ls != nil {
			ls.shutdown(10 * time.Second)
		}
	}
	if fatal != nil {
		return "", fatal
	}
	return s.RollbackRequested(), nil
}

// errTrying means a change is already waiting to be confirmed.
var errTrying = errors.New("a network change is already waiting to be confirmed. Keep it or cancel it first")

// tryNetwork starts listening with cfg alongside the current settings. If the
// new settings need a port the current ones use, the current listeners stop
// first, shortly after this returns (so the page that asked gets its answer);
// swapping reports that. Either way, the change goes back by itself unless
// it's confirmed within the timeout.
func (s *Server) tryNetwork(cfg *config.Config) (swapping bool, err error) {
	s.netMu.Lock()
	defer s.netMu.Unlock()
	if s.netPend != nil {
		return false, errTrying
	}
	if s.netCur == nil {
		return false, errors.New("the server isn't listening yet")
	}
	p := &pendingNet{cfg: cfg, expires: time.Now().Add(s.cfg.ConfirmTimeout)}
	set, err := s.startSet(cfg, true)
	if err != nil {
		if !errors.Is(err, syscall.EADDRINUSE) || !sharesPort(s.netCur.cfg(), cfg) {
			return false, err
		}
		p.stoppedOld, swapping = true, true
		go s.swapListeners(p)
	}
	p.set = set
	p.timer = time.AfterFunc(s.cfg.ConfirmTimeout, func() {
		s.revertNetwork("The new network settings weren't confirmed in time, so the earlier ones are back.")
	})
	s.netPend = p
	slog.Info("trying new network settings", "address", cfg.Describe(), "until", p.expires.Format(time.TimeOnly))
	return swapping, nil
}

// sharesPort reports whether a and b would listen on the same port.
func sharesPort(a, b *config.Config) bool {
	for _, x := range a.Ports() {
		for _, y := range b.Ports() {
			if x == y {
				return true
			}
		}
	}
	return false
}

// swapListeners stops the current listeners and starts the pending ones, for
// a change that reuses a port. If the new ones can't start, the old come back.
func (s *Server) swapListeners(p *pendingNet) {
	time.Sleep(500 * time.Millisecond)
	s.netMu.Lock()
	if s.netPend != p || s.netCur == nil {
		s.netMu.Unlock()
		return // cancelled meanwhile
	}
	old := s.netCur
	s.netCur = nil
	s.netMu.Unlock()
	old.shutdown(3 * time.Second)

	s.netMu.Lock()
	defer s.netMu.Unlock()
	if s.netPend != p {
		s.restartOld(old.cfg()) // cancelled while the old listeners were stopping
		return
	}
	set, err := s.startSet(p.cfg, true)
	if err != nil {
		slog.Warn("the new network settings didn't work", "err", err)
		p.timer.Stop()
		s.netPend = nil
		s.netNotice = "The new network settings didn't work (" + err.Error() + "), so the earlier ones are back."
		s.restartOld(old.cfg())
		return
	}
	p.set = set
}

// restartOld brings back listeners for cfg. Called with netMu held.
func (s *Server) restartOld(cfg *config.Config) {
	for i := 0; i < 10; i++ {
		set, err := s.startSet(cfg, false)
		if err == nil {
			s.netCur = set
			return
		}
		time.Sleep(300 * time.Millisecond) // the old port may take a moment to free
	}
	select {
	case s.netFatal <- errors.New("couldn't listen with the earlier network settings again"):
	default:
	}
}

// confirmNetwork keeps the pending settings: they're saved and the old
// listeners close.
func (s *Server) confirmNetwork() error {
	s.netMu.Lock()
	defer s.netMu.Unlock()
	p := s.netPend
	if p == nil || p.set == nil {
		return errors.New("there's no network change waiting to be confirmed")
	}
	if err := config.SaveNetwork(s.cfg.DataDir, p.cfg.NetworkValues()); err != nil {
		return err
	}
	p.timer.Stop()
	p.set.info.pending.Store(false)
	if old := s.netCur; old != nil {
		go old.shutdown(5 * time.Second)
	}
	s.netCur, s.netPend = p.set, nil
	s.netNotice = ""
	slog.Info("network settings confirmed", "address", p.cfg.Describe())
	return nil
}

// revertNetwork drops the pending settings and brings back the old ones if
// they had to stop.
func (s *Server) revertNetwork(notice string) {
	s.netMu.Lock()
	defer s.netMu.Unlock()
	p := s.netPend
	if p == nil {
		return
	}
	p.timer.Stop()
	s.netPend = nil
	s.netNotice = notice
	slog.Info("network change undone", "reason", notice)
	if p.set != nil {
		go p.set.shutdown(3 * time.Second)
	}
	if p.stoppedOld && s.netCur == nil {
		go func() {
			time.Sleep(500 * time.Millisecond)
			s.netMu.Lock()
			defer s.netMu.Unlock()
			if s.netCur == nil {
				s.restartOld(s.savedNet())
			}
		}()
	}
}

// savedNet is the confirmed network configuration, to go back to.
func (s *Server) savedNet() *config.Config {
	vals, err := config.ReadNetwork(s.cfg.DataDir)
	if err != nil || vals == nil {
		return s.cfg
	}
	c, err := config.WithNetwork(vals)
	if err != nil {
		return s.cfg
	}
	c.DataDir, c.AppDir, c.ConfirmTimeout = s.cfg.DataDir, s.cfg.AppDir, s.cfg.ConfirmTimeout
	return c
}

// applyNetwork saves a change that doesn't move the listeners (trusted
// proxies, the Let's Encrypt email) and uses it straight away.
func (s *Server) applyNetwork(cfg *config.Config) error {
	if err := config.SaveNetwork(s.cfg.DataDir, cfg.NetworkValues()); err != nil {
		return err
	}
	s.netMu.Lock()
	if s.netCur != nil {
		s.netCur.info.cfg.Store(cfg)
	} else {
		s.cfg = cfg
	}
	s.netMu.Unlock()
	return nil
}
