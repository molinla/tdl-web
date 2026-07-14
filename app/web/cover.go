package web

import (
	"context"
	"net/http"
	"os"
	"time"

	"github.com/go-faster/errors"
)

const (
	defaultCoverLimit    = 1
	coverWaitDefault     = 30 * time.Second
	coverWaitRetry       = 90 * time.Second
	coverPollInterval    = 200 * time.Millisecond
	coverRetryQueueDepth = 3
	coverFailureCooldown = 5 * time.Minute
)

func coverLimit() int {
	return defaultCoverLimit
}

func (s *Server) ensureCoverScheduler() {
	s.coverOnce.Do(func() {
		s.coverVisible = map[string]struct{}{}
		s.coverPending = map[string]struct{}{}
		s.coverActive = map[string]bool{}
		s.coverCancels = map[string]context.CancelFunc{}
		s.coverFailed = map[string]time.Time{}
		s.coverWake = make(chan struct{}, 1)
		s.tgCover = make(chan struct{}, coverLimit())
		go s.coverSchedulerLoop()
	})
}

func (s *Server) wakeCoverScheduler() {
	select {
	case s.coverWake <- struct{}{}:
	default:
	}
}

func (s *Server) wakeCoverLocked() {
	select {
	case s.coverWake <- struct{}{}:
	default:
	}
}

// enqueueCoverBuild queues a Telegram cover build. Returns 1-based queue position,
// or 0 when the item is already building or the cache is ready.
func (s *Server) enqueueCoverBuild(id string, priority bool) int {
	s.ensureCoverScheduler()
	if validThumbCacheFile(s.thumbCachePath(id)) {
		return 0
	}

	s.coverMu.Lock()
	defer s.coverMu.Unlock()
	if s.coverPaused {
		return 0
	}

	if retryAt, ok := s.coverFailed[id]; ok {
		if time.Now().Before(retryAt) {
			return 0
		}
		delete(s.coverFailed, id)
	}
	if _, ok := s.coverActive[id]; ok {
		return 0
	}
	if _, ok := s.coverPending[id]; ok {
		if priority {
			s.removeFromCoverQueueLocked(id)
			s.coverPriQueue = append([]string{id}, s.coverPriQueue...)
		}
		return s.coverPositionLocked(id)
	}

	s.coverPending[id] = struct{}{}
	if priority {
		s.coverPriQueue = append([]string{id}, s.coverPriQueue...)
	} else {
		s.coverQueue = append(s.coverQueue, id)
	}
	pos := s.coverPositionLocked(id)
	s.wakeCoverLocked()
	return pos
}

func (s *Server) setCoverState(paused bool, ids []string) {
	s.ensureCoverScheduler()

	visible := make(map[string]struct{}, len(ids))
	ordered := make([]string, 0, len(ids))
	s.mu.RLock()
	for _, id := range ids {
		item := s.items[id]
		if item == nil || item.Type != mediaVideo {
			continue
		}
		if _, ok := visible[id]; ok {
			continue
		}
		visible[id] = struct{}{}
		if !hasLocalVideoCoverSource(item.TargetPath) && !validThumbCacheFile(s.thumbCachePath(id)) {
			ordered = append(ordered, id)
		}
	}
	s.mu.RUnlock()

	var cancels []context.CancelFunc
	s.coverMu.Lock()
	s.coverPaused = paused
	s.coverVisible = visible
	for _, id := range s.coverPriQueue {
		delete(s.coverPending, id)
	}
	for _, id := range s.coverQueue {
		delete(s.coverPending, id)
	}
	s.coverPriQueue = nil
	s.coverQueue = nil

	for id, priority := range s.coverActive {
		_, stillVisible := visible[id]
		if paused || (priority && !stillVisible) || (!priority && len(ordered) > 0) {
			if cancel := s.coverCancels[id]; cancel != nil {
				cancels = append(cancels, cancel)
			}
		}
	}
	if !paused {
		for i := len(ordered) - 1; i >= 0; i-- {
			id := ordered[i]
			if _, active := s.coverActive[id]; active || validThumbCacheFile(s.thumbCachePath(id)) {
				continue
			}
			s.coverPending[id] = struct{}{}
			s.coverPriQueue = append([]string{id}, s.coverPriQueue...)
		}
	}
	s.coverMu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}
	s.wakeCoverScheduler()
}

func (s *Server) coverRequestPolicy(id, itemType string, requestedPriority bool) (priority, allowed bool) {
	s.ensureCoverScheduler()
	s.coverMu.Lock()
	defer s.coverMu.Unlock()
	if s.coverPaused {
		return false, false
	}
	if itemType != mediaVideo {
		return false, true
	}
	_, visible := s.coverVisible[id]
	return visible || requestedPriority, visible || requestedPriority
}

