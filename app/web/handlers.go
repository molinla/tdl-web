package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-faster/errors"
	"github.com/gorilla/mux"
	"github.com/gotd/contrib/http_io"
	"github.com/gotd/contrib/http_range"
	"github.com/gotd/contrib/partio"
	"github.com/gotd/contrib/tg_io"
	tgdownloader "github.com/gotd/td/telegram/downloader"
	"github.com/spf13/viper"

	"github.com/iyear/tdl/core/logctx"
	"github.com/iyear/tdl/core/util/tutil"
	"github.com/iyear/tdl/pkg/consts"
)

const streamChunkMaxBytes = 32 * 1024 * 1024

func (s *Server) handleItems(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.snapshot())
}

func (s *Server) snapshot() map[string]any {
	queuePos := s.queuePositions()
	downloading, queued := s.downloadActivityCounts()
	coverBuilding, coverQueued := s.coverActivityCounts()
	s.mu.RLock()
	defer s.mu.RUnlock()
	return map[string]any{
		"fingerprint":          s.fingerprint,
		"items":                s.itemListLocked(queuePos),
		"importing":            s.importing,
		"import_error":         s.importError,
		"import_total":         s.importTotal,
		"import_done":          s.importDone,
		"import_items":         len(s.order),
		"import_phase":         s.importPhase,
		"import_source":        s.importSource,
		"import_detail":        s.importDetail,
		"downloading_count":    downloading,
		"queued_count":         queued,
		"cover_building_count": coverBuilding,
		"cover_queued_count":   coverQueued,
	}
}

// queuePositions returns 1-based wait-queue index for each queued id.
// Priority (play) entries are listed first.
func (s *Server) queuePositions() map[string]int {
	s.dlMu.Lock()
	defer s.dlMu.Unlock()
	n := len(s.dlPriQueue) + len(s.dlQueue)
	if n == 0 {
		return nil
	}
	pos := make(map[string]int, n)
	i := 1
	for _, id := range s.dlPriQueue {
		pos[id] = i
		i++
	}
	for _, id := range s.dlQueue {
		pos[id] = i
		i++
	}
	return pos
}

func (s *Server) itemListLocked(queuePos map[string]int) []*Item {
	ret := make([]*Item, 0, len(s.order))
	for _, id := range s.order {
		if it := s.items[id]; it != nil {
			cp := *it
			cp.media = nil
			cp.thumb = nil
			cp.QueuePos = 0
			if queuePos != nil {
				if p, ok := queuePos[id]; ok && cp.Status != statusCaching {
					cp.QueuePos = p
				}
			}
			ret = append(ret, &cp)
		}
	}
	return ret
}

