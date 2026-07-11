package web

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidThumbCacheFile(t *testing.T) {
	dir := t.TempDir()
	okPath := filepath.Join(dir, "ok.jpg")
	// minimal JPEG SOI + enough padding
	buf := make([]byte, 200)
	buf[0], buf[1], buf[2] = 0xff, 0xd8, 0xff
	if err := os.WriteFile(okPath, buf, 0o644); err != nil {
		t.Fatal(err)
	}
	if !validThumbCacheFile(okPath) {
		t.Fatal("expected valid jpeg")
	}

	bad := filepath.Join(dir, "bad.jpg")
	if err := os.WriteFile(bad, []byte("ftypisom....notjpeg"), 0o644); err != nil {
		t.Fatal(err)
	}
	if validThumbCacheFile(bad) {
		t.Fatal("expected invalid")
	}
	if _, err := os.Stat(bad); !os.IsNotExist(err) {
		t.Fatal("bad file should be removed")
	}

	huge := filepath.Join(dir, "huge.jpg")
	f, err := os.Create(huge)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write([]byte{0xff, 0xd8, 0xff})
	if err := f.Truncate(thumbCacheMaxBytes + 1); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	_ = f.Close()
	if validThumbCacheFile(huge) {
		t.Fatal("oversized should be invalid")
	}
}