func (s *Server) preemptCoversForPlayback() {
	s.ensureCoverScheduler()
	var cancels []context.CancelFunc
	s.coverMu.Lock()
	for _, id := range s.coverPriQueue {
		delete(s.coverPending, id)
	}
	for _, id := range s.coverQueue {
		delete(s.coverPending, id)
	}
	s.coverPriQueue = nil
	s.coverQueue = nil
	for _, cancel := range s.coverCancels {
		cancels = append(cancels, cancel)
	}
	s.coverMu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	s.wakeCoverScheduler()
}

func (s *Server) removeFromCoverQueueLocked(id string) {
	delete(s.coverPending, id)
	s.coverPriQueue = filterID(s.coverPriQueue, id)
	s.coverQueue = filterID(s.coverQueue, id)
}

func (s *Server) coverPositionLocked(id string) int {
	if _, ok := s.coverActive[id]; ok {
		return 0
	}
	pos := 1
	for _, x := range s.coverPriQueue {
		if x == id {
			return pos
		}
		pos++
	}
	for _, x := range s.coverQueue {
		if x == id {
			return pos
		}
		pos++
	}
	return 0
}

func shouldWaitCover(queuePos int, retry bool) bool {
	if queuePos <= 1 {
		return true
	}
	return retry && queuePos <= coverRetryQueueDepth
}

func coverWaitTimeout(retry bool) time.Duration {
	if retry {
		return coverWaitRetry
	}
	return coverWaitDefault
}

