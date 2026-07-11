package web

import (
	"os"
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
