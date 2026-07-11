package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gorilla/mux"
	"github.com/gotd/td/tg"
)

func newHandlerTestServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	return &Server{
		opts: Options{
			CacheDir: filepath.Join(dir, "cache"),
			Dir:      filepath.Join(dir, "dl"),
		},
		items:       map[string]*Item{},
		finished:    map[int]struct{}{},
		downloading: map[string]struct{}{},
		cancels:     map[string]context.CancelFunc{},
		events:      make(chan struct{}, 8),
	}
}

func writeTestJPEG(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), defaultCachePerm); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 200)
	buf[0], buf[1], buf[2] = 0xff, 0xd8, 0xff
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
}

func serveWithID(h http.HandlerFunc, id string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/items/"+id, nil)
	req = mux.SetURLVars(req, map[string]string{"id": id})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestHandleThumbUsesDiskCacheWithoutDownloadQueue(t *testing.T) {
	s := newHandlerTestServer(t)
	id := "cached-thumb"
	writeTestJPEG(t, s.thumbCachePath(id))
	s.items[id] = &Item{
		ID:         id,
		Type:       mediaVideo,
		Status:     statusQueued,
		TargetPath: filepath.Join(s.opts.Dir, "missing.mp4"),
		MIME:       "video/mp4",
	}

	rr := serveWithID(s.handleThumb(context.Background()), id)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rr.Code, rr.Body.String())
	}
	if got := s.pendingDownloadCount(); got != 0 {
		t.Fatalf("pending downloads=%d, want 0", got)
	}
}

func TestHandlePreviewUsesLocalImageWithoutDownloadQueue(t *testing.T) {
	s := newHandlerTestServer(t)
	id := "local-image"
	target := filepath.Join(s.opts.Dir, "image.jpg")
	writeTestJPEG(t, target)
	s.items[id] = &Item{
		ID:         id,
		Type:       mediaImage,
		Status:     statusQueued,
		TargetPath: target,
		MIME:       "image/jpeg",
		Size:       200,
	}

	rr := serveWithID(s.handlePreview(context.Background()), id)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rr.Code, rr.Body.String())
	}
	if got := s.pendingDownloadCount(); got != 0 {
		t.Fatalf("pending downloads=%d, want 0", got)
	}
}

func TestHandlePreviewRemoteDoesNotEnqueueDownload(t *testing.T) {
	s := newHandlerTestServer(t)
	id := "remote-image"
	loc := &tg.InputDocumentFileLocation{ID: 1, AccessHash: 2, FileReference: []byte{1}, ThumbSize: "x"}
	s.items[id] = &Item{
		ID:         id,
		Type:       mediaImage,
		Status:     statusQueued,
		TargetPath: filepath.Join(s.opts.Dir, "missing.jpg"),
		MIME:       "image/jpeg",
		Size:       200,
		media:      &media{Location: loc, Name: "missing.jpg", Size: 200, MIME: "image/jpeg"},
		thumb:      &media{Location: loc, Name: "missing.jpg", Size: thumbCacheMaxBytes + 1, MIME: "image/jpeg"},
	}
	s.ensureTGServe()
	for i := 0; i < cap(s.tgShared); i++ {
		s.tgShared <- struct{}{}
	}

	rr := serveWithID(s.handlePreview(context.Background()), id)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want %d", rr.Code, http.StatusServiceUnavailable)
	}
	if got := s.pendingDownloadCount(); got != 0 {
		t.Fatalf("pending downloads=%d, want 0", got)
	}
}

func TestEnsureThumbCacheCoalescesConcurrentBuilds(t *testing.T) {
	s := newHandlerTestServer(t)
	path := s.thumbCachePath("coalesce")
	start := make(chan struct{})
	var builds atomic.Int32
	var wg sync.WaitGroup
	errs := make(chan error, 8)

	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- s.ensureThumbCache("same-key", path, func() error {
				builds.Add(1)
				<-start
				if err := os.MkdirAll(filepath.Dir(path), defaultCachePerm); err != nil {
					return err
				}
				buf := make([]byte, 200)
				buf[0], buf[1], buf[2] = 0xff, 0xd8, 0xff
				return os.WriteFile(path, buf, 0o644)
			})
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("ensureThumbCache error: %v", err)
		}
	}
	if got := builds.Load(); got != 1 {
		t.Fatalf("build count=%d, want 1", got)
	}
	if !validThumbCacheFile(path) {
		t.Fatal("expected valid thumb cache")
	}
}
