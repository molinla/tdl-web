package web

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-faster/errors"
	"github.com/gotd/contrib/partio"
	"github.com/gotd/contrib/tg_io"
	"github.com/spf13/viper"

	"github.com/iyear/tdl/pkg/consts"
)

const (
	// Enough for a first keyframe on many progressive MP4s; not a full download.
	posterPrefixMaxBytes = 4 * 1024 * 1024
	posterPrefixChunk    = 512 * 1024
	posterMinLocalBytes  = 64 * 1024
	remotePosterTimeout  = 30 * time.Second
	remotePosterMaxBytes = 32 * 1024 * 1024
	// Streaming-marked MP4s try a 4 MiB prefix first, then spend the remaining
	// budget on a bounded head/tail fallback when the moov atom is at the end.
	remotePosterFallbackSpan = (remotePosterMaxBytes - posterPrefixMaxBytes) / 2
)

// extractVideoPoster writes the first frame of a local video to outJpg using ffmpeg.
func extractVideoPoster(videoPath, outJpg string) error {
	if st, err := os.Stat(videoPath); err != nil || st.Size() == 0 {
		return fmt.Errorf("video missing")
	}
	// Only reuse an existing cache if it is a real JPEG (not a dumped mp4).
	if validThumbCacheFile(outJpg) {
		return nil
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		return fmt.Errorf("ffmpeg not found in PATH")
	}
	if err := os.MkdirAll(filepath.Dir(outJpg), defaultCachePerm); err != nil {
		return err
	}
	// Must keep a .jpg extension: ffmpeg picks the muxer from the suffix
	// (writing to out.jpg.tmp fails with "Unable to choose an output format").
	tmp := outJpg + ".part.jpg"
	_ = os.Remove(tmp)

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	// Tolerate truncated / still-downloading files (moov may be incomplete).
	cmd := exec.CommandContext(ctx, ffmpeg,
		"-hide_banner", "-loglevel", "error",
		"-fflags", "+genpts+discardcorrupt",
		"-i", videoPath,
		"-an",
		"-frames:v", "1",
		"-q:v", "4",
		"-y",
		tmp,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("ffmpeg: %w (%s)", err, string(out))
	}
	if st, err := os.Stat(tmp); err != nil || st.Size() == 0 {
		_ = os.Remove(tmp)
		return fmt.Errorf("ffmpeg produced empty poster")
	}
	if err := os.Rename(tmp, outJpg); err != nil {
		return err
	}
	if !validThumbCacheFile(outJpg) {
		_ = os.Remove(outJpg)
		return fmt.Errorf("poster is not a valid JPEG")
	}
	return nil
}

// extractRemoteVideoPoster downloads bounded ranges from a Telegram video and
// extracts a JPEG poster. It never downloads the whole remote file.
func (s *Server) extractRemoteVideoPoster(ctx context.Context, m *media, outJpg string) error {
	if validThumbCacheFile(outJpg) {
		return nil
	}
	if !canExtractRemoteVideoPoster(m) {
		return errors.New("remote poster unsupported for media")
	}
	if m == nil || m.Location == nil {
		return errors.New("no media")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return fmt.Errorf("ffmpeg not found in PATH")
	}

	var attempts []string
	for _, attempt := range remotePosterPlan(m) {
		timeout := remotePosterAttemptTimeout(attempt)
		switch attempt.mode {
		case remotePosterModePrefix:
			if err := s.extractRemoteVideoPosterBytes(ctx, m, outJpg, attempt.bytes, timeout); err == nil {
				return nil
			} else {
				attempts = append(attempts, attempt.String()+": "+err.Error())
			}
		case remotePosterModeSparse:
			if err := s.extractRemoteSparseVideoPosterPass(ctx, m, outJpg, attempt.bytes, timeout); err == nil {
				return nil
			} else {
				attempts = append(attempts, attempt.String()+": "+err.Error())
			}
		}
	}
	if len(attempts) == 0 {
		return errors.New("remote poster unsupported for media")
	}
	return errors.New("remote poster extraction failed: " + strings.Join(attempts, "; "))
}

