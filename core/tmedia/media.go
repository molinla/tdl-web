package tmedia

import (
	"github.com/gotd/td/tg"
)

type Media struct {
	InputFileLoc tg.InputFileLocationClass // mtproto file location of the media file
	Name         string                    // file name
	Size         int64                     // size in bytes
	DC           int                       // which DC the media is stored
	Date         int64                     // media creation(upload) timestamp
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
		if _, _, ok := GetPhotoSize(p.Sizes); !ok {
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

	photoSize := &tg.PhotoSize{}
	for _, t := range thumbs {
		if p, ok := t.(*tg.PhotoSize); ok {
			photoSize = p
			break
		}
	}

	if photoSize == nil {
		return nil, false
	}

	return &Media{
		InputFileLoc: &tg.InputDocumentFileLocation{
			ID:            doc.ID,
			AccessHash:    doc.AccessHash,
			FileReference: doc.FileReference,
			ThumbSize:     photoSize.Type,
		},
		Name: "thumb.jpg",
		Size: int64(photoSize.Size),
		DC:   doc.DCID,
		Date: int64(doc.Date),
	}, true
}
