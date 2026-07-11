package web

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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

// extractRemoteVideoPoster downloads only the start of a Telegram video and
// extracts a JPEG poster. Used when there is no document thumb and no local file.
func (s *Server) extractRemoteVideoPoster(ctx context.Context, m *media, outJpg string) error {
	if validThumbCacheFile(outJpg) {
		return nil
	}
	if m == nil || m.Location == nil {
		return errors.New("no media")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return fmt.Errorf("ffmpeg not found in PATH")
	}

	need := int64(posterPrefixMaxBytes)
	if m.Size > 0 && m.Size < need {
		need = m.Size
	}
	if need < posterMinLocalBytes {
		return errors.New("media too small for poster prefix")
	}

	dir := filepath.Join(s.opts.CacheDir, "poster-prefix")
	if err := os.MkdirAll(dir, defaultCachePerm); err != nil {
		return err
	}
	prefixPath := filepath.Join(dir, filepath.Base(outJpg)+".mp4"+tempExt)
	_ = os.Remove(prefixPath)
	defer func() { _ = os.Remove(prefixPath) }()

	dlCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	if err := s.downloadMediaPrefix(dlCtx, m, prefixPath, need); err != nil {
		return err
	}
	return extractVideoPoster(prefixPath, outJpg)
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