type remotePosterMode int

const (
	remotePosterModePrefix remotePosterMode = iota
	remotePosterModeSparse
)

type remotePosterAttempt struct {
	mode  remotePosterMode
	bytes int64
}

func (a remotePosterAttempt) String() string {
	switch a.mode {
	case remotePosterModePrefix:
		return fmt.Sprintf("prefix-%dMiB", a.bytes/(1024*1024))
	case remotePosterModeSparse:
		return fmt.Sprintf("sparse-%dMiB", a.bytes*2/(1024*1024))
	default:
		return "unknown"
	}
}

func remotePosterPlan(m *media) []remotePosterAttempt {
	if m == nil || m.Location == nil || m.Size <= 0 {
		return nil
	}
	ext := strings.ToLower(filepath.Ext(m.Name))
	switch ext {
	case ".mp4", ".m4v", ".3gp", ".3gpp":
		return mp4PosterPlan(m)
	case ".mov":
		return []remotePosterAttempt{{mode: remotePosterModeSparse, bytes: 16 * 1024 * 1024}}
	case ".mpg", ".mpeg", ".vob", ".ts", ".mts", ".m2ts":
		return []remotePosterAttempt{{mode: remotePosterModePrefix, bytes: 16 * 1024 * 1024}}
	case ".avi":
		return []remotePosterAttempt{{mode: remotePosterModePrefix, bytes: remotePosterMaxBytes}}
	case ".mkv", ".webm", ".flv", ".wmv", ".asf":
		return []remotePosterAttempt{{mode: remotePosterModePrefix, bytes: 16 * 1024 * 1024}}
	}

	switch strings.ToLower(strings.TrimSpace(m.MIME)) {
	case "video/mp4", "video/3gpp", "video/3gp":
		return mp4PosterPlan(m)
	case "video/quicktime":
		return []remotePosterAttempt{{mode: remotePosterModeSparse, bytes: 16 * 1024 * 1024}}
	case "video/mpeg", "video/mp2t":
		return []remotePosterAttempt{{mode: remotePosterModePrefix, bytes: 16 * 1024 * 1024}}
	case "video/avi", "video/vnd.avi", "video/x-msvideo", "video/msvideo":
		return []remotePosterAttempt{{mode: remotePosterModePrefix, bytes: remotePosterMaxBytes}}
	case "video/x-matroska", "video/webm", "video/x-flv", "video/x-ms-wmv", "video/x-ms-asf":
		return []remotePosterAttempt{{mode: remotePosterModePrefix, bytes: 16 * 1024 * 1024}}
	}
	return nil
}

func mp4PosterPlan(m *media) []remotePosterAttempt {
	if m.SupportsStreaming || m.PreloadPrefixSize > 0 {
		return []remotePosterAttempt{
			{mode: remotePosterModePrefix, bytes: posterPrefixMaxBytes},
			{mode: remotePosterModeSparse, bytes: remotePosterFallbackSpan},
		}
	}
	return []remotePosterAttempt{{mode: remotePosterModeSparse, bytes: 16 * 1024 * 1024}}
}

func remotePosterAttemptTimeout(attempt remotePosterAttempt) time.Duration {
	bytes := attempt.bytes
	if attempt.mode == remotePosterModeSparse {
		bytes *= 2
	}
	switch {
	case bytes > 16*1024*1024:
		return 90 * time.Second
	case bytes > 8*1024*1024:
		return 60 * time.Second
	default:
		return remotePosterTimeout
	}
}

