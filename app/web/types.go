package web

import (
	"context"
	"sync"

	"github.com/gotd/td/tg"
	"golang.org/x/sync/singleflight"

	"github.com/iyear/tdl/core/dcpool"
	"github.com/iyear/tdl/core/storage"
)

const (
	statusQueued     = "queued"
	statusCaching    = "caching"
	statusPaused     = "paused"
	statusCompleted  = "completed"
	statusError      = "error"
	statusSkipped    = "skipped"
	mediaVideo       = "video"
	mediaImage       = "image"
	mediaFile        = "file"
	defaultCachePerm = 0o755
	tempExt          = ".tmp"
	downloadPartSize = 1024 * 1024

	RangeTypeID   = "id"
	RangeTypeTime = "time"
)

type Options struct {
	Addr               string
	Dir                string
	CacheDir           string
	Files              []string
	URLs               []string
	Template           string
	Continue           bool
	SkipSame           bool
	Desc               bool
	Takeout            bool
	WebToken           string
	JellyfinURL        string
	JellyfinAPIKey     string
	JellyfinRefresh    bool
	JellyfinLibraryDir string
	// RangeType is empty (no filter), "id", or "time".
	RangeType string
	// RangeFrom/RangeTo are inclusive bounds for --type id|time.
	RangeFrom int
	RangeTo   int
	// RefreshMeta forces re-fetching Telegram metadata, ignoring disk cache.
	RefreshMeta bool
}

type Item struct {
	ID              string `json:"id"`
	PeerID          int64  `json:"peer_id"`
	MessageID       int    `json:"message_id"`
	LogicalPos      int    `json:"logical_pos"`
	Name            string `json:"name"`
	MIME            string `json:"mime"`
	Type            string `json:"type"`
	Size            int64  `json:"size"`
	Duration        int    `json:"duration,omitempty"`
	Date            int64  `json:"date,omitempty"` // message unix seconds
	Status          string `json:"status"`
	Progress        int64  `json:"progress"`
	Error           string `json:"error,omitempty"`
	TargetPath      string `json:"target_path"`
	ThumbURL        string `json:"thumb_url,omitempty"`
	CoverURL        string `json:"cover,omitempty"`
	PreviewURL      string `json:"preview_url,omitempty"`
	StreamURL       string `json:"stream_url,omitempty"`
	DownloadURL     string `json:"download_url"`
	ResumeCompleted bool   `json:"resume_completed"`
	SkipSame        bool   `json:"skip_same"`
	QueuePos        int    `json:"queue_pos,omitempty"` // 1-based wait queue; 0 if not waiting
	ManualPaused    bool   `json:"manual_paused,omitempty"`

	media *media
	thumb *media
}

type media struct {
	Location tg.InputFileLocationClass
	Name     string
	Size     int64
	DC       int
	MIME     string
}

type Server struct {
	opts Options
	pool dcpool.Pool
	kvd  storage.Storage
	ctx  context.Context

	mu           sync.RWMutex
	items        map[string]*Item
	order        []string
	fingerprint  string
	finished     map[int]struct{}
	downloading  map[string]struct{}
	dlPriority   map[string]bool
	preempted    map[string]struct{}
	cancels      map[string]context.CancelFunc
	importing    bool
	importError  string
	importTotal  int
	importDone   int
	importPhase  string
	importSource string
	importDetail string

	// Global download scheduler (shared -l/--limit).
	// Priority queue (play) gets a reserved slot; background may borrow it when idle.
	dlOnce      sync.Once
	dlMu        sync.Mutex
	dlPriQueue  []string
	dlQueue     []string
	dlPending   map[string]struct{}
	dlActive    int
	dlActivePri int
	dlWake      chan struct{}

	// Telegram media serve gate (thumb/preview fail-fast; stream reserved).
	tgServeOnce sync.Once
	tgShared    chan struct{}
	tgStream    chan struct{}

	// Coalesces same-item thumbnail/poster cache builds.
	thumbGroup singleflight.Group

	events chan struct{}
	jelly  *jellyfinClient
}
