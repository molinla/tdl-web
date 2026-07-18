package web

import (
	"bytes"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWebAuthMiddlewareAndQR(t *testing.T) {
	a := &webAuth{status: authUnauthorized}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	handler := a.middleware(next)

	for _, path := range []string{"/api/auth/status", "/api/health"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("%s: got %d", path, rec.Code)
		}
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/items", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized items: got %d", rec.Code)
	}

	a.status = authAuthorized
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/items", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("authorized items: got %d", rec.Code)
	}

	b, err := renderLoginQR("tg://login?token=test")
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("decode QR PNG: %v", err)
	}
	var black, white bool
	for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
		for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
			r, g, bl, a := img.At(x, y).RGBA()
			white = white || a == 0xffff && r == 0xffff && g == 0xffff && bl == 0xffff
			black = black || a == 0xffff && r == 0 && g == 0 && bl == 0
		}
	}
	if !black || !white {
		t.Fatalf("QR colors: black=%v white=%v", black, white)
	}
}

func TestWebAuthSwitchRestartsWithoutLogout(t *testing.T) {
	restart := make(chan struct{})
	a := &webAuth{status: authAuthorized, restart: restart, canSwitch: true}
	rec := httptest.NewRecorder()

	a.handleSwitch(rec, httptest.NewRequest(http.MethodPost, "/api/auth/switch", strings.NewReader(`{"previous":true}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("switch: got %d", rec.Code)
	}
	if !a.switchToPrevious() {
		t.Fatal("switch did not select previous session")
	}
	select {
	case <-restart:
	case <-time.After(time.Second):
		t.Fatal("switch did not restart")
	}
}

func TestWebAuthCanCancelPendingAccountSwitch(t *testing.T) {
	restart := make(chan struct{})
	a := &webAuth{status: authWaitingScan, restart: restart, canSwitch: true}
	rec := httptest.NewRecorder()

	a.handleSwitch(rec, httptest.NewRequest(http.MethodPost, "/api/auth/switch", strings.NewReader(`{"previous":true}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("switch: got %d", rec.Code)
	}
	if !a.switchToPrevious() || !a.discardCurrentOnSwitch() {
		t.Fatal("pending session was not marked for cancellation")
	}
}
