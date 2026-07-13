package tmedia

import (
	"testing"

	"github.com/gotd/td/tg"
)

func TestGetDocumentStrippedThumbExpandsInlineJPEG(t *testing.T) {
	stripped := []byte{1, 8, 8, 1, 2, 3, 4}
	doc := &tg.Document{
		ID:         1,
		AccessHash: 2,
	}
	doc.SetThumbs([]tg.PhotoSizeClass{
		&tg.PhotoStrippedSize{Type: "i", Bytes: stripped},
	})

	got, ok := GetDocumentStrippedThumb(doc)
	if !ok {
		t.Fatal("expected ok")
	}
	if len(got) <= len(stripped) || got[0] != 0xff || got[1] != 0xd8 || got[2] != 0xff {
		t.Fatalf("got invalid expanded JPEG len=%d header=% x", len(got), got[:3])
	}
}

func TestGetDocumentCoverPrefersDownloadableThumbOverStripped(t *testing.T) {
	doc := &tg.Document{ID: 1, AccessHash: 2, DCID: 2, Date: 1, FileReference: []byte{1}}
	doc.SetThumbs([]tg.PhotoSizeClass{
		&tg.PhotoStrippedSize{Type: "i", Bytes: []byte{1, 8, 8, 1}},
		&tg.PhotoSize{Type: "m", W: 320, H: 180, Size: 9000},
	})
	media := &tg.MessageMediaDocument{Document: doc}

	cover, ok := GetDocumentCover(media)
	if !ok {
		t.Fatal("expected cover")
	}
	loc, ok := cover.InputFileLoc.(*tg.InputDocumentFileLocation)
	if !ok || loc.ThumbSize != "m" || len(cover.Inline) != 0 {
		t.Fatalf("unexpected cover loc=%T inline=%d", cover.InputFileLoc, len(cover.Inline))
	}
}
