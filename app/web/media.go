package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/go-faster/errors"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"

	"github.com/iyear/tdl/core/tmedia"
	"github.com/iyear/tdl/core/util/fsutil"
	"github.com/iyear/tdl/pkg/utils"
)

var placeholderSVG = `<svg xmlns="http://www.w3.org/2000/svg" width="320" height="180"><rect width="100%" height="100%" fill="#141414"/><text x="50%" y="50%" fill="#808080" text-anchor="middle" dominant-baseline="middle" font-family="sans-serif" font-size="18">no thumbnail</text></svg>`

func convertMedia(msg *tg.Message) (*media, *media, string, int, error) {
	md, ok := tmedia.GetMedia(msg)
	if !ok {
		return nil, nil, "", 0, errors.New(tmedia.MediaUnavailableReason(msg))
	}
	mime := "application/octet-stream"
	duration := 0
	var thumb *media
	switch m := msg.Media.(type) {
	case *tg.MessageMediaDocument:
		doc, ok := m.Document.AsNotEmpty()
		if !ok {
			return nil, nil, "", 0, errors.New("document is empty")
		}
		mime = doc.MimeType
		duration = videoDuration(doc)
		if th, ok := tmedia.GetDocumentThumb(doc); ok {
			thumb = &media{
				Location: th.InputFileLoc,
				Name:     th.Name,
				Size:     th.Size,
				DC:       th.DC,
				MIME:     "image/jpeg",
			}
		}
	case *tg.MessageMediaPhoto:
		mime = "image/jpeg"
	}
	main := &media{
		Location: md.InputFileLoc,
		Name:     md.Name,
		Size:     md.Size,
		DC:       md.DC,
		MIME:     mime,
	}
	if thumb == nil && strings.HasPrefix(mime, "image/") {
		thumb = main
	}
	return main, thumb, mime, duration, nil
}

func videoDuration(doc *tg.Document) int {
	for _, attr := range doc.Attributes {
		if a, ok := attr.(*tg.DocumentAttributeVideo); ok {
			return int(a.Duration)
		}
	}
	return 0
}

func renderName(tpl *template.Template, from peers.Peer, msg *tg.Message, item *media) (string, error) {
	var b strings.Builder
	err := tpl.Execute(&b, struct {
		DialogID     int64
		MessageID    int
		MessageDate  int64
		FileName     string
		FileCaption  string
		FileSize     string
		DownloadDate int64
	}{
		DialogID:     from.ID(),
		MessageID:    msg.ID,
		MessageDate:  int64(msg.Date),
		FileName:     item.Name,
		FileCaption:  msg.Message,
		FileSize:     utils.Byte.FormatBinaryBytes(item.Size),
		DownloadDate: time.Now().Unix(),
	})
	if err != nil {
		return "", errors.Wrap(err, "execute template")
	}
	return b.String(), nil
}

func stableID(fingerprint string, peerID int64, msgID, logical int, item *media) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s:%d:%d:%d:%s:%d", fingerprint, peerID, msgID, logical, item.Name, item.Size)
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func mediaType(name, mime string) string {
	lower := strings.ToLower(name)
	if strings.HasPrefix(mime, "video/") ||
		strings.HasSuffix(lower, ".mp4") ||
		strings.HasSuffix(lower, ".mkv") ||
		strings.HasSuffix(lower, ".mov") ||
		strings.HasSuffix(lower, ".webm") ||
		strings.HasSuffix(lower, ".avi") {
		return mediaVideo
	}
	if strings.HasPrefix(mime, "image/") {
		return mediaImage
	}
	return mediaFile
}

func sameFileExists(path string, size int64) bool {
	stat, err := os.Stat(path)
	if err != nil {
		return false
	}
	return fsutil.GetNameWithoutExt(path) == fsutil.GetNameWithoutExt(stat.Name()) && stat.Size() == size
}

func joinPath(dir, name string) string {
	return filepath.Join(dir, name)
}

func baseName(name string) string {
	return filepath.Base(name)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}