func (s *Server) handleImport(ctx context.Context) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		if s.importing {
			s.mu.Unlock()
			http.Error(w, "import already in progress", http.StatusConflict)
			return
		}
		s.importing = true
		s.importError = ""
		s.importPhase = phaseParseJSON
		s.importSource = sourceJSON
		s.importDetail = "读取 JSON 导出"
		s.mu.Unlock()
		s.notify()

		if err := os.MkdirAll(filepath.Join(s.opts.CacheDir, "imports"), defaultCachePerm); err != nil {
			s.setImporting(false, err.Error())
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		path := filepath.Join(s.opts.CacheDir, "imports", fmt.Sprintf("import-%d.json", time.Now().UnixNano()))

		rType := r.FormValue("type")
		fromStr := r.FormValue("from")
		toStr := r.FormValue("to")
		if rType == "" {
			rType = r.URL.Query().Get("type")
		}
		if fromStr == "" {
			fromStr = r.URL.Query().Get("from")
		}
		if toStr == "" {
			toStr = r.URL.Query().Get("to")
		}
		rangeType, rangeFrom, rangeTo, err := parseRangeForm(rType, fromStr, toStr)
		if err != nil {
			s.setImporting(false, err.Error())
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if rangeType == "" {
			rangeType, rangeFrom, rangeTo = s.opts.RangeType, s.opts.RangeFrom, s.opts.RangeTo
		}

		var src io.Reader
		file, _, err := r.FormFile("file")
		if err == nil {
			defer file.Close()
			src = file
		} else {
			src = r.Body
		}
		dst, err := os.Create(path)
		if err != nil {
			s.setImporting(false, err.Error())
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if _, err = io.Copy(dst, src); err != nil {
			_ = dst.Close()
			s.setImporting(false, err.Error())
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err = dst.Close(); err != nil {
			s.setImporting(false, err.Error())
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		go func() {
			if err := s.importSources(ctx, []string{path}, nil, rangeType, rangeFrom, rangeTo); err != nil {
				s.setImporting(false, err.Error())
				return
			}
			s.setImporting(false, "")
		}()
		writeJSON(w, map[string]any{"ok": true, "importing": true})
	}
}

func (s *Server) handleDownload(ctx context.Context) http.HandlerFunc {
	type request struct {
		IDs []string `json:"ids"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		var req request
		_ = json.NewDecoder(r.Body).Decode(&req)
		if len(req.IDs) == 0 {
			s.mu.RLock()
			for _, id := range s.order {
				req.IDs = append(req.IDs, id)
			}
			s.mu.RUnlock()
		}
		s.enqueueDownloadsExplicit(ctx, req.IDs)
		writeJSON(w, map[string]any{"ok": true})
	}
}

func (s *Server) handleCache(ctx context.Context) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := mux.Vars(r)["id"]
		s.enqueueDownload(id)
		writeJSON(w, map[string]any{"ok": true})
	}
}

func (s *Server) handlePause(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	s.pauseDownload(id)
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleThumb(ctx context.Context) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := mux.Vars(r)["id"]
		retryRequest := r.URL.Query().Get("retry") != ""
		s.mu.RLock()
		item := s.items[id]
		if item == nil {
			s.mu.RUnlock()
			http.NotFound(w, r)
			return
		}
		target := item.TargetPath
		itemType := item.Type
		mime := item.MIME
		itemStatus := item.Status
		s.mu.RUnlock()

		thumbPath := s.thumbCachePath(id)

		// 1) Disk thumb / poster cache (must be real JPEG, not a dumped mp4).
		if validThumbCacheFile(thumbPath) {
			if f, err := os.Open(thumbPath); err == nil {
				defer f.Close()
				setMediaCacheHeaders(w)
				serveLocalFile(w, r, f, id+".jpg", "image/jpeg")
				return
			}
		}

		// 2) Local image original (any size).
		if itemType == mediaImage {
			if f, err := os.Open(target); err == nil {
				defer f.Close()
				setMediaCacheHeaders(w)
				serveLocalFile(w, r, f, filepath.Base(target), mime)
				return
			}
		}

		// 3) Local video (complete or in-progress .tmp) -> first-frame JPEG.
		if itemType == mediaVideo {
			hasLocalVideo := false
			for _, path := range []string{target, target + tempExt} {
				if st, err := os.Stat(path); err == nil && st.Size() >= posterMinLocalBytes {
					hasLocalVideo = true
					break
				}
			}
			if hasLocalVideo {
				_ = s.ensureThumbCache("thumb:"+id, thumbPath, func() error {
					var lastErr error
					for _, path := range []string{target, target + tempExt} {
						if st, err := os.Stat(path); err == nil && st.Size() >= posterMinLocalBytes {
							if err := extractVideoPoster(path, thumbPath); err == nil {
								return nil
							} else {
								lastErr = err
							}
						}
					}
					if lastErr != nil {
						return lastErr
					}
					return errors.New("local video unavailable")
				})
				if validThumbCacheFile(thumbPath) {
					if f, err := os.Open(thumbPath); err == nil {
						defer f.Close()
						setMediaCacheHeaders(w)
						serveLocalFile(w, r, f, id+".jpg", "image/jpeg")
						return
					}
				}
			}
		}

		// 4–5) Telegram thumb / remote poster via isolated cover queue.
		if itemStatus == statusError {
			s.resetItemError(id)
		}
		resolved, ensureErr := s.ensureMedia(ctx, id)
		if ensureErr != nil {
			s.markItemError(id, ensureErr)
			serveThumbUnavailable(w)
			return
		}
		if resolved != nil && resolved.thumb != nil {
			thumb := resolved.thumb
			if itemType == mediaImage && sameMediaPayload(thumb, resolved.media) {
				// Stream image below; cache warming uses cover queue.
			} else if thumb.Size > thumbCacheMaxBytes && itemType != mediaImage {
				serveThumbUnavailable(w)
				return
			} else if s.tryServeTelegramCover(w, r, ctx, id, itemType, resolved, retryRequest) {
				return
			} else if itemType != mediaImage {
				serveThumbUnavailable(w)
				return
			}
		} else if itemType == mediaVideo && resolved != nil && resolved.media != nil {
			if s.tryServeTelegramCover(w, r, ctx, id, itemType, resolved, retryRequest) {
				return
			}
		}

		// 6) Image without local file: stream image media (not video).
		if itemType == mediaImage && resolved != nil && resolved.media != nil {
			src := resolved.media
			if src.Size <= thumbCacheMaxBytes {
				s.enqueueCoverBuild(id, retryRequest)
			}
			if !s.tryAcquireTGShared() {
				serveThumbUnavailable(w)
				return
			}
			defer s.releaseTGShared()
			setMediaCacheHeaders(w)
			s.serveTelegramMedia(ctx, src, w, r, false)
			return
		}

		serveThumbUnavailable(w)
	}
}

func (s *Server) handlePreview(ctx context.Context) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := mux.Vars(r)["id"]
		s.mu.RLock()
		item := s.items[id]
		if item == nil {
			s.mu.RUnlock()
			http.NotFound(w, r)
			return
		}
		if item.Type != mediaImage {
			s.mu.RUnlock()
			http.Error(w, "not an image", http.StatusBadRequest)
			return
		}
		target := item.TargetPath
		mime := item.MIME
		s.mu.RUnlock()

		if f, err := os.Open(target); err == nil {
			defer f.Close()
			setMediaCacheHeaders(w)
			serveLocalFile(w, r, f, filepath.Base(target), mime)
			return
		}
		// Prefer valid disk thumb while full image is still downloading.
		if validThumbCacheFile(s.thumbCachePath(id)) {
			if f, err := os.Open(s.thumbCachePath(id)); err == nil {
				defer f.Close()
				setMediaCacheHeaders(w)
				serveLocalFile(w, r, f, id+".jpg", "image/jpeg")
				return
			}
		}

		s.resetItemError(id)

		resolved, err := s.ensureMedia(ctx, id)
		if err != nil {
			s.markItemError(id, err)
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		src := resolved.media
		if src == nil {
			src = resolved.thumb
		}
		// Warm thumb cache in background for subsequent grid loads.
		if resolved.thumb != nil && resolved.thumb.Size <= thumbCacheMaxBytes {
			thumb := resolved.thumb
			thumbPath := s.thumbCachePath(id)
			go func() {
				_ = s.ensureThumbCache("thumb:"+id, thumbPath, func() error {
					return s.cacheMediaFile(ctx, thumb, thumbPath)
				})
			}()
		}
		if !s.tryAcquireTGShared() {
			http.Error(w, "media busy", http.StatusServiceUnavailable)
			return
		}
		defer s.releaseTGShared()
		setMediaCacheHeaders(w)
		s.serveTelegramMedia(ctx, src, w, r, true)
	}
}

func (s *Server) handleStream(ctx context.Context) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := mux.Vars(r)["id"]
		s.mu.RLock()
		item := s.items[id]
		if item == nil {
			s.mu.RUnlock()
			http.NotFound(w, r)
			return
		}
		target := item.TargetPath
		mime := item.MIME
		size := item.Size
		s.mu.RUnlock()
		if f, err := os.Open(target); err == nil {
			defer f.Close()
			serveStreamFile(w, r, f, filepath.Base(target), mime)
			return
		}
		if serveTmpRange(w, r, target+tempExt, mime, size) {
			if tmpComplete(target, size) {
				go s.promoteCompletedTmp(id)
			} else {
				s.startDownloadNow(id)
			}
			return
		}

		resolved, err := s.ensureMedia(ctx, id)
		if err != nil {
			s.markItemError(id, err)
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if resolved.media == nil || resolved.media.Location == nil {
			err := errors.New("media location unavailable")
			s.markItemError(id, err)
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		// Play-while-caching: priority slot, stream from Telegram and land full file in --dir.
		s.startDownloadNow(id)
		release, err := s.acquireTGStream(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		defer release()
		s.serveTelegramStream(ctx, resolved.media, w, r)
	}
}

func (s *Server) promoteCompletedTmp(id string) {
	ctx := s.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.RLock()
	item := s.items[id]
	if item == nil {
		s.mu.RUnlock()
		return
	}
	target := item.TargetPath
	size := item.Size
	logical := item.LogicalPos
	s.mu.RUnlock()

	promoted, err := promoteTmp(target, size)
	if err != nil || !promoted {
		return
	}
	s.markCompleted(ctx, id, logical, size)
	_ = s.saveFinishedOrClear(context.Background())
	_ = s.saveMetaCache()
}

func serveTmpRange(w http.ResponseWriter, r *http.Request, tmpPath, mime string, fullSize int64) bool {
	if fullSize <= 0 {
		return false
	}
	f, err := os.Open(tmpPath)
	if err != nil {
		return false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() <= 0 {
		return false
	}
	spec, ok, status, err := boundedTmpRange(r.Header.Get("Range"), st.Size(), fullSize)
	if err != nil {
		writeRangeError(w, status, fullSize, err)
		return true
	}
	if !ok {
		return false
	}
	if _, err := f.Seek(spec.start, io.SeekStart); err != nil {
		return false
	}
	if mime != "" {
		w.Header().Set("Content-Type", mime)
	}
	w.Header().Set("Accept-Ranges", "bytes")
	setStreamCacheHeaders(w)
	if spec.partial {
		w.Header().Set("Content-Range", spec.contentRange(fullSize))
	}
	w.Header().Set("Content-Length", strconv.FormatInt(spec.length, 10))
	if spec.partial {
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	if r.Method == http.MethodHead {
		return true
	}
	_, _ = io.CopyN(w, f, spec.length)
	return true
}

func (s *Server) handleFileDownload(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	s.mu.RLock()
	item := s.items[id]
	if item == nil {
		s.mu.RUnlock()
		http.NotFound(w, r)
		return
	}
	path := item.TargetPath
	name := item.Name
	ready := item.Status == statusCompleted
	s.mu.RUnlock()
	if !ready {
		http.Error(w, "file is not ready", http.StatusConflict)
		return
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, strings.ReplaceAll(name, `"`, `'`)))
	http.ServeFile(w, r, path)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	send := func() bool {
		b, _ := json.Marshal(s.snapshot())
		if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	if !send() {
		return
	}
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.events:
			if !send() {
				return
			}
		case <-ticker.C:
			if !send() {
				return
			}
		}
	}
}

func tgSharedLimit() int {
	n := downloadLimit()
	if n < 2 {
		return 2
	}
	return n
}

func (s *Server) ensureTGServe() {
	s.tgServeOnce.Do(func() {
		s.tgShared = make(chan struct{}, tgSharedLimit())
		s.tgStream = make(chan struct{}, 1)
	})
}

// tryAcquireTGShared non-blocking; thumb/preview fail-fast when saturated.
func (s *Server) tryAcquireTGShared() bool {
	s.ensureTGServe()
	select {
	case s.tgShared <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *Server) releaseTGShared() {
	select {
	case <-s.tgShared:
	default:
	}
}

// acquireTGStream prefers the reserved stream slot, then falls back to shared.
func (s *Server) acquireTGStream(ctx context.Context) (func(), error) {
	s.ensureTGServe()
	select {
	case s.tgStream <- struct{}{}:
		return func() { <-s.tgStream }, nil
	default:
	}
	select {
	case s.tgShared <- struct{}{}:
		return func() { <-s.tgShared }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *Server) serveTelegramMedia(ctx context.Context, m *media, w http.ResponseWriter, r *http.Request, inline bool) {
	if m == nil {
		http.Error(w, "media unavailable", http.StatusNotFound)
		return
	}
	api := s.pool.Client(ctx, m.DC)
	if s.opts.Takeout {
		api = s.pool.Takeout(ctx, m.DC)
	}
	partSize := int64(viper.GetInt(consts.FlagPartSize))
	if partSize < 1024 || partSize > 1024*1024 || partSize%1024 != 0 {
		partSize = 512 * 1024
	}
	u := partio.NewStreamer(
		tg_io.NewDownloader(api).ChunkSource(m.Size, m.Location),
		partSize)
	if inline {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, strings.ReplaceAll(m.Name, `"`, `'`)))
	}
	http_io.NewHandler(u, m.Size).
		WithContentType(m.MIME).
		WithLog(logctx.From(ctx).Named("web-stream")).
		ServeHTTP(w, r)
}

func (s *Server) serveTelegramStream(ctx context.Context, m *media, w http.ResponseWriter, r *http.Request) {
	if m == nil {
		http.Error(w, "media unavailable", http.StatusNotFound)
		return
	}
	spec, ok := prepareStreamResponse(w, r, m.Size, m.MIME, m.Name)
	if !ok || r.Method == http.MethodHead || spec.length <= 0 {
		return
	}
	api := s.pool.Client(ctx, m.DC)
	if s.opts.Takeout {
		api = s.pool.Takeout(ctx, m.DC)
	}
	partSize := int64(viper.GetInt(consts.FlagPartSize))
	if partSize < 1024 || partSize > 1024*1024 || partSize%1024 != 0 {
		partSize = 512 * 1024
	}
	u := partio.NewStreamer(
		tg_io.NewDownloader(api).ChunkSource(m.Size, m.Location),
		partSize)
	err := u.StreamAt(r.Context(), spec.start, &limitedStreamWriter{w: w, n: spec.length})
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) {
		logctx.From(ctx).Named("web-stream").Error("Failed to stream")
	}
}

func (s *Server) thumbCachePath(id string) string {
	return filepath.Join(s.opts.CacheDir, "thumbs", id+".jpg")
}

func (s *Server) ensureThumbCache(key string, path string, build func() error) error {
	if validThumbCacheFile(path) {
		return nil
	}
	_, err, _ := s.thumbGroup.Do(key, func() (any, error) {
		if validThumbCacheFile(path) {
			return nil, nil
		}
		if build == nil {
			return nil, errors.New("empty thumb cache builder")
		}
		return nil, build()
	})
	return err
}

func sameMediaPayload(a, b *media) bool {
	if a == nil || b == nil {
		return false
	}
	return a.Name == b.Name && a.Size == b.Size && a.DC == b.DC && a.MIME == b.MIME
}

func (s *Server) cacheMediaFile(ctx context.Context, m *media, path string) error {
	if m == nil || m.Location == nil {
		return errors.New("empty media")
	}
	// Skip absurdly large payloads (corrupt video-as-thumb); 3–20MB JPEGs OK.
	if m.Size > thumbCacheMaxBytes {
		return errors.New("media too large for thumb cache")
	}
	if validThumbCacheFile(path) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), defaultCachePerm); err != nil {
		return err
	}
	tmp := path + tempExt
	_ = os.Remove(tmp)
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	api := s.pool.Client(ctx, m.DC)
	if s.opts.Takeout {
		api = s.pool.Takeout(ctx, m.DC)
	}
	_, dlErr := tgdownloader.NewDownloader().
		WithPartSize(512*1024).
		Download(api, m.Location).
		WithThreads(tutil.BestThreads(m.Size, viper.GetInt(consts.FlagThreads))).
		Parallel(ctx, f)
	closeErr := f.Close()
	if dlErr != nil {
		_ = os.Remove(tmp)
		return dlErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return closeErr
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if !validThumbCacheFile(path) {
		_ = os.Remove(path)
		return errors.New("cached thumb is not a valid JPEG")
	}
	return nil
}

func setMediaCacheHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
}

func setStreamCacheHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
}

func serveThumbUnavailable(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, "thumbnail not ready", http.StatusServiceUnavailable)
}

func serveLocalFile(w http.ResponseWriter, r *http.Request, f *os.File, name, mime string) {
	if mime != "" {
		w.Header().Set("Content-Type", mime)
	}
	stat, err := f.Stat()
	mod := time.Now()
	if err == nil {
		mod = stat.ModTime()
	}
	http.ServeContent(w, r, name, mod, f)
}

type streamSpec struct {
	start   int64
	length  int64
	partial bool
}

func (s streamSpec) contentRange(size int64) string {
	return fmt.Sprintf("bytes %d-%d/%d", s.start, s.start+s.length-1, size)
}

func boundedStreamRange(header string, size int64) (streamSpec, int, error) {
	if size < 0 {
		return streamSpec{}, http.StatusBadRequest, http_range.ErrInvalid
	}
	if size == 0 {
		return streamSpec{}, http.StatusOK, nil
	}
	ranges, err := http_range.ParseRange(header, size)
	if err != nil {
		if errors.Is(err, http_range.ErrNoOverlap) {
			return streamSpec{}, http.StatusRequestedRangeNotSatisfiable, err
		}
		return streamSpec{}, http.StatusBadRequest, err
	}
	if len(ranges) > 1 {
		return streamSpec{}, http.StatusRequestedRangeNotSatisfiable, http_range.ErrInvalid
	}

	spec := streamSpec{length: size}
	if len(ranges) == 1 {
		spec.start = ranges[0].Start
		spec.length = ranges[0].Length
		spec.partial = true
	}
	if spec.length > streamChunkMaxBytes {
		spec.length = streamChunkMaxBytes
		spec.partial = true
	}
	return spec, http.StatusOK, nil
}

