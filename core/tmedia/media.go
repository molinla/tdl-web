package tmedia

import (
	"github.com/gotd/td/tg"
)

type Media struct {
	InputFileLoc tg.InputFileLocationClass // mtproto file location of the media file
	Name         string                    // file name
	Size         int64                     // size in bytes
	Width        int
	Height       int
	DC           int   // which DC the media is stored
	Date         int64 // media creation(upload) timestamp
	// Inline contains already-expanded JPEG bytes for Telegram stripped thumbs.
	Inline []byte
	// Video streaming hints from DocumentAttributeVideo.
	SupportsStreaming bool
	PreloadPrefixSize int
}

func ExtractMedia(m tg.MessageMediaClass) (*Media, bool) {
	switch m := m.(type) {
	case *tg.MessageMediaPhoto:
		return GetPhotoInfo(m)
	case *tg.MessageMediaDocument:
		return GetDocumentInfo(m)
	case *tg.MessageMediaInvoice:
		return GetExtendedMedia(m.ExtendedMedia)
	case *tg.MessageMediaPaidMedia:
		for _, em := range m.ExtendedMedia {
			if md, ok := GetExtendedMedia(em); ok {
				return md, true
			}
		}
	}
	return nil, false
}

func GetMedia(msg tg.MessageClass) (*Media, bool) {
	mm, ok := msg.(*tg.Message)
	if !ok {
		return nil, false
	}

	media, ok := mm.GetMedia()
	if !ok {
		return nil, false
	}

	return ExtractMedia(media)
}

// MediaUnavailableReason explains why GetMedia failed for a *tg.Message.
func MediaUnavailableReason(msg *tg.Message) string {
	if msg == nil {
		return "message is nil"
	}
	media, ok := msg.GetMedia()
	if !ok || media == nil {
		return "message has no media on Telegram (deleted or never attached)"
	}
	switch m := media.(type) {
	case *tg.MessageMediaDocument:
		if _, ok := m.Document.(*tg.Document); !ok {
			return "document unavailable on Telegram (deleted or expired)"
		}
		return "document media could not be parsed"
	case *tg.MessageMediaPhoto:
		p, ok := m.Photo.(*tg.Photo)
		if !ok {
			return "photo unavailable on Telegram (deleted or expired)"
		}
		if _, _, _, _, ok := GetPhotoSize(p.Sizes); !ok {
			return "photo has no downloadable size"
		}
		return "photo media could not be parsed"
	case *tg.MessageMediaPaidMedia:
		return "paid media is locked or has no downloadable content"
	case *tg.MessageMediaEmpty:
		return "message media is empty"
	case *tg.MessageMediaUnsupported:
		return "unsupported media type on Telegram"
	default:
		return "unsupported media type: " + media.TypeName()
	}
}

func GetExtendedMedia(mm tg.MessageExtendedMediaClass) (*Media, bool) {
	m, ok := mm.(*tg.MessageExtendedMedia)
	if !ok {
		return nil, false
	}
	return ExtractMedia(m.Media)
}

func GetDocumentThumb(doc *tg.Document) (*Media, bool) {
	thumbs, exists := doc.GetThumbs()
	if !exists {
		return nil, false
	}

	tp, size, width, height, ok := GetPhotoSize(thumbs)
	if !ok {
		return nil, false
	}

	return &Media{
		InputFileLoc: &tg.InputDocumentFileLocation{
			ID:            doc.ID,
			AccessHash:    doc.AccessHash,
			FileReference: doc.FileReference,
			ThumbSize:     tp,
		},
		Name:   "thumb.jpg",
		Size:   int64(size),
		Width:  width,
		Height: height,
		DC:     doc.DCID,
		Date:   int64(doc.Date),
	}, true
}

func GetDocumentCover(media *tg.MessageMediaDocument) (*Media, bool) {
	doc, ok := media.Document.(*tg.Document)
	if !ok {
		return nil, false
	}
	if cover, ok := media.GetVideoCover(); ok {
		if photo, ok := cover.(*tg.Photo); ok {
			if result, ok := GetPhotoMedia(photo); ok {
				return result, true
			}
		}
	}
	if thumb, ok := GetDocumentVideoThumb(doc); ok {
		return thumb, true
	}
	if thumb, ok := GetDocumentThumb(doc); ok {
		return thumb, true
	}
	if inline, ok := GetDocumentStrippedThumb(doc); ok {
		return &Media{
			Name:   "thumb.jpg",
			Size:   int64(len(inline)),
			Inline: inline,
		}, true
	}
	return nil, false
}

func GetDocumentVideoThumb(doc *tg.Document) (*Media, bool) {
	thumbs, exists := doc.GetVideoThumbs()
	if !exists {
		return nil, false
	}
	tp, size, width, height, ok := GetVideoSize(thumbs)
	if !ok {
		return nil, false
	}
	return &Media{
		InputFileLoc: &tg.InputDocumentFileLocation{
			ID:            doc.ID,
			AccessHash:    doc.AccessHash,
			FileReference: doc.FileReference,
			ThumbSize:     tp,
		},
		Name:   "thumb.jpg",
		Size:   int64(size),
		Width:  width,
		Height: height,
		DC:     doc.DCID,
		Date:   int64(doc.Date),
	}, true
}

func GetVideoSize(sizes []tg.VideoSizeClass) (string, int, int, int, bool) {
	var (
		bestType string
		bestSize int
		bestW    int
		bestH    int
		found    bool
	)
	for _, size := range sizes {
		switch s := size.(type) {
		case *tg.VideoSize:
			score := s.Size
			if score == 0 {
				score = s.W * s.H
			}
			if !found || score >= bestSize {
				bestType, bestSize, bestW, bestH, found = s.Type, score, s.W, s.H, true
			}
		}
	}
	return bestType, bestSize, bestW, bestH, found
}

// GetDocumentStrippedThumb returns the expanded inline JPEG blur preview
// embedded in a document when no downloadable thumb is available.
func GetDocumentStrippedThumb(doc *tg.Document) ([]byte, bool) {
	thumbs, exists := doc.GetThumbs()
	if !exists {
		return nil, false
	}
	for _, size := range thumbs {
		s, ok := size.(*tg.PhotoStrippedSize)
		if !ok {
			continue
		}
		if jpg, ok := StrippedPhotoToJPG(s.Bytes); ok {
			return jpg, true
		}
	}
	return nil, false
}