func (s *Server) waitCoverBuild(ctx context.Context, id string) error {
	thumbPath := s.thumbCachePath(id)
	ticker := time.NewTicker(coverPollInterval)
	defer ticker.Stop()
	for {
		if validThumbCacheFile(thumbPath) {
			return nil
		}
		if !s.coverBuildPending(id) {
			return errors.New("cover build failed")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *Server) coverBuildPending(id string) bool {
	s.coverMu.Lock()
	defer s.coverMu.Unlock()
	if _, ok := s.coverActive[id]; ok {
		return true
	}
	if _, ok := s.coverPending[id]; ok {
		return true
	}
	return false
}

func (s *Server) serveCoverFromCache(w http.ResponseWriter, r *http.Request, id string) bool {
	thumbPath := s.thumbCachePath(id)
	if !validThumbCacheFile(thumbPath) {
		return false
	}
	f, err := os.Open(thumbPath)
	if err != nil {
		return false
	}
	defer f.Close()
	setMediaCacheHeaders(w)
	serveLocalFile(w, r, f, id+".jpg", "image/jpeg")
	return true
}

func (s *Server) tryServeTelegramCover(
	w http.ResponseWriter,
	r *http.Request,
	ctx context.Context,
	id string,
	itemType string,
	resolved *Item,
	priority bool,
	retryRequest bool,
) bool {
	if resolved == nil {
		return false
	}

	needsCover := false
	if resolved.thumb != nil {
		if itemType == mediaImage && sameMediaPayload(resolved.thumb, resolved.media) {
			return false
		}
		if resolved.thumb.Size <= thumbCacheMaxBytes {
			needsCover = true
		} else if itemType != mediaImage {
			return false
		}
	} else if itemType == mediaVideo && resolved.media != nil {
		needsCover = true
	} else if itemType == mediaImage && resolved.media != nil && resolved.media.Size <= thumbCacheMaxBytes {
		needsCover = true
	}

	if !needsCover {
		return false
	}

	queuePos := s.enqueueCoverBuild(id, priority)
	if !shouldWaitCover(queuePos, retryRequest || priority) {
		return false
	}

	waitCtx, cancel := context.WithTimeout(r.Context(), coverWaitTimeout(retryRequest))
	defer cancel()
	_ = s.waitCoverBuild(waitCtx, id)
	return s.serveCoverFromCache(w, r, id)
}

func (s *Server) coverSchedulerLoop() {
	ctx := s.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		s.coverMu.Lock()
		active := len(s.coverActive)
		var id string
		var priority bool
		var ok bool
		if !s.coverPaused && active < coverLimit() {
			id, priority, ok = s.popCoverJobLocked()
			if ok {
				s.coverActive[id] = priority
			}
		}
		var jobCtx context.Context
		var cancel context.CancelFunc
		if ok {
			jobCtx, cancel = context.WithCancel(ctx)
			s.coverCancels[id] = cancel
		}
		s.coverMu.Unlock()

		if !ok {
			select {
			case <-s.coverWake:
			case <-ctx.Done():
				return
			}
			continue
		}

		go func(jobID string, jobPriority bool, runCtx context.Context, cancel context.CancelFunc) {
			defer cancel()
			if jobPriority {
				s.beginCoverBandwidth(runCtx)
				defer s.endCoverBandwidth()
			}
			var err error
			if runCtx.Err() != nil {
				err = runCtx.Err()
			} else {
				err = s.ensureThumbCache("thumb:"+jobID, s.thumbCachePath(jobID), func() error {
					return s.buildCoverFromTelegram(runCtx, jobID)
				})
			}
			canceled := errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
			cooldown := err != nil && !canceled && s.shouldCooldownCoverFailure(jobID)
			s.coverMu.Lock()
			if cooldown {
				s.coverFailed[jobID] = time.Now().Add(coverFailureCooldown)
			} else {
				delete(s.coverFailed, jobID)
			}
			delete(s.coverActive, jobID)
			delete(s.coverCancels, jobID)
			delete(s.coverPending, jobID)
			s.coverMu.Unlock()
			s.wakeCoverScheduler()
		}(id, priority, jobCtx, cancel)
	}
}

func (s *Server) shouldCooldownCoverFailure(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item := s.items[id]
	return item != nil && item.Type == mediaVideo && item.thumb == nil && item.media != nil
}

func (s *Server) popCoverJobLocked() (string, bool, bool) {
	for len(s.coverPriQueue) > 0 {
		id := s.coverPriQueue[0]
		s.coverPriQueue = s.coverPriQueue[1:]
		if s.itemNeedsCoverBuild(id) {
			return id, true, true
		}
		delete(s.coverPending, id)
	}
	for len(s.coverQueue) > 0 {
		id := s.coverQueue[0]
		s.coverQueue = s.coverQueue[1:]
		if s.itemNeedsCoverBuild(id) {
			return id, false, true
		}
		delete(s.coverPending, id)
	}
	return "", false, false
}

func (s *Server) itemNeedsCoverBuild(id string) bool {
	if validThumbCacheFile(s.thumbCachePath(id)) {
		return false
	}
	s.mu.RLock()
	item := s.items[id]
	s.mu.RUnlock()
	return item != nil
}

func hasLocalVideoCoverSource(target string) bool {
	for _, path := range []string{target, target + tempExt} {
		if st, err := os.Stat(path); err == nil && st.Size() >= posterMinLocalBytes {
			return true
		}
	}
	return false
}

func (s *Server) acquireTGCover(ctx context.Context) bool {
	s.ensureCoverScheduler()
	select {
	case s.tgCover <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *Server) releaseTGCover() {
	select {
	case <-s.tgCover:
	default:
	}
}

func (s *Server) buildCoverFromTelegram(ctx context.Context, id string) error {
	if coverBuildHook != nil {
		return coverBuildHook(s, ctx, id)
	}
	thumbPath := s.thumbCachePath(id)
	if validThumbCacheFile(thumbPath) {
		return nil
	}

	s.mu.RLock()
	item := s.items[id]
	if item == nil {
		s.mu.RUnlock()
		return errors.New("item not found")
	}
	itemType := item.Type
	itemStatus := item.Status
	s.mu.RUnlock()

	if itemStatus == statusError {
		s.resetItemError(id)
	}

	resolved, err := s.ensureMedia(ctx, id)
	if err != nil {
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			s.markItemError(id, err)
		}
		return err
	}
	if resolved == nil {
		return errors.New("item not found")
	}

	if resolved.thumb != nil {
		thumb := resolved.thumb
		if itemType == mediaImage && sameMediaPayload(thumb, resolved.media) {
			// Handled by image streaming in handleThumb.
		} else if len(thumb.Inline) > 0 {
			return writeInlineThumb(thumb.Inline, thumbPath)
		} else if thumb.Size <= thumbCacheMaxBytes {
			if !s.acquireTGCover(ctx) {
				return ctx.Err()
			}
			defer s.releaseTGCover()
			return s.cacheMediaFile(ctx, thumb, thumbPath)
		} else if itemType != mediaImage {
			return errors.New("thumb too large for cache")
		}
	}

	if itemType == mediaVideo && resolved.media != nil {
		if !canExtractRemoteVideoPoster(resolved.media) {
			return errors.New("remote poster unsupported for media")
		}
		if !s.acquireTGCover(ctx) {
			return ctx.Err()
		}
		defer s.releaseTGCover()
		return s.extractRemoteVideoPoster(ctx, resolved.media, thumbPath)
	}

	if itemType == mediaImage && resolved.media != nil && resolved.media.Size <= thumbCacheMaxBytes {
		if !s.acquireTGCover(ctx) {
			return ctx.Err()
		}
		defer s.releaseTGCover()
		return s.cacheMediaFile(ctx, resolved.media, thumbPath)
	}

	return errors.New("no cover source")
}

func (s *Server) coverActivityCounts() (building, queued int) {
	s.ensureCoverScheduler()
	s.coverMu.Lock()
	defer s.coverMu.Unlock()
	return len(s.coverActive), len(s.coverPriQueue) + len(s.coverQueue)
}

// coverBuildHook if set, replaces buildCoverFromTelegram body (unit tests only).
var coverBuildHook func(*Server, context.Context, string) error
