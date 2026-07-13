package web

import (
	"encoding/json"
	"os"
	"path/filepath"

	"go.uber.org/zap"

	"github.com/iyear/tdl/core/logctx"
)

const metaCacheVersion = 5

type metaCacheFile struct {
	Version       int             `json:"version"`
	Fingerprint   string          `json:"fingerprint"`
	Template      string          `json:"template"`
	ExpectedTotal int             `json:"expected_total"`
	Items         []metaCacheItem `json:"items"`
}

type metaCacheItem struct {
	ID           string  `json:"id"`
	PeerID       int64   `json:"peer_id"`
	MessageID    int     `json:"message_id"`
	LogicalPos   int     `json:"logical_pos"`
	RelPath      string  `json:"rel_path"`
	Name         string  `json:"name"`
	MIME         string  `json:"mime"`
	Type         string  `json:"type"`
	Size         int64   `json:"size"`
	Duration     int     `json:"duration,omitempty"`
	CoverAspect  float64 `json:"cover_aspect,omitempty"`
	Date         int64   `json:"date,omitempty"`
	Status       string  `json:"status,omitempty"`
	Error        string  `json:"error,omitempty"`
	Progress     int64   `json:"progress,omitempty"`
	ManualPaused bool    `json:"manual_paused,omitempty"`
	// Do not cache Telegram InputFileLocation here; its file_reference expires.
}

func (s *Server) metaCachePath(fingerprint string) string {
	return filepath.Join(s.opts.CacheDir, "meta", fingerprint+".json")
}

func (s *Server) saveMetaCache() error {
	s.mu.RLock()
	if s.importing {
		s.mu.RUnlock()
		return nil
	}
	fp := s.fingerprint
	if fp == "" {
		s.mu.RUnlock()
		return nil
	}
	expected := s.importTotal
	if expected <= 0 {
		expected = len(s.order)
	}
	// Never persist a partial list under a full-fingerprint key.
	if expected > 0 && len(s.order) != expected {
		s.mu.RUnlock()
		return nil
	}
	out := metaCacheFile{
		Version:       metaCacheVersion,
		Fingerprint:   fp,
		Template:      s.opts.Template,
		ExpectedTotal: expected,
		Items:         make([]metaCacheItem, 0, len(s.order)),
	}
	for _, id := range s.order {
		it := s.items[id]
		if it == nil {
			continue
		}
		rel := it.Name
		if r, err := filepath.Rel(s.opts.Dir, it.TargetPath); err == nil {
			rel = r
		}
		out.Items = append(out.Items, metaCacheItem{
			ID:           it.ID,
			PeerID:       it.PeerID,
			MessageID:    it.MessageID,
			LogicalPos:   it.LogicalPos,
			RelPath:      rel,
			Name:         it.Name,
			MIME:         it.MIME,
			Type:         it.Type,
			Size:         it.Size,
			Duration:     it.Duration,
			CoverAspect:  it.CoverAspect,
			Date:         it.Date,
			Status:       it.Status,
			Error:        it.Error,
			Progress:     it.Progress,
			ManualPaused: it.ManualPaused,
		})
	}
	s.mu.RUnlock()

	path := s.metaCachePath(fp)
	if err := os.MkdirAll(filepath.Dir(path), defaultCachePerm); err != nil {
		return err
	}
	tmp := path + ".tmp"
	b, err := json.Marshal(out)
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// loadMetaCache loads a complete cache for fingerprint.
// expectedTotal must match the JSON message count; incomplete/legacy caches are rejected.
func (s *Server) loadMetaCache(fingerprint string, expectedTotal int) ([]*Item, bool) {
	if fingerprint == "" || s.opts.RefreshMeta {
		return nil, false
	}
	b, err := os.ReadFile(s.metaCachePath(fingerprint))
	if err != nil {
		return nil, false
	}
	var file metaCacheFile
	if err := json.Unmarshal(b, &file); err != nil {
		return nil, false
	}
	if file.Fingerprint != fingerprint {
		return nil, false
	}
	// Template mismatch would change paths; ignore cache.
	if file.Template != "" && file.Template != s.opts.Template {
		return nil, false
	}
	n := len(file.Items)
	complete := file.ExpectedTotal > 0 &&
		file.ExpectedTotal == expectedTotal &&
		n == expectedTotal &&
		file.Version == metaCacheVersion
	if !complete {
		log := zap.NewNop()
		if s.ctx != nil {
			log = logctx.From(s.ctx)
		}
		log.Info("meta cache incomplete, rebuilding",
			zap.String("fingerprint", fingerprint),
			zap.Int("cache_items", n),
			zap.Int("cache_expected", file.ExpectedTotal),
			zap.Int("json_expected", expectedTotal),
			zap.Int("cache_version", file.Version))
		return nil, false
	}

	items := make([]*Item, 0, n)
	for _, c := range file.Items {
		rel := c.RelPath
		if rel == "" {
			rel = c.Name
		}
		item := &Item{
			ID:           c.ID,
			PeerID:       c.PeerID,
			MessageID:    c.MessageID,
			LogicalPos:   c.LogicalPos,
			Name:         c.Name,
			MIME:         c.MIME,
			Type:         c.Type,
			Size:         c.Size,
			Duration:     c.Duration,
			CoverAspect:  c.CoverAspect,
			Date:         c.Date,
			Status:       statusQueued,
			Error:        c.Error,
			Progress:     c.Progress,
			ManualPaused: c.ManualPaused,
			TargetPath:   filepath.Join(s.opts.Dir, rel),
			DownloadURL:  "/api/items/" + c.ID + "/download",
		}
		// Restore permanent failure so --continue / auto image queue won't retry forever.
		if c.Status == statusError {
			item.Status = statusError
		} else if c.Status == statusPaused {
			item.Status = statusPaused
		}
		if item.Name == "" {
			item.Name = filepath.Base(rel)
		}
		switch item.Type {
		case mediaVideo:
			item.ThumbURL = "/api/items/" + item.ID + "/thumb"
			item.CoverURL = item.ThumbURL
			item.StreamURL = "/api/items/" + item.ID + "/stream"
		case mediaImage:
			item.ThumbURL = "/api/items/" + item.ID + "/thumb"
			item.CoverURL = item.ThumbURL
			item.PreviewURL = "/api/items/" + item.ID + "/preview"
		}
		items = append(items, item)
	}
	log := zap.NewNop()
	if s.ctx != nil {
		log = logctx.From(s.ctx)
	}
	log.Info("meta cache hit",
		zap.String("fingerprint", fingerprint),
		zap.Int("items", len(items)))
	return items, len(items) > 0
}
