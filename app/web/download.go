package web

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/go-faster/errors"
	"github.com/gotd/contrib/partio"
	"github.com/gotd/contrib/tg_io"
	"github.com/spf13/viper"

	"github.com/iyear/tdl/pkg/consts"
)

var (
	renameFile              = os.Rename
	errTmpPromotionDeferred = errors.New("tmp promotion deferred")
	promoteTmpRetryDelay    = 100 * time.Millisecond
)

const promoteTmpAttempts = 5

func downloadLimit() int {
	limit := viper.GetInt(consts.FlagLimit)
	if limit <= 0 {
		return 2
	}
	return limit
}

// downloadReservedSlots is capacity held for priority (play) downloads.
func downloadReservedSlots(limit int) int {
	if limit <= 0 {
		return 0
	}
	if limit == 1 {
		return 1
	}
	return 1
}

func (s *Server) ensureDownloadScheduler() {
	s.dlOnce.Do(func() {
		s.dlWake = make(chan struct{}, 1)
		s.dlPending = map[string]struct{}{}
		go s.downloadSchedulerLoop()
	})
}

func (s *Server) wakeDownloader() {
	select {
	case s.dlWake <- struct{}{}:
	default:
	}
}

// enqueueDownload queues id at the back (background / download-all / images).
func (s *Server) enqueueDownload(id string) {
	s.ensureDownloadScheduler()
	s.queuePush(id, false, true, true)
}

func (s *Server) enqueueDownloadBackground(id string) {
	s.ensureDownloadScheduler()
	s.queuePush(id, false, true, false)
}

// startDownloadNow queues id at the front (play / explicit priority).
func (s *Server) startDownloadNow(id string) {
	s.ensureDownloadScheduler()
	if s.queuePush(id, true, true, true) {
		s.preemptBackgroundDownload()
	}
}

func (s *Server) enqueueDownloads(_ context.Context, ids []string) {
	s.enqueueDownloadsWithPausePolicy(ids, false)
}

func (s *Server) enqueueDownloadsExplicit(_ context.Context, ids []string) {
	s.enqueueDownloadsWithPausePolicy(ids, true)
}

func (s *Server) enqueueDownloadsWithPausePolicy(ids []string, clearManualPaused bool) {
	s.ensureDownloadScheduler()
	changed := false
	for _, id := range ids {
		if s.queuePush(id, false, false, clearManualPaused) {
			changed = true
		}
	}
	if changed {
		s.notify()
		s.wakeDownloader()
	}
}

// queuePush adds id to the wait queue. If doNotify is true, SSE clients are updated.
// Returns whether the queue changed.
func (s *Server) queuePush(id string, priority bool, doNotify bool, clearManualPaused bool) bool {
	if id == "" {
		return false
	}
	s.mu.Lock()
	it := s.items[id]
	skip := it == nil || it.Status == statusCompleted || (it.ManualPaused && !clearManualPaused)
	_, active := s.downloading[id]
	if it != nil && clearManualPaused {
		it.ManualPaused = false
	}
	s.mu.Unlock()
	if skip || active {
		return false
	}

	changed := false
	s.dlMu.Lock()
	if s.dlPending == nil {
		s.dlPending = map[string]struct{}{}
	}
	if _, ok := s.dlPending[id]; ok {
		if priority {
			// Move existing entry to priority front.
			s.removeFromQueueLocked(id)
			s.dlPriQueue = append([]string{id}, s.dlPriQueue...)
			s.dlPending[id] = struct{}{}
			changed = true
		}
		s.dlMu.Unlock()
		if changed {
			if doNotify {
				s.notify()
			}
			s.wakeDownloader()
		}
		return changed
	}
	s.dlPending[id] = struct{}{}
	if priority {
		s.dlPriQueue = append([]string{id}, s.dlPriQueue...)
	} else {
		s.dlQueue = append(s.dlQueue, id)
	}
	s.dlMu.Unlock()
	if doNotify {
		s.notify()
	}
	s.wakeDownloader()
	return true
}

func (s *Server) preemptBackgroundDownload() {
	s.dlMu.Lock()
	canPreempt := s.dlActive >= downloadLimit() && s.dlActive > s.dlActivePri
	s.dlMu.Unlock()
	if !canPreempt {
		return
	}
	s.cancelOneBackgroundDownload()
}

