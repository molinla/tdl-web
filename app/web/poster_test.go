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

func TestCanExtractRemoteVideoPosterAllowsSmallVideo(t *testing.T) {
	m := &media{
		Location: &tg.InputDocumentFileLocation{ID: 1, AccessHash: 2, FileReference: []byte{1}},
		Name:     "small.mp4",
		MIME:     "video/mp4",
		Size:     39 * 1024 * 1024,
	}
	if !canExtractRemoteVideoPoster(m) {
		t.Fatal("small video should allow full temporary poster extraction")
	}
	if !canExtractRemoteFullVideoPoster(m) {
		t.Fatal("small video should use full temporary extraction")
	}
}

func TestCanExtractRemoteVideoPosterAllowsLargeNonStreamingVideoViaSparse(t *testing.T) {
	m := &media{
		Location: &tg.InputDocumentFileLocation{ID: 1, AccessHash: 2, FileReference: []byte{1}},
		Name:     "large.mp4",
		MIME:     "video/mp4",
		Size:     190 * 1024 * 1024,
	}
	if !canExtractRemoteVideoPoster(m) {
		t.Fatal("large non-streaming mp4 should allow sparse poster extraction")
	}
	if !canExtractRemoteSparseVideoPoster(m) {
		t.Fatal("large non-streaming mp4 should use sparse extraction")
	}
}

func TestCanExtractRemoteVideoPosterAllowsStreamingPrefix(t *testing.T) {
	m := &media{
		Location:          &tg.InputDocumentFileLocation{ID: 1, AccessHash: 2, FileReference: []byte{1}},
		Name:              "large.mp4",
		MIME:              "video/mp4",
		Size:              190 * 1024 * 1024,
		SupportsStreaming: true,
	}
	if !canExtractRemoteVideoPoster(m) {
		t.Fatal("streaming mp4 should allow prefix poster extraction")
	}
	if canExtractRemoteFullVideoPoster(m) {
		t.Fatal("large streaming mp4 should not use full temporary extraction")
	}
}

func TestCanExtractRemoteVideoPosterAllowsLargeMOVViaSparse(t *testing.T) {
	m := &media{
		Location:          &tg.InputDocumentFileLocation{ID: 1, AccessHash: 2, FileReference: []byte{1}},
		Name:              "large.mov",
		MIME:              "video/quicktime",
		Size:              190 * 1024 * 1024,
		SupportsStreaming: true,
	}
	if !canExtractRemoteVideoPoster(m) {
		t.Fatal("large MOV should allow sparse poster extraction")
	}
	if canExtractRemotePrefixVideoPoster(m) {
		t.Fatal("large MOV should not use prefix poster extraction")
	}
	if !canExtractRemoteSparseVideoPoster(m) {
		t.Fatal("large MOV should use sparse poster extraction")
	}
}

func TestRemotePosterPlanSelectsExpectedStrategyOrder(t *testing.T) {
	loc := &tg.InputDocumentFileLocation{ID: 1, AccessHash: 2, FileReference: []byte{1}}
	tests := []struct {
		name string
		m    *media
		want []remotePosterMode
	}{
		{
			name: "small video uses full temp download",
			m:    &media{Location: loc, Name: "small.mp4", MIME: "video/mp4", Size: 39 * 1024 * 1024},
			want: []remotePosterMode{remotePosterModeFull},
		},
		{
			name: "large streaming mp4 tries prefix then sparse",
			m:    &media{Location: loc, Name: "large.mp4", MIME: "video/mp4", Size: 190 * 1024 * 1024, SupportsStreaming: true},
			want: []remotePosterMode{remotePosterModePrefix, remotePosterModeSparse},
		},
		{
			name: "large mov uses sparse",
			m:    &media{Location: loc, Name: "large.mov", MIME: "video/quicktime", Size: 190 * 1024 * 1024},
			want: []remotePosterMode{remotePosterModeSparse},
		},
		{
			name: "large unknown file unsupported",
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

func TestSparsePosterRanges(t *testing.T) {
	head, tailOffset, tail, full := sparsePosterRanges(200*1024*1024, 16*1024*1024)
	if full || head != 16*1024*1024 || tail != 16*1024*1024 || tailOffset != 184*1024*1024 {
		t.Fatalf("large ranges head=%d tailOffset=%d tail=%d full=%v", head, tailOffset, tail, full)
	}
	head, tailOffset, tail, full = sparsePosterRanges(20*1024*1024, 16*1024*1024)
	if !full || head != 20*1024*1024 || tailOffset != 0 || tail != 0 {
		t.Fatalf("small ranges head=%d tailOffset=%d tail=%d full=%v", head, tailOffset, tail, full)
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