func (s *Server) extractRemoteVideoPosterBytes(ctx context.Context, m *media, outJpg string, maxBytes int64, timeout time.Duration) error {
	need := boundedRemotePosterBytes(m.Size, maxBytes)
	if need < posterMinLocalBytes {
		return errors.New("media too small for poster prefix")
	}

	dir := filepath.Join(s.opts.CacheDir, "poster-prefix")
	if err := os.MkdirAll(dir, defaultCachePerm); err != nil {
		return err
	}
	prefixPath := filepath.Join(dir, filepath.Base(outJpg)+remotePosterTempExt(m)+tempExt)
	_ = os.Remove(prefixPath)
	defer func() { _ = os.Remove(prefixPath) }()

	dlCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if err := s.downloadMediaPrefix(dlCtx, m, prefixPath, need); err != nil {
		return err
	}
	return extractVideoPoster(prefixPath, outJpg)
}

func remotePosterTempExt(m *media) string {
	if m != nil {
		if ext := strings.ToLower(filepath.Ext(m.Name)); ext != "" {
			return ext
		}
		switch strings.ToLower(m.MIME) {
		case "video/quicktime":
			return ".mov"
		case "video/3gpp", "video/3gp":
			return ".3gp"
		case "video/mpeg":
			return ".mpg"
		case "video/mp2t":
			return ".ts"
		case "video/avi", "video/vnd.avi", "video/x-msvideo", "video/msvideo":
			return ".avi"
		case "video/x-matroska":
			return ".mkv"
		case "video/webm":
			return ".webm"
		case "video/x-flv":
			return ".flv"
		case "video/x-ms-wmv", "video/x-ms-asf":
			return ".wmv"
		}
	}
	return ".mp4"
}

func canExtractRemoteVideoPoster(m *media) bool {
	return len(remotePosterPlan(m)) > 0
}

func boundedRemotePosterBytes(size, requested int64) int64 {
	if size <= 0 || requested <= 0 {
		return 0
	}
	if requested > remotePosterMaxBytes {
		requested = remotePosterMaxBytes
	}
	if requested >= size {
		requested = size / 2
	}
	return requested
}

