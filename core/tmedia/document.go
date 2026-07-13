package tmedia

import (
	"strconv"

	"github.com/gabriel-vasile/mimetype"
	"github.com/gotd/td/tg"
)

func GetDocumentInfo(doc *tg.MessageMediaDocument) (*Media, bool) {
	d, ok := doc.Document.(*tg.Document)
	if !ok {
		return nil, false
	}
	width, height, supportsStreaming, preloadPrefixSize := getDocumentVideoInfo(d)

	return &Media{
		InputFileLoc: &tg.InputDocumentFileLocation{
			ID:            d.ID,
			AccessHash:    d.AccessHash,
			FileReference: d.FileReference,
		},
		Name:              GetDocumentName(d),
		Size:              d.Size,
		Width:             width,
		Height:            height,
		DC:                d.DCID,
		Date:              int64(d.Date),
		SupportsStreaming: supportsStreaming,
		PreloadPrefixSize: preloadPrefixSize,
	}, true
}

func getDocumentVideoInfo(doc *tg.Document) (int, int, bool, int) {
	for _, attr := range doc.Attributes {
		switch a := attr.(type) {
		case *tg.DocumentAttributeVideo:
			preload, _ := a.GetPreloadPrefixSize()
			return a.W, a.H, a.GetSupportsStreaming(), preload
		case *tg.DocumentAttributeImageSize:
			return a.W, a.H, false, 0
		}
	}
	return 0, 0, false, 0
}

func GetDocumentName(doc *tg.Document) string {
	for _, attr := range doc.Attributes {
		name, ok := attr.(*tg.DocumentAttributeFilename)
		if ok {
			return name.FileName
		}
	}

	// #185: stable file name so --skip-same can work
	mime := mimetype.Lookup(doc.MimeType)
	ext := ".unknown"
	if mime != nil {
		ext = mime.Extension()
	}
	return strconv.FormatInt(doc.ID, 10) + ext
}