func boundedTmpRange(header string, tmpSize, fullSize int64) (streamSpec, bool, int, error) {
	spec, status, err := boundedStreamRange(header, fullSize)
	if err != nil {
		return streamSpec{}, false, status, err
	}
	if spec.start >= tmpSize {
		return streamSpec{}, false, http.StatusOK, nil
	}
	if spec.start+spec.length > tmpSize {
		spec.length = tmpSize - spec.start
		spec.partial = true
	}
	return spec, spec.length > 0, http.StatusOK, nil
}

func writeRangeError(w http.ResponseWriter, status int, size int64, err error) {
	if status == http.StatusRequestedRangeNotSatisfiable {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", size))
	}
	http.Error(w, err.Error(), status)
}

func prepareStreamResponse(w http.ResponseWriter, r *http.Request, size int64, mime, name string) (streamSpec, bool) {
	spec, status, err := boundedStreamRange(r.Header.Get("Range"), size)
	if err != nil {
		writeRangeError(w, status, size, err)
		return streamSpec{}, false
	}
	if mime != "" {
		w.Header().Set("Content-Type", mime)
	}
	if name != "" {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, strings.ReplaceAll(name, `"`, `'`)))
	}
	w.Header().Set("Accept-Ranges", "bytes")
	setStreamCacheHeaders(w)
	if spec.partial {
		w.Header().Set("Content-Range", spec.contentRange(size))
	}
	w.Header().Set("Content-Length", strconv.FormatInt(spec.length, 10))
	if spec.partial {
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	return spec, true
}

func serveStreamFile(w http.ResponseWriter, r *http.Request, f *os.File, name, mime string) {
	stat, err := f.Stat()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	spec, ok := prepareStreamResponse(w, r, stat.Size(), mime, "")
	if !ok || r.Method == http.MethodHead || spec.length <= 0 {
		return
	}
	if _, err := f.Seek(spec.start, io.SeekStart); err != nil {
		return
	}
	_, _ = io.CopyN(w, f, spec.length)
}

type limitedStreamWriter struct {
	w io.Writer
	n int64
}

func (w *limitedStreamWriter) Write(p []byte) (int, error) {
	if w.n <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > w.n {
		p = p[:w.n]
	}
	n, err := w.w.Write(p)
	w.n -= int64(n)
	if err != nil {
		return n, err
	}
	if w.n <= 0 {
		return n, io.EOF
	}
	return n, nil
}