func (s *Server) extractRemoteSparseVideoPosterPass(ctx context.Context, m *media, outJpg string, span int64, timeout time.Duration) error {
	headLen, tailOffset, tailLen := sparsePosterRanges(m.Size, span)
	if headLen < posterMinLocalBytes || tailLen < posterMinLocalBytes {
		return errors.New("sparse ranges too small")
	}

	dir := filepath.Join(s.opts.CacheDir, "poster-prefix")
	if err := os.MkdirAll(dir, defaultCachePerm); err != nil {
		return err
	}
	sparsePath := filepath.Join(dir, filepath.Base(outJpg)+".sparse"+remotePosterTempExt(m)+tempExt)
	_ = os.Remove(sparsePath)
	defer func() { _ = os.Remove(sparsePath) }()

	f, err := os.OpenFile(sparsePath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if err := f.Truncate(m.Size); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	dlCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := s.downloadMediaRange(dlCtx, m, sparsePath, 0, headLen); err != nil {
		return errors.Wrap(err, "download head")
	}
	if err := s.downloadMediaRange(dlCtx, m, sparsePath, tailOffset, tailLen); err != nil {
		return errors.Wrap(err, "download tail")
	}
	return extractVideoPoster(sparsePath, outJpg)
}

func sparsePosterRanges(size, span int64) (headLen, tailOffset, tailLen int64) {
	if size <= 0 || span <= 0 {
		return 0, 0, 0
	}
	if span > remotePosterMaxBytes/2 {
		span = remotePosterMaxBytes / 2
	}
	if span*2 >= size {
		span = size / 4
	}
	return span, size - span, span
}

func (s *Server) downloadMediaPrefix(ctx context.Context, m *media, dest string, maxBytes int64) error {
	if m == nil || m.Location == nil {
		return errors.New("empty media")
	}
	if maxBytes <= 0 {
		return errors.New("invalid prefix size")
	}
	api := s.pool.Client(ctx, m.DC)
	if s.opts.Takeout {
		api = s.pool.Takeout(ctx, m.DC)
	}

	chunk := int64(posterPrefixChunk)
	if ps := int64(viper.GetInt(consts.FlagPartSize)); ps >= 1024 && ps <= 1024*1024 {
		chunk = ps
	}

	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()

	src := tg_io.NewDownloader(api).ChunkSource(m.Size, m.Location)
	streamer := partio.NewStreamer(src, chunk)
	lw := &prefixLimitWriter{w: f, n: maxBytes}
	err = streamer.Stream(ctx, lw)
	if err != nil && !errors.Is(err, errPrefixDone) && !errors.Is(err, io.ErrShortWrite) {
		_ = os.Remove(dest)
		return err
	}
	st, stErr := f.Stat()
	if stErr != nil || st.Size() < posterMinLocalBytes {
		_ = os.Remove(dest)
		if stErr != nil {
			return stErr
		}
		return errors.New("downloaded prefix too small")
	}
	return nil
}

func (s *Server) downloadMediaRange(ctx context.Context, m *media, dest string, offset, length int64) error {
	if m == nil || m.Location == nil {
		return errors.New("empty media")
	}
	if offset < 0 || length <= 0 || offset >= m.Size {
		return errors.New("invalid media range")
	}
	if offset+length > m.Size {
		length = m.Size - offset
	}
	api := s.pool.Client(ctx, m.DC)
	if s.opts.Takeout {
		api = s.pool.Takeout(ctx, m.DC)
	}
	chunk := int64(posterPrefixChunk)
	if ps := int64(viper.GetInt(consts.FlagPartSize)); ps >= 1024 && ps <= 1024*1024 {
		chunk = ps
	}
	f, err := os.OpenFile(dest, os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	requestOffset := alignDown(offset, 1024)
	skip := offset - requestOffset
	src := tg_io.NewDownloader(api).ChunkSource(m.Size, m.Location)
	streamer := partio.NewStreamer(src, chunk)
	w := &rangeFileWriter{
		f:      f,
		pos:    offset,
		skip:   skip,
		remain: length,
	}
	err = streamer.StreamAt(ctx, requestOffset, w)
	if err != nil && !errors.Is(err, errPrefixDone) && !errors.Is(err, io.ErrShortWrite) {
		return err
	}
	if w.remain > 0 {
		return errors.New("downloaded range too small")
	}
	return nil
}

var errPrefixDone = errors.New("poster prefix complete")

type prefixLimitWriter struct {
	w io.Writer
	n int64
}

func (p *prefixLimitWriter) Write(b []byte) (int, error) {
	if p.n <= 0 {
		return 0, errPrefixDone
	}
	if int64(len(b)) > p.n {
		b = b[:p.n]
	}
	n, err := p.w.Write(b)
	p.n -= int64(n)
	if err != nil {
		return n, err
	}
	if p.n <= 0 {
		return n, errPrefixDone
	}
	return n, nil
}

type rangeFileWriter struct {
	f      *os.File
	pos    int64
	skip   int64
	remain int64
}

func (w *rangeFileWriter) Write(b []byte) (int, error) {
	original := len(b)
	consumed := 0
	if w.remain <= 0 {
		return 0, errPrefixDone
	}
	if w.skip > 0 {
		if int64(len(b)) <= w.skip {
			w.skip -= int64(len(b))
			return original, nil
		}
		consumed = int(w.skip)
		b = b[w.skip:]
		w.skip = 0
	}
	if int64(len(b)) > w.remain {
		b = b[:w.remain]
	}
	n, err := w.f.WriteAt(b, w.pos)
	w.pos += int64(n)
	w.remain -= int64(n)
	if err != nil {
		return consumed + n, err
	}
	if w.remain <= 0 {
		return consumed + n, errPrefixDone
	}
	return original, nil
}
