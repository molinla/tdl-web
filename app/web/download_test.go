package web

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/viper"

	"github.com/iyear/tdl/pkg/consts"
)

func TestGlobalDownloadLimit(t *testing.T) {
	prev := viper.GetInt(consts.FlagLimit)
	viper.Set(consts.FlagLimit, 2)
	defer viper.Set(consts.FlagLimit, prev)

	var active, peak atomic.Int32
	release := make(chan struct{})
	entered := make(chan struct{}, 8)

	prevHook := testDownloadHook
	testDownloadHook = func(ctx context.Context, id string) error {
		n := active.Add(1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		entered <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			active.Add(-1)
			return ctx.Err()
		}
		active.Add(-1)
		return nil
	}
	defer func() { testDownloadHook = prevHook }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s := &Server{
		ctx:         ctx,
		items:       map[string]*Item{},
		order:       nil,
		finished:    map[int]struct{}{},
		downloading: map[string]struct{}{},
		cancels:     map[string]context.CancelFunc{},
		events:      make(chan struct{}, 1),
	}
	ids := []string{"a", "b", "c", "d", "e"}
	for i, id := range ids {
		s.items[id] = &Item{
			ID:         id,
			Type:       mediaImage,
			Status:     statusQueued,
			LogicalPos: i,
			Size:       1,
		}
		s.order = append(s.order, id)
	}

	s.enqueueDownloads(ctx, ids)

	// Wait until 2 workers are inside the hook (at limit).
	deadline := time.After(3 * time.Second)
	got := 0
	for got < 2 {
		select {
		case <-entered:
			got++
		case <-deadline:
			t.Fatalf("timed out waiting for workers; got=%d peak=%d pending=%d active=%d",
				got, peak.Load(), s.pendingDownloadCount(), s.activeDownloadCount())
		}
	}

	// Give scheduler a moment; must not exceed limit 2.
	time.Sleep(150 * time.Millisecond)
	if p := peak.Load(); p > 2 {
		t.Fatalf("peak concurrent downloads = %d, want <= 2", p)
	}
	if n := active.Load(); n > 2 {
		t.Fatalf("active downloads = %d, want <= 2", n)
	}

	close(release)
	deadline = time.After(3 * time.Second)
	for s.activeDownloadCount() > 0 || s.pendingDownloadCount() > 0 {
		select {
		case <-deadline:
			t.Fatalf("downloads did not finish; active=%d pending=%d",
				s.activeDownloadCount(), s.pendingDownloadCount())
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}
	if p := peak.Load(); p > 2 {
		t.Fatalf("peak concurrent downloads = %d, want <= 2", p)
	}
}

func TestStartDownloadNowPrependsQueue(t *testing.T) {
	prev := viper.GetInt(consts.FlagLimit)
	viper.Set(consts.FlagLimit, 1)
	defer viper.Set(consts.FlagLimit, prev)

	gate := make(chan struct{})
	order := make(chan string, 8)

	prevHook := testDownloadHook
	testDownloadHook = func(ctx context.Context, id string) error {
		order <- id
		<-gate
		return nil
	}
	defer func() { testDownloadHook = prevHook }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s := &Server{
		ctx:         ctx,
		items:       map[string]*Item{},
		finished:    map[int]struct{}{},
		downloading: map[string]struct{}{},
		cancels:     map[string]context.CancelFunc{},
		events:      make(chan struct{}, 1),
	}
	for i, id := range []string{"bg1", "bg2", "play"} {
		s.items[id] = &Item{ID: id, Type: mediaVideo, Status: statusQueued, LogicalPos: i, Size: 1}
		s.order = append(s.order, id)
	}

	s.enqueueDownload("bg1")
	// Wait for bg1 to hold the only slot.
	select {
	case id := <-order:
		if id != "bg1" {
			t.Fatalf("first download = %s, want bg1", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for bg1")
	}

	s.enqueueDownload("bg2")
	s.startDownloadNow("play")

	close(gate)

	var second, third string
	select {
	case second = <-order:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for second")
	}
	select {
	case third = <-order:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for third")
	}
	if second != "play" {
		t.Fatalf("priority play should run second, got order second=%s third=%s", second, third)
	}
}

func TestPriorityReserveSlot(t *testing.T) {
	prev := viper.GetInt(consts.FlagLimit)
	viper.Set(consts.FlagLimit, 2)
	defer viper.Set(consts.FlagLimit, prev)

	var active, peak atomic.Int32
	gates := map[string]chan struct{}{
		"imgA": make(chan struct{}),
		"imgB": make(chan struct{}),
		"imgC": make(chan struct{}),
		"play": make(chan struct{}),
	}
	entered := make(chan string, 8)

	prevHook := testDownloadHook
	testDownloadHook = func(ctx context.Context, id string) error {
		n := active.Add(1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		entered <- id
		select {
		case <-gates[id]:
		case <-ctx.Done():
			active.Add(-1)
			return ctx.Err()
		}
		active.Add(-1)
		return nil
	}
	defer func() { testDownloadHook = prevHook }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s := &Server{
		ctx:         ctx,
		items:       map[string]*Item{},
		finished:    map[int]struct{}{},
		downloading: map[string]struct{}{},
		cancels:     map[string]context.CancelFunc{},
		events:      make(chan struct{}, 1),
	}
	for i, id := range []string{"imgA", "imgB", "imgC", "play"} {
		typ := mediaImage
		if id == "play" {
			typ = mediaVideo
		}
		s.items[id] = &Item{ID: id, Type: typ, Status: statusQueued, LogicalPos: i, Size: 1}
		s.order = append(s.order, id)
	}

	s.enqueueDownloads(ctx, []string{"imgA", "imgB", "imgC"})

	var running []string
	deadline := time.After(3 * time.Second)
	for len(running) < 2 {
		select {
		case id := <-entered:
			running = append(running, id)
		case <-deadline:
			t.Fatalf("timeout waiting for 2 background downloads; got=%v", running)
		}
	}
	time.Sleep(100 * time.Millisecond)
	if peak.Load() > 2 {
		t.Fatalf("peak=%d want <= 2", peak.Load())
	}
	if active.Load() != 2 {
		t.Fatalf("active=%d want 2", active.Load())
	}

	s.startDownloadNow("play")
	// Free one borrowed background slot; reserved capacity should go to play, not imgC.
	close(gates[running[0]])

	deadline = time.After(3 * time.Second)
	var sawPlay bool
	for !sawPlay {
		select {
		case id := <-entered:
			if id == "play" {
				sawPlay = true
			} else if id == "imgC" || (id != running[0] && id != running[1]) {
				t.Fatalf("unexpected %s started before priority play", id)
			}
		case <-deadline:
			t.Fatal("timeout waiting for priority play to start")
		}
	}
	if peak.Load() > 2 {
		t.Fatalf("peak=%d want <= 2", peak.Load())
	}

	for _, g := range gates {
		select {
		case <-g:
		default:
			close(g)
		}
	}
	deadline = time.After(3 * time.Second)
	for s.activeDownloadCount() > 0 || s.pendingDownloadCount() > 0 {
		select {
		case <-deadline:
			t.Fatalf("downloads did not finish; active=%d pending=%d",
				s.activeDownloadCount(), s.pendingDownloadCount())
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}
}

func TestAlignDown(t *testing.T) {
	cases := []struct {
		n, align, want int64
	}{
		{0, 1024, 0},
		{1023, 1024, 0},
		{1024, 1024, 1024},
		{1025, 1024, 1024},
		{1048576 + 500, 1024, 1048576},
		{500, 0, 500},
	}
	for _, c := range cases {
		if got := alignDown(c.n, c.align); got != c.want {
			t.Fatalf("alignDown(%d,%d)=%d want %d", c.n, c.align, got, c.want)
		}
	}
}
