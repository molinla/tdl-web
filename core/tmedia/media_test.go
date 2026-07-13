package tmedia

import (
	"testing"

	"github.com/gotd/td/tg"
)

func TestGetDocumentThumbPrefersLargestSize(t *testing.T) {
	doc := &tg.Document{
		ID:            1,
		AccessHash:    2,
		FileReference: []byte{1},
		DCID:          2,
		Date:          100,
	}
	doc.SetThumbs([]tg.PhotoSizeClass{
		&tg.PhotoStrippedSize{Type: "i", Bytes: []byte{1, 2, 3}},
		&tg.PhotoSize{Type: "s", W: 90, H: 90, Size: 2_000},
		&tg.PhotoSize{Type: "m", W: 320, H: 320, Size: 18_000},
	})

	thumb, ok := GetDocumentThumb(doc)
	if !ok {
		t.Fatal("expected ok")
	}
	loc, ok := thumb.InputFileLoc.(*tg.InputDocumentFileLocation)
	if !ok {
		t.Fatalf("unexpected location type %T", thumb.InputFileLoc)
	}
	if loc.ThumbSize != "m" || thumb.Size != 18_000 || thumb.Width != 320 || thumb.Height != 320 {
		t.Fatalf("got type=%s size=%d width=%d height=%d, want m/18000/320/320", loc.ThumbSize, thumb.Size, thumb.Width, thumb.Height)
	}
}

func TestGetDocumentCoverPrefersVideoThumb(t *testing.T) {
	doc := &tg.Document{
		ID:            1,
		AccessHash:    2,
		FileReference: []byte{1},
		DCID:          2,
		Date:          100,
	}
	doc.SetThumbs([]tg.PhotoSizeClass{
		&tg.PhotoSize{Type: "m", W: 320, H: 180, Size: 18_000},
	})
	doc.SetVideoThumbs([]tg.VideoSizeClass{
		&tg.VideoSize{Type: "v", W: 640, H: 360, Size: 42_000},
	})

	cover, ok := GetDocumentCover(&tg.MessageMediaDocument{Document: doc})
	if !ok {
		t.Fatal("expected ok")
	}
	loc, ok := cover.InputFileLoc.(*tg.InputDocumentFileLocation)
	if !ok {
		t.Fatalf("unexpected location type %T", cover.InputFileLoc)
	}
	if loc.ThumbSize != "v" || cover.Size != 42_000 || cover.Width != 640 || cover.Height != 360 {
		t.Fatalf("got type=%s size=%d width=%d height=%d, want v/42000/640/360", loc.ThumbSize, cover.Size, cover.Width, cover.Height)
	}
}

func TestGetDocumentInfoReadsDimensions(t *testing.T) {
	media, ok := GetDocumentInfo(&tg.MessageMediaDocument{
		Document: &tg.Document{
			ID:         1,
			AccessHash: 2,
			Attributes: []tg.DocumentAttributeClass{
				&tg.DocumentAttributeVideo{W: 1920, H: 1080},
			},
		},
	})
	if !ok {
		t.Fatal("expected ok")
	}
	if media.Width != 1920 || media.Height != 1080 {
		t.Fatalf("got width=%d height=%d, want 1920/1080", media.Width, media.Height)
	}
}
