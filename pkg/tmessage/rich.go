package tmessage

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/bcicen/jstream"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
	"github.com/mitchellh/mapstructure"
	"go.uber.org/zap"

	"github.com/iyear/tdl/core/dcpool"
	"github.com/iyear/tdl/core/logctx"
	"github.com/iyear/tdl/core/storage"
)

// MediaInfo is metadata extracted from Telegram Desktop / tdl export JSON.
// Enough to render a gallery without calling GetSingleMessage.
type MediaInfo struct {
	ID       int
	Date     int64
	FileName string
	Size     int64
	MIME     string
	Duration int
	Width    int
	Height   int
	Kind     string // video | image | file
	Caption  string
}

type RichDialog struct {
	Peer     tg.InputPeerClass
	PeerID   int64
	Messages []MediaInfo
}

type richMessage struct {
	ID              int         `mapstructure:"id"`
	Type            string      `mapstructure:"type"`
	Time            string      `mapstructure:"date_unixtime"`
	File            string      `mapstructure:"file"`
	FileName        string      `mapstructure:"file_name"`
	FileSize        int64       `mapstructure:"file_size"`
	Photo           string      `mapstructure:"photo"`
	MediaType       string      `mapstructure:"media_type"`
	MimeType        string      `mapstructure:"mime_type"`
	DurationSeconds int         `mapstructure:"duration_seconds"`
	Width           int         `mapstructure:"width"`
	Height          int         `mapstructure:"height"`
	Text            interface{} `mapstructure:"text"`
}

// FromFileRich parses export JSON into rich media rows (no per-message Telegram fetch).
func FromFileRich(ctx context.Context, pool dcpool.Pool, kvd storage.Storage, files []string, onlyMedia bool) ([]*RichDialog, error) {
	out := make([]*RichDialog, 0, len(files))
	for _, file := range files {
		d, err := parseFileRich(ctx, pool.Default(ctx), kvd, file, onlyMedia)
		if err != nil {
			return nil, err
		}
		logctx.From(ctx).Debug("Parse rich file",
			zap.String("file", file),
			zap.Int("num", len(d.Messages)))
		out = append(out, d)
	}
	return out, nil
}

func parseFileRich(ctx context.Context, client *tg.Client, kvd storage.Storage, file string, onlyMedia bool) (*RichDialog, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	peer, err := getChatInfo(ctx, client, kvd, f)
	if err != nil {
		return nil, err
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return collectRich(ctx, f, peer, onlyMedia)
}

func collectRich(ctx context.Context, r io.Reader, peer peers.Peer, onlyMedia bool) (*RichDialog, error) {
	d := jstream.NewDecoder(r, 2)
	out := &RichDialog{
		Peer:     peer.InputPeer(),
		PeerID:   peer.ID(),
		Messages: make([]MediaInfo, 0),
	}

	for mv := range d.Stream() {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		if mv.ValueType != jstream.Object {
			continue
		}
		fm := richMessage{}
		if err := mapstructure.WeakDecode(mv.Value, &fm); err != nil {
			return nil, err
		}
		if fm.ID < 0 || fm.Type != typeMessage {
			continue
		}
		hasFile := fm.File != "" || fm.FileName != ""
		hasPhoto := fm.Photo != ""
		if !hasFile && !hasPhoto && onlyMedia {
			continue
		}

		info := MediaInfo{
			ID:       fm.ID,
			FileName: pickFileName(fm),
			Size:     fm.FileSize,
			MIME:     fm.MimeType,
			Duration: fm.DurationSeconds,
			Width:    fm.Width,
			Height:   fm.Height,
			Caption:  textAsString(fm.Text),
		}
		if fm.Time != "" {
			if ts, err := strconv.ParseInt(fm.Time, 10, 64); err == nil {
				info.Date = ts
			}
		}
		if info.MIME == "" && hasPhoto {
			info.MIME = "image/jpeg"
		}
		info.Kind = classifyKind(fm.MediaType, info.MIME, info.FileName, hasPhoto)
		out.Messages = append(out.Messages, info)
	}
	return out, nil
}

func pickFileName(fm richMessage) string {
	if strings.TrimSpace(fm.FileName) != "" {
		return filepath.Base(fm.FileName)
	}
	f := strings.TrimSpace(fm.File)
	// Official export uses placeholder text when media was not downloaded.
	if f != "" && !strings.Contains(f, "File exceeds maximum size") && !strings.HasPrefix(f, "(") {
		return filepath.Base(f)
	}
	if strings.TrimSpace(fm.Photo) != "" && !strings.HasPrefix(fm.Photo, "(") {
		return filepath.Base(fm.Photo)
	}
	if fm.ID > 0 {
		ext := ""
		switch {
		case strings.HasPrefix(fm.MimeType, "video/"):
			ext = ".mp4"
		case strings.HasPrefix(fm.MimeType, "image/"):
			ext = ".jpg"
		case fm.Photo != "":
			ext = ".jpg"
		}
		return strconv.Itoa(fm.ID) + ext
	}
	return "file"
}

func classifyKind(mediaType, mime, name string, hasPhoto bool) string {
	mt := strings.ToLower(mediaType)
	if strings.Contains(mt, "video") || strings.HasPrefix(mime, "video/") {
		return "video"
	}
	if hasPhoto || strings.Contains(mt, "photo") || strings.HasPrefix(mime, "image/") {
		return "image"
	}
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".mp4"), strings.HasSuffix(lower, ".mkv"),
		strings.HasSuffix(lower, ".mov"), strings.HasSuffix(lower, ".webm"),
		strings.HasSuffix(lower, ".avi"):
		return "video"
	case strings.HasSuffix(lower, ".jpg"), strings.HasSuffix(lower, ".jpeg"),
		strings.HasSuffix(lower, ".png"), strings.HasSuffix(lower, ".webp"),
		strings.HasSuffix(lower, ".gif"):
		return "image"
	}
	return "file"
}

func textAsString(v interface{}) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	default:
		return ""
	}
}

// ToDialog converts RichDialog into the minimal Dialog used for resume fingerprints.
func (d *RichDialog) ToDialog() *Dialog {
	msgs := make([]int, 0, len(d.Messages))
	dates := map[int]int64{}
	for _, m := range d.Messages {
		msgs = append(msgs, m.ID)
		if m.Date > 0 {
			dates[m.ID] = m.Date
		}
	}
	return &Dialog{Peer: d.Peer, Messages: msgs, Dates: dates}
}
