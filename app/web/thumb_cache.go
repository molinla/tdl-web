package web

import (
	"errors"
	"os"
	"path/filepath"
)

const (
	thumbCacheMinBytes = 100
	thumbCacheMaxBytes = 20 * 1024 * 1024 // reject corrupt video dumps; allow large real JPEGs
)

// validThumbCacheFile reports whether path is a plausible JPEG thumb cache.
// Invalid / oversized files are removed so callers can rebuild.
func validThumbCacheFile(path string) bool {
	st, err := os.Stat(path)
	if err != nil || st.Size() < thumbCacheMinBytes || st.Size() > thumbCacheMaxBytes {
		if err == nil {
			_ = os.Remove(path)
		}
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var hdr [3]byte
	n, err := f.Read(hdr[:])
	if err != nil || n < 3 || hdr[0] != 0xff || hdr[1] != 0xd8 || hdr[2] != 0xff {
		_ = os.Remove(path)
		return false
	}
	return true
}

// writeInlineThumb persists embedded JPEG bytes (e.g. PhotoStrippedSize) to cache.
func writeInlineThumb(data []byte, path string) error {
	if len(data) < 3 || data[0] != 0xff || data[1] != 0xd8 || data[2] != 0xff {
		return errors.New("inline thumb is not JPEG")
	}
	if validThumbCacheFile(path) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), defaultCachePerm); err != nil {
		return err
	}
	tmp := path + tempExt
	_ = os.Remove(tmp)
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if !validThumbCacheFile(path) {
		_ = os.Remove(path)
		return errors.New("inline thumb cache invalid")
	}
	return nil
}
