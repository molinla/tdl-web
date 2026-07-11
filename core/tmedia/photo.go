package tmedia

import (
	"strconv"

	"github.com/gotd/td/tg"
)

func GetPhotoInfo(photo *tg.MessageMediaPhoto) (*Media, bool) {
	p, ok := photo.Photo.(*tg.Photo)
	if !ok {
		return nil, false
	}

	tp, size, ok := GetPhotoSize(p.Sizes)
	if !ok {
		return nil, false
	}
	return &Media{
		InputFileLoc: &tg.InputPhotoFileLocation{
			ID:            p.ID,
			AccessHash:    p.AccessHash,
			FileReference: p.FileReference,
			ThumbSize:     tp,
		},
		// Telegram photo is compressed, and extension is always jpg.
		Name: strconv.FormatInt(p.ID, 10) + ".jpg", // unique name
		Size: int64(size),
		DC:   p.DCID,
		Date: int64(p.Date),
	}, true
}

// GetPhotoSize picks the largest downloadable photo size.
// Prefer real PhotoSize / Progressive over in-memory CachedSize.
func GetPhotoSize(sizes []tg.PhotoSizeClass) (string, int, bool) {
	var (
		bestType string
		bestSize int
		found    bool
	)
	for _, size := range sizes {
		switch s := size.(type) {
		case *tg.PhotoSize:
			if !found || s.Size >= bestSize {
				bestType, bestSize, found = s.Type, s.Size, true
			}
		case *tg.PhotoSizeProgressive:
			sz := 0
			if n := len(s.Sizes); n > 0 {
				sz = s.Sizes[n-1]
			}
			if !found || sz >= bestSize {
				bestType, bestSize, found = s.Type, sz, true
			}
		case *tg.PhotoCachedSize:
			sz := len(s.Bytes)
			// Only use cached bytes when no remote size exists yet.
			if !found {
				bestType, bestSize, found = s.Type, sz, true
			}
		}
	}
	return bestType, bestSize, found
}
