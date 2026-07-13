package web

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gotd/td/tg"
)

func TestRemotePosterPlanSelectsExpectedStrategyOrder(t *testing.T) {
	loc := &tg.InputDocumentFileLocation{ID: 1, AccessHash: 2, FileReference: []byte{1}}
	tests := []struct {
		name string
		m    *media
		want []remotePosterAttempt
	}{
		{
			name: "streaming mp4 falls back within total budget",
			m:    &media{Location: loc, Name: "video.mp4", MIME: "video/mp4", Size: 190 * 1024 * 1024, SupportsStreaming: true},
			want: []remotePosterAttempt{
				{mode: remotePosterModePrefix, bytes: posterPrefixMaxBytes},
				{mode: remotePosterModeSparse, bytes: remotePosterFallbackSpan},
			},
		},
		{
			name: "non-streaming mp4 uses bounded head and tail",
			m:    &media{Location: loc, Name: "video.mp4", MIME: "video/mp4", Size: 190 * 1024 * 1024},
			want: []remotePosterAttempt{{mode: remotePosterModeSparse, bytes: 16 * 1024 * 1024}},
		},
		{
			name: "mov extension wins over mp4 mime",
			m:    &media{Location: loc, Name: "video.mov", MIME: "video/mp4", Size: 190 * 1024 * 1024},
			want: []remotePosterAttempt{{mode: remotePosterModeSparse, bytes: 16 * 1024 * 1024}},
		},
		{
			name: "mpeg uses prefix",
			m:    &media{Location: loc, Name: "video.mpg", MIME: "video/mpeg", Size: 190 * 1024 * 1024},
			want: []remotePosterAttempt{{mode: remotePosterModePrefix, bytes: 16 * 1024 * 1024}},
		},
		{
			name: "avi uses largest bounded prefix",
			m:    &media{Location: loc, Name: "video.avi", MIME: "video/vnd.avi", Size: 190 * 1024 * 1024},
			want: []remotePosterAttempt{{mode: remotePosterModePrefix, bytes: remotePosterMaxBytes}},
		},
		{
			name: "matroska uses prefix",
			m:    &media{Location: loc, Name: "video.mkv", MIME: "video/x-matroska", Size: 190 * 1024 * 1024},
			want: []remotePosterAttempt{{mode: remotePosterModePrefix, bytes: 16 * 1024 * 1024}},
		},
		{
			name: "mime fallback works without extension",
			m:    &media{Location: loc, Name: "video", MIME: "video/mpeg", Size: 190 * 1024 * 1024},
			want: []remotePosterAttempt{{mode: remotePosterModePrefix, bytes: 16 * 1024 * 1024}},
		},
		{
			name: "unknown video stays unsupported",
			m:    &media{Location: loc, Name: "video.bin", MIME: "video/unknown", Size: 190 * 1024 * 1024},
			want: nil,
		},
		{
			name: "unknown file stays unsupported",
			m:    &media{Location: loc, Name: "large.bin", MIME: "application/octet-stream", Size: 190 * 1024 * 1024},
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := remotePosterPlan(tt.m); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("plan=%v want %v", got, tt.want)
			}
		})
	}
}

func TestRemotePosterReadsStayBounded(t *testing.T) {
	if total := posterPrefixMaxBytes + remotePosterFallbackSpan*2; total != remotePosterMaxBytes {
		t.Fatalf("streaming fallback total=%d want %d", total, remotePosterMaxBytes)
	}
	if got := boundedRemotePosterBytes(10*1024*1024, remotePosterMaxBytes); got != 5*1024*1024 {
		t.Fatalf("small prefix=%d want half file", got)
	}
	if got := boundedRemotePosterBytes(100*1024*1024, 64*1024*1024); got != remotePosterMaxBytes {
		t.Fatalf("large prefix=%d want max %d", got, remotePosterMaxBytes)
	}
}

func TestSparsePosterRanges(t *testing.T) {
	head, tailOffset, tail := sparsePosterRanges(200*1024*1024, 16*1024*1024)
	if head != 16*1024*1024 || tail != 16*1024*1024 || tailOffset != 184*1024*1024 {
		t.Fatalf("large ranges head=%d tailOffset=%d tail=%d", head, tailOffset, tail)
	}
	head, tailOffset, tail = sparsePosterRanges(20*1024*1024, 16*1024*1024)
	if head != 5*1024*1024 || tail != 5*1024*1024 || tailOffset != 15*1024*1024 {
		t.Fatalf("small ranges head=%d tailOffset=%d tail=%d", head, tailOffset, tail)
	}
	if got := alignDown(12345, 1024); got != 12288 {
		t.Fatalf("alignDown=%d want 12288", got)
	}
}

func TestRangeFileWriterWritesSparseOffsets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sparse.bin")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Truncate(32); err != nil {
		t.Fatal(err)
	}
	w := &rangeFileWriter{f: f, pos: 10, skip: 2, remain: 4}
	n, err := w.Write([]byte("xxabcdyy"))
	if n != 6 || !errors.Is(err, errPrefixDone) {
		t.Fatalf("write n=%d err=%v", n, err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data[10:14]); got != "abcd" {
		t.Fatalf("range data=%q want abcd", got)
	}
	if len(data) != 32 {
		t.Fatalf("len=%d want 32", len(data))
	}
}

func TestExtractVideoPosterMissingFFmpegOrFile(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "poster.jpg")
	err := extractVideoPoster(filepath.Join(dir, "missing.mp4"), out)
	if err == nil {
		t.Fatal("expected error for missing video")
	}
}

func TestThumbCachePath(t *testing.T) {
	s := &Server{opts: Options{CacheDir: t.TempDir()}}
	p := s.thumbCachePath("abc")
	if filepath.Base(p) != "abc.jpg" {
		t.Fatalf("got %s", p)
	}
	if _, err := os.Stat(filepath.Dir(p)); err == nil {
		t.Fatal("dir should not exist yet")
	}
}

func TestPrefixLimitWriter(t *testing.T) {
	var buf bytes.Buffer
	w := &prefixLimitWriter{w: &buf, n: 10}
	n, err := w.Write([]byte("hello"))
	if n != 5 || err != nil {
		t.Fatalf("first write: n=%d err=%v", n, err)
	}
	n, err = w.Write([]byte("world!!!"))
	if n != 5 || !errors.Is(err, errPrefixDone) {
		t.Fatalf("second write: n=%d err=%v", n, err)
	}
	if buf.String() != "helloworld" {
		t.Fatalf("got %q", buf.String())
	}
}
