package web

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/viper"

	"github.com/iyear/tdl/pkg/consts"
)

func TestEnqueueCoverBuildDedupesPending(t *testing.T) {
	s := newHandlerTestServer(t)
	s.ensureCoverScheduler()
	s.coverMu.Lock()
	s.coverActive["hold"] = false
	s.coverMu.Unlock()
	s.items["v1"] = &Item{ID: "v1", Type: mediaVideo}

	first := s.enqueueCoverBuild("v1", false)
	second := s.enqueueCoverBuild("v1", false)
	if first != 1 || second != 1 {
		t.Fatalf("positions=%d,%d want 1,1", first, second)
	}
	s.coverMu.Lock()
	pending := len(s.coverPending)
	queued := len(s.coverPriQueue) + len(s.coverQueue)
	s.coverMu.Unlock()
	if pending != 1 || queued != 1 {
		t.Fatalf("pending=%d queued=%d want 1,1", pending, queued)
	}
}

func TestEnqueueCoverBuildPriorityJumpsQueue(t *testing.T) {
	s := newHandlerTestServer(t)
	s.ensureCoverScheduler()
	s.coverMu.Lock()
	s.coverActive["hold"] = false
	s.coverMu.Unlock()

	s.items["a"] = &Item{ID: "a", Type: mediaVideo}
	s.items["b"] = &Item{ID: "b", Type: mediaVideo}
	s.items["c"] = &Item{ID: "c", Type: mediaVideo}

	if pos := s.enqueueCoverBuild("a", false); pos != 1 {
		t.Fatalf("a pos=%d", pos)
	}
	if pos := s.enqueueCoverBuild("b", false); pos != 2 {
		t.Fatalf("b pos=%d", pos)
	}
	if pos := s.enqueueCoverBuild("c", true); pos != 1 {
		t.Fatalf("c pos=%d want 1", pos)
	}
	if pos := s.enqueueCoverBuild("a", false); pos != 2 {
		t.Fatalf("a after bump pos=%d want 2", pos)
	}
}

func TestShouldWaitCoverHybrid(t *testing.T) {
	if !shouldWaitCover(0, false) || !shouldWaitCover(1, false) {
		t.Fatal("front/active should wait")
	}
	if shouldWaitCover(5, false) {
		t.Fatal("deep queue without retry should not wait")
	}
	if !shouldWaitCover(3, true) {
		t.Fatal("retry within depth should wait")
	}
	if shouldWaitCover(4, true) {
		t.Fatal("retry beyond depth should not wait")
	}
}

