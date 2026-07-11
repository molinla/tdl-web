package web

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

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
