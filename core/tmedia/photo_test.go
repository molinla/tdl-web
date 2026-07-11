package tmedia

import (
	"testing"

	"github.com/gotd/td/tg"
)

func TestGetPhotoSizePrefersRemoteOverCached(t *testing.T) {
	typ, size, ok := GetPhotoSize([]tg.PhotoSizeClass{
		&tg.PhotoStrippedSize{Type: "i", Bytes: []byte{1, 2, 3}},
		&tg.PhotoCachedSize{Type: "m", W: 100, H: 100, Bytes: make([]byte, 50)},
		&tg.PhotoSize{Type: "x", W: 800, H: 600, Size: 120_000},
	})
	if !ok {
		t.Fatal("expected ok")
	}
	if typ != "x" || size != 120_000 {
		t.Fatalf("got type=%s size=%d, want x/120000", typ, size)
	}
}

func TestGetPhotoSizeFallsBackToCached(t *testing.T) {
	typ, size, ok := GetPhotoSize([]tg.PhotoSizeClass{
		&tg.PhotoCachedSize{Type: "m", W: 100, H: 100, Bytes: make([]byte, 42)},
	})
	if !ok {
		t.Fatal("expected ok")
	}
	if typ != "m" || size != 42 {
		t.Fatalf("got type=%s size=%d, want m/42", typ, size)
	}
}

func TestMediaUnavailableReasonDocumentEmpty(t *testing.T) {
	msg := &tg.Message{
		Media: &tg.MessageMediaDocument{
			Document: &tg.DocumentEmpty{ID: 1},
		},
	}
	got := MediaUnavailableReason(msg)
	if got == "" || got == "message is not media" {
		t.Fatalf("unexpected reason: %q", got)
	}
}