func TestVisibleCoverPreemptsBackgroundDownload(t *testing.T) {
	prev := viper.GetInt(consts.FlagLimit)
	viper.Set(consts.FlagLimit, 1)
	defer viper.Set(consts.FlagLimit, prev)

	started := make(chan string, 4)
	released := make(chan struct{})
	preempted := make(chan string, 1)

	prevHook := testDownloadHook
	testDownloadHook = func(ctx context.Context, id string) error {
		started <- id
		select {
		case <-released:
			return nil
		case <-ctx.Done():
			preempted <- id
			return ctx.Err()
		}
	}
	defer func() { testDownloadHook = prevHook }()

	coverStarted := make(chan struct{})
	origCover := coverBuildHook
	coverBuildHook = func(srv *Server, _ context.Context, id string) error {
		close(coverStarted)
		writeTestJPEG(t, srv.thumbCachePath(id))
		return nil
	}
	defer func() { coverBuildHook = origCover }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := newHandlerTestServer(t)
	s.ctx = ctx
	s.items["bg"] = &Item{ID: "bg", Type: mediaVideo, Status: statusQueued, Size: 1}
	s.items["cover"] = &Item{ID: "cover", Type: mediaVideo, Status: statusQueued, Size: 1}
	s.order = []string{"bg", "cover"}

	s.enqueueDownload("bg")
	select {
	case id := <-started:
		if id != "bg" {
			t.Fatalf("first download=%s want bg", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for bg download")
	}

	s.enqueueCoverBuild("cover", true)

	select {
	case <-coverStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("cover build did not start")
	}
	select {
	case id := <-preempted:
		if id != "bg" {
			t.Fatalf("preempted=%s want bg", id)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("visible cover did not preempt background download")
	}

	waitCoverIdle(t, s)
	close(released)
}

func TestCoverBandwidthPreemptsAllBackgroundDownloads(t *testing.T) {
	prev := viper.GetInt(consts.FlagLimit)
	viper.Set(consts.FlagLimit, 2)
	defer viper.Set(consts.FlagLimit, prev)

	started := make(chan string, 8)
	released := make(chan struct{})
	var preempted sync.Map

	prevHook := testDownloadHook
	testDownloadHook = func(ctx context.Context, id string) error {
		started <- id
		select {
		case <-released:
			return nil
		case <-ctx.Done():
			preempted.Store(id, true)
			return ctx.Err()
		}
	}
	defer func() { testDownloadHook = prevHook }()

	coverGate := make(chan struct{})
	coverStarted := make(chan struct{})
	origCover := coverBuildHook
	coverBuildHook = func(srv *Server, ctx context.Context, id string) error {
		close(coverStarted)
		<-coverGate
		writeTestJPEG(t, srv.thumbCachePath(id))
		return nil
	}
	defer func() { coverBuildHook = origCover }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := newHandlerTestServer(t)
	s.ctx = ctx
	for _, id := range []string{"bg1", "bg2", "cover"} {
		s.items[id] = &Item{ID: id, Type: mediaVideo, Status: statusQueued, Size: 1}
		s.order = append(s.order, id)
	}

	s.enqueueDownload("bg1")
	s.enqueueDownload("bg2")
	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case id := <-started:
			seen[id] = true
		case <-time.After(2 * time.Second):
			t.Fatalf("timeout waiting for background downloads; seen=%v", seen)
		}
	}

	s.enqueueCoverBuild("cover", true)

	select {
	case <-coverStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("cover build did not start")
	}

	deadline := time.After(3 * time.Second)
	for {
		_, p1 := preempted.Load("bg1")
		_, p2 := preempted.Load("bg2")
		s.dlMu.Lock()
		bgActive := s.dlActive - s.dlActivePri
		hold := s.coverBandwidthHold
		s.dlMu.Unlock()
		if p1 && p2 && bgActive <= 0 && hold >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("expected both backgrounds preempted; p1=%v p2=%v bgActive=%d hold=%d", p1, p2, bgActive, hold)
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}

	close(coverGate)
	waitCoverIdle(t, s)
	close(released)
}

func TestCoverStateReplacesVisibleQueueAndCancelsStaleActive(t *testing.T) {
	s := newHandlerTestServer(t)
	s.ensureCoverScheduler()
	s.items["old"] = &Item{ID: "old", Type: mediaVideo}
	s.items["new"] = &Item{ID: "new", Type: mediaVideo}

	oldCtx, oldCancel := context.WithCancel(context.Background())
	s.coverMu.Lock()
	s.coverActive["old"] = true
	s.coverCancels["old"] = oldCancel
	s.coverMu.Unlock()

	s.setCoverState(false, []string{"new", "missing"})

	select {
	case <-oldCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("stale visible cover was not canceled")
	}

	s.coverMu.Lock()
	defer s.coverMu.Unlock()
	if s.coverPaused {
		t.Fatal("cover scheduler unexpectedly paused")
	}
	if _, ok := s.coverVisible["new"]; !ok || len(s.coverVisible) != 1 {
		t.Fatalf("visible=%v want only new", s.coverVisible)
	}
	if len(s.coverPriQueue) != 1 || s.coverPriQueue[0] != "new" {
		t.Fatalf("priority queue=%v want [new]", s.coverPriQueue)
	}
}

func TestCoverPauseCancelsWithoutFailureCooldownAndResumeRequeues(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := newHandlerTestServer(t)
	s.ctx = ctx
	s.items["video"] = &Item{ID: "video", Type: mediaVideo}

	started := make(chan struct{})
	origCover := coverBuildHook
	coverBuildHook = func(_ *Server, ctx context.Context, _ string) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	defer func() { coverBuildHook = origCover }()

	s.setCoverState(false, []string{"video"})
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("visible cover did not start")
	}

	s.setCoverState(true, []string{"video"})
	waitCoverIdle(t, s)

	s.coverMu.Lock()
	_, failed := s.coverFailed["video"]
	paused := s.coverPaused
	s.coverMu.Unlock()
	if failed {
		t.Fatal("canceled cover entered failure cooldown")
	}
	if !paused {
		t.Fatal("cover scheduler should be paused")
	}
	if pos := s.enqueueCoverBuild("video", true); pos != 0 {
		t.Fatalf("paused enqueue position=%d want 0", pos)
	}

	s.coverMu.Lock()
	s.coverActive["hold"] = false
	s.coverMu.Unlock()
	s.setCoverState(false, []string{"video"})
	s.coverMu.Lock()
	queued := len(s.coverPriQueue)
	s.coverMu.Unlock()
	if queued != 1 {
		t.Fatalf("resume queued=%d want 1", queued)
	}
}

