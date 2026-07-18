package web

import (
	"context"
	"encoding/json"
	"errors"
	"image/color"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth/qrlogin"
	"github.com/gotd/td/tgerr"
	"github.com/skip2/go-qrcode"
)

type AccountSwitchError struct {
	Previous       bool
	DiscardCurrent bool
}

func (e *AccountSwitchError) Error() string {
	return "web account switch"
}

const (
	authChecking         = "checking"
	authUnauthorized     = "unauthorized"
	authWaitingScan      = "waiting_scan"
	authPasswordRequired = "password_required"
	authAuthorizing      = "authorizing"
	authAuthorized       = "authorized"
	authError            = "error"
)

type webAuth struct {
	ctx       context.Context
	client    *telegram.Client
	loggedIn  qrlogin.LoggedIn
	restart   chan struct{}
	restartDo sync.Once

	mu         sync.RWMutex
	status     string
	err        string
	qrPNG      []byte
	qrRevision int64
	expiresAt  int64
	running    bool
	canSwitch  bool
	previous   bool
	discard    bool
	ready      chan struct{}
	readyDo    sync.Once
}

type authSnapshot struct {
	Status     string `json:"status"`
	Authorized bool   `json:"authorized"`
	Error      string `json:"error,omitempty"`
	QRRevision int64  `json:"qr_revision,omitempty"`
	ExpiresAt  int64  `json:"expires_at,omitempty"`
	CanSwitch  bool   `json:"can_switch_back"`
}

func newWebAuth(ctx context.Context, client *telegram.Client, loggedIn qrlogin.LoggedIn, restart chan struct{}, canSwitch bool) *webAuth {
	return &webAuth{
		ctx:       ctx,
		client:    client,
		loggedIn:  loggedIn,
		restart:   restart,
		status:    authChecking,
		canSwitch: canSwitch,
		ready:     make(chan struct{}),
	}
}

func (a *webAuth) check(ctx context.Context) {
	status, err := a.client.Auth().Status(ctx)
	if err != nil {
		a.fail(err)
		return
	}
	if status.Authorized {
		a.authorize()
		return
	}
	a.mu.Lock()
	a.status = authUnauthorized
	a.err = ""
	a.mu.Unlock()
}

func (a *webAuth) snapshot() authSnapshot {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return authSnapshot{
		Status:     a.status,
		Authorized: a.status == authAuthorized,
		Error:      a.err,
		QRRevision: a.qrRevision,
		ExpiresAt:  a.expiresAt,
		CanSwitch:  a.canSwitch,
	}
}

func (a *webAuth) authorized() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.status == authAuthorized
}

func (a *webAuth) switchToPrevious() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.previous
}

func (a *webAuth) discardCurrentOnSwitch() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.discard
}

func (a *webAuth) wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-a.ready:
		return nil
	}
}

func (a *webAuth) start() {
	a.mu.Lock()
	if a.running || a.status == authAuthorized || a.status == authPasswordRequired {
		a.mu.Unlock()
		return
	}
	a.running = true
	a.status = authWaitingScan
	a.err = ""
	a.qrPNG = nil
	a.expiresAt = 0
	a.mu.Unlock()

	go a.runQR()
}

func (a *webAuth) runQR() {
	_, err := a.client.QR().Auth(a.ctx, a.loggedIn, func(_ context.Context, token qrlogin.Token) error {
		png, err := renderLoginQR(token.URL())
		if err != nil {
			return err
		}
		a.mu.Lock()
		a.status = authWaitingScan
		a.err = ""
		a.qrPNG = png
		a.qrRevision++
		a.expiresAt = token.Expires().Unix()
		a.mu.Unlock()
		return nil
	})
	if err != nil {
		if tgerr.Is(err, "SESSION_PASSWORD_NEEDED") {
			a.mu.Lock()
			a.running = false
			a.status = authPasswordRequired
			a.err = ""
			a.qrPNG = nil
			a.expiresAt = 0
			a.mu.Unlock()
			return
		}
		if a.ctx.Err() == nil {
			a.fail(err)
		}
		return
	}

	if _, err := a.client.Self(a.ctx); err != nil {
		a.fail(err)
		return
	}
	a.authorize()
}

func renderLoginQR(url string) ([]byte, error) {
	qr, err := qrcode.New(url, qrcode.Highest)
	if err != nil {
		return nil, err
	}
	qr.ForegroundColor = color.Black
	qr.BackgroundColor = color.White
	return qr.PNG(360)
}

func (a *webAuth) password(ctx context.Context, password string) error {
	password = strings.TrimSpace(password)
	if password == "" {
		return errors.New("password is required")
	}

	a.mu.Lock()
	if a.status != authPasswordRequired {
		a.mu.Unlock()
		return errors.New("2FA password is not required")
	}
	a.status = authAuthorizing
	a.err = ""
	a.mu.Unlock()

	if _, err := a.client.Auth().Password(ctx, password); err != nil {
		a.mu.Lock()
		a.status = authPasswordRequired
		a.err = err.Error()
		a.mu.Unlock()
		return err
	}
	if _, err := a.client.Self(ctx); err != nil {
		a.fail(err)
		return err
	}
	a.authorize()
	return nil
}

func (a *webAuth) authorize() {
	a.mu.Lock()
	a.status = authAuthorized
	a.err = ""
	a.running = false
	a.qrPNG = nil
	a.expiresAt = 0
	a.mu.Unlock()
	a.readyDo.Do(func() { close(a.ready) })
}

func (a *webAuth) fail(err error) {
	a.mu.Lock()
	a.running = false
	a.status = authError
	a.err = err.Error()
	a.qrPNG = nil
	a.expiresAt = 0
	a.mu.Unlock()
}

func (a *webAuth) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions ||
			strings.HasPrefix(r.URL.Path, "/api/auth/") ||
			r.URL.Path == "/api/health" {
			next.ServeHTTP(w, r)
			return
		}
		if !a.authorized() {
			http.Error(w, "Telegram authorization required", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *webAuth) handleStatus(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, a.snapshot())
}

func (a *webAuth) handleStart(w http.ResponseWriter, _ *http.Request) {
	a.start()
	writeJSON(w, a.snapshot())
}

func (a *webAuth) handleQR(w http.ResponseWriter, r *http.Request) {
	a.mu.RLock()
	png := append([]byte(nil), a.qrPNG...)
	a.mu.RUnlock()
	if len(png) == 0 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}

func (a *webAuth) handlePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if err := a.password(r.Context(), req.Password); err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	writeJSON(w, a.snapshot())
}

func (a *webAuth) handleSwitch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Previous bool `json:"previous"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if req.Previous && !a.canSwitch {
		http.Error(w, "no previous account", http.StatusConflict)
		return
	}
	authorized := a.authorized()
	if !req.Previous && !authorized {
		http.Error(w, "Telegram authorization required", http.StatusUnauthorized)
		return
	}
	a.mu.Lock()
	a.previous = req.Previous
	a.discard = req.Previous && !authorized
	a.mu.Unlock()
	writeJSON(w, map[string]any{"ok": true, "restarting": true})
	go a.restartDo.Do(func() { close(a.restart) })
}