// cancelOneBackgroundDownload marks one non-priority download as preempted and
// cancels it. The download is re-queued when its goroutine exits.
// Returns true if a download was canceled.
func (s *Server) cancelOneBackgroundDownload() bool {
	var cancel context.CancelFunc
	s.mu.Lock()
	for id := range s.downloading {
		if s.dlPriority[id] {
			continue
		}
		cancel = s.cancels[id]
		if cancel != nil {
			if s.preempted == nil {
				s.preempted = map[string]struct{}{}
			}
			s.preempted[id] = struct{}{}
			break
		}
	}
	s.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

func (s *Server) downloadLimitWithCoverHoldLocked() int {
	limit := downloadLimit() - s.coverBandwidthHold
	if limit < 0 {
		return 0
	}
	return limit
}

// beginCoverBandwidth reserves download capacity for cover Telegram work and
// preempts all background downloads. Priority (play) downloads are kept.
// A single remaining multi-threaded download is enough to starve covers, so
// freeing only one slot is not sufficient.
func (s *Server) beginCoverBandwidth(ctx context.Context) {
	s.ensureDownloadScheduler()
	s.dlMu.Lock()
	reserve := downloadLimit() - s.dlActivePri
	if reserve < 1 {
		reserve = 1
	}
	s.coverBandwidthHold += reserve
	s.dlMu.Unlock()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for s.cancelOneBackgroundDownload() {
		}
		s.dlMu.Lock()
		bgActive := s.dlActive - s.dlActivePri
		cap := s.downloadLimitWithCoverHoldLocked()
		ok := bgActive <= 0 && s.dlActive <= cap
		s.dlMu.Unlock()
		if ok {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (s *Server) endCoverBandwidth() {
	s.dlMu.Lock()
	// coverLimit is 1; clear the reservation for this cover job.
	s.coverBandwidthHold = 0
	s.dlMu.Unlock()
	s.wakeDownloader()
}

func (s *Server) removeFromQueue(id string) {
	s.dlMu.Lock()
	s.removeFromQueueLocked(id)
	s.dlMu.Unlock()
}

func (s *Server) removeFromQueueLocked(id string) {
	if _, ok := s.dlPending[id]; !ok {
		return
	}
	delete(s.dlPending, id)
	s.dlPriQueue = filterID(s.dlPriQueue, id)
	s.dlQueue = filterID(s.dlQueue, id)
}

func filterID(in []string, id string) []string {
	out := in[:0]
	for _, x := range in {
		if x != id {
			out = append(out, x)
		}
	}
	return out
}

func (s *Server) popPriorityLocked() (string, bool) {
	for len(s.dlPriQueue) > 0 {
		id := s.dlPriQueue[0]
		s.dlPriQueue = s.dlPriQueue[1:]
		delete(s.dlPending, id)
		// Release dlMu before s.mu to keep lock order (mu → dlMu in queuePush).
		s.dlMu.Unlock()
		s.mu.RLock()
		it := s.items[id]
		_, active := s.downloading[id]
		skip := it == nil || it.Status == statusCompleted || active
		s.mu.RUnlock()
		s.dlMu.Lock()
		if skip {
			continue
		}
		return id, true
	}
	return "", false
}

func (s *Server) popBackgroundLocked() (string, bool) {
	for len(s.dlQueue) > 0 {
		id := s.dlQueue[0]
		s.dlQueue = s.dlQueue[1:]
		delete(s.dlPending, id)
		s.dlMu.Unlock()
		s.mu.RLock()
		it := s.items[id]
		_, active := s.downloading[id]
		skip := it == nil || it.Status == statusCompleted || active
		s.mu.RUnlock()
		s.dlMu.Lock()
		if skip {
			continue
		}
		return id, true
	}
	return "", false
}

// tryStartDownloadLocked picks the next job that fits reserved/shared slot rules.
// Caller must hold dlMu. On success, dlActive(/Pri) are incremented.
func (s *Server) tryStartDownloadLocked() (id string, priority bool, ok bool) {
	limit := s.downloadLimitWithCoverHoldLocked()
	reserved := downloadReservedSlots(limit)
	sharedCap := limit - reserved
	if s.dlActive >= limit {
		return "", false, false
	}

	hasPriWaiting := len(s.dlPriQueue) > 0

	// Priority jobs may use any free slot (including the reserved one).
	if hasPriWaiting {
		if id, ok := s.popPriorityLocked(); ok {
			s.dlActive++
			s.dlActivePri++
			return id, true, true
		}
		hasPriWaiting = len(s.dlPriQueue) > 0
	}

	// Background: shared slots only; borrow reserved when no priority work is
	// waiting or running.
	bgCap := sharedCap
	if !hasPriWaiting && s.dlActivePri == 0 {
		bgCap = limit
	}
	bgActive := s.dlActive - s.dlActivePri
	if bgActive >= bgCap || s.dlActive >= limit {
		return "", false, false
	}
	if id, ok := s.popBackgroundLocked(); ok {
		s.dlActive++
		return id, false, true
	}
	return "", false, false
}

func (s *Server) downloadSchedulerLoop() {
	ctx := s.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		s.dlMu.Lock()
		id, pri, ok := s.tryStartDownloadLocked()
		s.dlMu.Unlock()
		if !ok {
			select {
			case <-s.dlWake:
			case <-ctx.Done():
				return
			}
			continue
		}
		go func(id string, pri bool) {
			defer func() {
				s.dlMu.Lock()
				s.dlActive--
				if pri {
					s.dlActivePri--
				}
				if s.dlActive < 0 {
					s.dlActive = 0
				}
				if s.dlActivePri < 0 {
					s.dlActivePri = 0
				}
				s.dlMu.Unlock()
				s.wakeDownloader()
			}()
			s.startDownload(ctx, id, pri)
			s.waitDownload(id)
		}(id, pri)
	}
}

// startDownload begins a single item download without acquiring the global slot.
// Callers must go through the scheduler (enqueueDownload / startDownloadNow).
func (s *Server) startDownload(parent context.Context, id string, priority bool) {
	if parent == nil {
		parent = s.ctx
	}
	if parent == nil {
		parent = context.Background()
	}

	s.mu.Lock()
	if _, ok := s.downloading[id]; ok {
		s.mu.Unlock()
		return
	}
	item := s.items[id]
	if item == nil || item.Status == statusCompleted {
		s.mu.Unlock()
		return
	}
	dlCtx, cancel := context.WithCancel(parent)
	if s.cancels == nil {
		s.cancels = map[string]context.CancelFunc{}
	}
	if s.dlPriority == nil {
		s.dlPriority = map[string]bool{}
	}
	s.cancels[id] = cancel
	s.downloading[id] = struct{}{}
	s.dlPriority[id] = priority
	item.Status = statusCaching
	item.Error = ""
	s.mu.Unlock()
	s.notify()

	go func() {
		err := s.downloadItem(dlCtx, id)
		s.mu.Lock()
		delete(s.downloading, id)
		delete(s.cancels, id)
		delete(s.dlPriority, id)
		_, wasPreempted := s.preempted[id]
		delete(s.preempted, id)
		if it := s.items[id]; it != nil {
			switch {
			case err == nil:
				// markCompleted already set status
			case errors.Is(err, errTmpPromotionDeferred):
				it.Status = statusPaused
				it.Error = ""
				if p := tmpProgress(it.TargetPath, it.Size); p > it.Progress {
					it.Progress = p
				}
			case errors.Is(err, context.Canceled) && wasPreempted:
				it.Status = statusQueued
				it.Error = ""
				if p := tmpProgress(it.TargetPath, it.Size); p > it.Progress {
					it.Progress = p
				}
			case errors.Is(err, context.Canceled):
				it.Status = statusPaused
				it.Error = ""
				if p := tmpProgress(it.TargetPath, it.Size); p > it.Progress {
					it.Progress = p
				}
			default:
				it.Status = statusError
				it.Error = err.Error()
				if p := tmpProgress(it.TargetPath, it.Size); p > 0 {
					it.Progress = p
				}
			}
		}
		s.mu.Unlock()
		_ = s.saveFinishedOrClear(context.Background())
		_ = s.saveMetaCache()
		s.notify()
		if wasPreempted {
			s.enqueueDownloadBackground(id)
		}
	}()
}

func (s *Server) pauseDownload(id string) {
	s.removeFromQueue(id)
	s.mu.Lock()
	cancel, active := s.cancels[id]
	item := s.items[id]
	if item != nil {
		item.ManualPaused = true
	}
	delete(s.preempted, id)
	if active {
		s.mu.Unlock()
		cancel()
		return
	}
	if item != nil && item.Status != statusCompleted {
		if p := tmpProgress(item.TargetPath, item.Size); p > 0 {
			item.Progress = p
		}
		if item.Status == statusQueued || item.Status == statusError || item.Progress > 0 {
			item.Status = statusPaused
			item.Error = ""
		}
	}
	s.mu.Unlock()
	_ = s.saveMetaCache()
	s.notify()
}

func (s *Server) markItemError(id string, err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	if it := s.items[id]; it != nil && it.Status != statusCompleted {
		it.Status = statusError
		it.Error = err.Error()
	}
	s.mu.Unlock()
	_ = s.saveMetaCache()
	s.notify()
}

func (s *Server) resetItemError(id string) {
	changed := false
	s.mu.Lock()
	if it := s.items[id]; it != nil && it.Status == statusError {
		it.Status = statusQueued
		it.Error = ""
		changed = true
	}
	s.mu.Unlock()
	if changed {
		_ = s.saveMetaCache()
		s.notify()
	}
}

// testDownloadHook if set, replaces downloadItem body (unit tests only).
var testDownloadHook func(ctx context.Context, id string) error

func (s *Server) downloadItem(ctx context.Context, id string) error {
	if testDownloadHook != nil {
		return testDownloadHook(ctx, id)
	}
	item, err := s.ensureMedia(ctx, id)
	if err != nil {
		return err
	}
	main := item.media
	if main == nil || main.Location == nil {
		return errors.New("media location unavailable")
	}
	target := item.TargetPath
	logical := item.LogicalPos
	size := main.Size
	if size <= 0 {
		size = item.Size
	}

	if err := os.MkdirAll(filepath.Dir(target), defaultCachePerm); err != nil {
		return err
	}
	if sameFileExists(target, size) {
		s.markCompleted(ctx, id, logical, size)
		return nil
	}

	tmp := target + tempExt
	startOffset := tmpProgress(target, size)
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}

	if startOffset > 0 {
		s.setProgress(id, startOffset)
	}
	if size > 0 && startOffset >= size {
		_ = f.Close()
		promoted, err := promoteTmp(target, size)
		if err != nil {
			return err
		}
		if !promoted {
			s.setProgress(id, tmpProgress(target, size))
			return errTmpPromotionDeferred
		}
		s.markCompleted(ctx, id, logical, size)
		return nil
	}

	api := s.pool.Client(ctx, main.DC)
	if s.opts.Takeout {
		api = s.pool.Takeout(ctx, main.DC)
	}

	// Telegram upload.getFile (precise): offset/limit must be divisible by 1KB and
	// limit ≤ 1MB. Always request a full aligned part; never shrink limit to the
	// remaining byte count (that causes LIMIT_INVALID on the last chunk).
	partSize := int64(downloadPartSize)
	// Align resume to 1KB (precise getFile). Streamer re-reads the enclosing part.
	startOffset = alignDown(startOffset, 1024)
	if err := f.Truncate(startOffset); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Seek(startOffset, io.SeekStart); err != nil {
		_ = f.Close()
		return err
	}
	s.setProgress(id, startOffset)

	src := tg_io.NewDownloader(api).ChunkSource(size, main.Location)
	streamer := partio.NewStreamer(src, partSize)
	pw := &downloadProgressWriter{
		w:      f,
		s:      s,
		id:     id,
		offset: startOffset,
		size:   size,
	}
	streamErr := streamer.StreamAt(ctx, startOffset, pw)
	if err := f.Sync(); err != nil {
		_ = f.Close()
		if streamErr != nil {
			return streamErr
		}
		return err
	}
	if err := f.Close(); err != nil {
		if streamErr != nil {
			return streamErr
		}
		return err
	}
	if streamErr != nil && !errors.Is(streamErr, io.EOF) {
		return streamErr
	}
	promoted, err := promoteTmp(target, size)
	if err != nil {
		return err
	}
	if !promoted {
		s.setProgress(id, tmpProgress(target, size))
		return errTmpPromotionDeferred
	}
	s.markCompleted(ctx, id, logical, size)
	return nil
}

type downloadProgressWriter struct {
	w      io.Writer
	s      *Server
	id     string
	offset int64
	size   int64
}

func (p *downloadProgressWriter) Write(b []byte) (int, error) {
	if p.size > 0 && p.offset >= p.size {
		return 0, io.EOF
	}
	if p.size > 0 && p.offset+int64(len(b)) > p.size {
		b = b[:p.size-p.offset]
	}
	n, err := p.w.Write(b)
	p.offset += int64(n)
	if n > 0 {
		p.s.setProgress(p.id, p.offset)
	}
	if err != nil {
		return n, err
	}
	if p.size > 0 && p.offset >= p.size {
		return n, io.EOF
	}
	return n, nil
}

func alignDown(n, align int64) int64 {
	if align <= 0 || n <= 0 {
		if n < 0 {
			return 0
		}
		return n
	}
	return n - (n % align)
}

func (s *Server) setProgress(id string, n int64) {
	s.mu.Lock()
	if it := s.items[id]; it != nil {
		it.Progress = n
	}
	s.mu.Unlock()
	s.notify()
}

func (s *Server) markCompleted(ctx context.Context, id string, logical int, size int64) {
	s.mu.Lock()
	if it := s.items[id]; it != nil {
		it.Status = statusCompleted
		it.Progress = size
		it.Error = ""
	}
	s.finished[logical] = struct{}{}
	s.mu.Unlock()
	if s.jelly != nil {
		s.jelly.RefreshSoon(ctx)
	}
	s.notify()
}

func tmpProgress(target string, size int64) int64 {
	st, err := os.Stat(target + tempExt)
	if err != nil {
		return 0
	}
	n := st.Size()
	if size > 0 && n > size {
		return size
	}
	if n < 0 {
		return 0
	}
	return n
}

func tmpComplete(target string, size int64) bool {
	if size <= 0 {
		return false
	}
	st, err := os.Stat(target + tempExt)
	return err == nil && st.Size() >= size
}

func promoteTmp(target string, size int64) (bool, error) {
	if promotedFileExists(target, size) {
		return true, nil
	}
	tmp := target + tempExt
	if size > 0 {
		if !tmpComplete(target, size) {
			return false, nil
		}
	} else if _, err := os.Stat(tmp); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	var lastErr error
	for i := 0; i < promoteTmpAttempts; i++ {
		if err := renameFile(tmp, target); err != nil {
			if promotedFileExists(target, size) {
				return true, nil
			}
			if !isTmpBusyRename(err) {
				return false, err
			}
			lastErr = err
			time.Sleep(promoteTmpRetryDelay)
			continue
		}
		return true, nil
	}
	if size <= 0 {
		if _, err := os.Stat(tmp); err == nil || os.IsNotExist(err) {
			return false, nil
		} else {
			return false, err
		}
	}
	if tmpComplete(target, size) {
		return false, nil
	}
	return false, lastErr
}

func promotedFileExists(target string, size int64) bool {
	if size > 0 {
		return sameFileExists(target, size)
	}
	_, err := os.Stat(target)
	return err == nil
}

func isTmpBusyRename(err error) bool {
	linkErr, ok := err.(*os.LinkError)
	if !ok {
		return false
	}
	errno, ok := linkErr.Err.(syscall.Errno)
	return ok && (errno == 32 || errno == 33)
}

// applyDiskProgress sets paused/completed from local files after import.
func applyDiskProgress(item *Item) {
	if item == nil || item.Status == statusCompleted {
		return
	}
	if sameFileExists(item.TargetPath, item.Size) {
		item.Status = statusCompleted
		item.Progress = item.Size
		return
	}
	if p := tmpProgress(item.TargetPath, item.Size); p > 0 {
		item.Progress = p
		item.Status = statusPaused
	}
}

func (s *Server) waitDownload(id string) {
	for {
		s.mu.RLock()
		_, active := s.downloading[id]
		s.mu.RUnlock()
		if !active {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// activeDownloadCount is used by tests.
func (s *Server) activeDownloadCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.downloading)
}

// pendingDownloadCount is used by tests.
func (s *Server) pendingDownloadCount() int {
	s.dlMu.Lock()
	defer s.dlMu.Unlock()
	return len(s.dlPriQueue) + len(s.dlQueue)
}