func TestCoverSchedulerUsesIsolatedTGCover(t *testing.T) {
	s := newHandlerTestServer(t)
	s.ensureCoverScheduler()
	s.ensureTGServe()
	for i := 0; i < cap(s.tgShared); i++ {
		s.tgShared <- struct{}{}
	}

	s.items["cover-only"] = &Item{
		ID:   "cover-only",
		Type: mediaVideo,
		thumb: &media{
			Name: "thumb.jpg",
			Size: 128,
			MIME: "image/jpeg",
		},
	}

	var built atomic.Int32
	orig := coverBuildHook
	coverBuildHook = func(_ *Server, _ context.Context, id string) error {
		if id != "cover-only" {
			t.Fatalf("unexpected id %q", id)
		}
		built.Add(1)
		writeTestJPEG(t, s.thumbCachePath(id))
		return nil
	}
	defer func() { coverBuildHook = orig }()

	s.enqueueCoverBuild("cover-only", false)

	deadline := time.After(3 * time.Second)
	for built.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("cover worker did not run")
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}
	if !validThumbCacheFile(s.thumbCachePath("cover-only")) {
		t.Fatal("expected cover cache")
	}
	waitCoverIdle(t, s)
}

func TestWaitCoverBuildReturnsWhenCacheReady(t *testing.T) {
	s := newHandlerTestServer(t)
	id := "ready"
	writeTestJPEG(t, s.thumbCachePath(id))

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.waitCoverBuild(ctx, id); err != nil {
		t.Fatalf("waitCoverBuild: %v", err)
	}
}

func TestCoverFailureCooldownSkipsImmediateRetry(t *testing.T) {
	s := newHandlerTestServer(t)
	s.items["bad-cover"] = &Item{
		ID:    "bad-cover",
		Type:  mediaVideo,
		media: &media{Name: "bad-cover.mp4"},
	}

	var builds atomic.Int32
	orig := coverBuildHook
	coverBuildHook = func(_ *Server, _ context.Context, _ string) error {
		builds.Add(1)
		return errors.New("poster failed")
	}
	defer func() { coverBuildHook = orig }()

	s.enqueueCoverBuild("bad-cover", true)
	waitCoverIdle(t, s)
	if got := builds.Load(); got != 1 {
		t.Fatalf("builds=%d want 1", got)
	}

	if pos := s.enqueueCoverBuild("bad-cover", true); pos != 0 {
		t.Fatalf("retry position=%d want 0 during cooldown", pos)
	}
	time.Sleep(100 * time.Millisecond)
	if got := builds.Load(); got != 1 {
		t.Fatalf("builds=%d want cooldown to suppress retry", got)
	}
}

func TestEnsureThumbCacheCoalescesWithCoverQueue(t *testing.T) {
	s := newHandlerTestServer(t)
	s.ensureCoverScheduler()
	path := s.thumbCachePath("coalesce-cover")
	start := make(chan struct{})
	var builds atomic.Int32
	var wg sync.WaitGroup
	errs := make(chan error, 4)

	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- s.ensureThumbCache("thumb:coalesce-cover", path, func() error {
				builds.Add(1)
				<-start
				writeTestJPEG(t, path)
				return nil
			})
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("ensureThumbCache: %v", err)
		}
	}
	if got := builds.Load(); got != 1 {
		t.Fatalf("builds=%d want 1", got)
	}
}

func waitCoverIdle(t *testing.T, s *Server) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		s.coverMu.Lock()
		active := len(s.coverActive)
		queued := len(s.coverPriQueue) + len(s.coverQueue)
		pending := len(s.coverPending)
		s.coverMu.Unlock()
		if active == 0 && queued == 0 && pending == 0 {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("cover scheduler still busy; active=%d queued=%d pending=%d", active, queued, pending)
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}
}
