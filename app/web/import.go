package web

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/go-faster/errors"
	"github.com/gotd/td/telegram/peers"
	"go.uber.org/zap"

	"github.com/iyear/tdl/app/dl"
	"github.com/iyear/tdl/core/logctx"
	"github.com/iyear/tdl/core/storage"
	"github.com/iyear/tdl/core/util/tutil"
	"github.com/iyear/tdl/pkg/key"
	"github.com/iyear/tdl/pkg/tmessage"
	"github.com/iyear/tdl/pkg/tplfunc"
	"github.com/iyear/tdl/pkg/utils"
)

// JSON-only list build is cheap; notify less often than Telegram fetch path.
const importNotifyEvery = 200

func normalizeRange(opts *Options) error {
	typ := strings.ToLower(strings.TrimSpace(opts.RangeType))
	opts.RangeType = typ
	if typ == "" {
		return nil
	}
	if typ != RangeTypeID && typ != RangeTypeTime {
		return fmt.Errorf("invalid --type %q, want id or time", opts.RangeType)
	}
	if opts.RangeTo == 0 && opts.RangeFrom == 0 {
		opts.RangeFrom = 0
		opts.RangeTo = math.MaxInt
	}
	if opts.RangeFrom > opts.RangeTo {
		opts.RangeFrom, opts.RangeTo = opts.RangeTo, opts.RangeFrom
	}
	return nil
}

func applyDialogRange(dialogs [][]*tmessage.Dialog, typ string, from, to int) [][]*tmessage.Dialog {
	if typ == "" {
		return dialogs
	}
	out := make([][]*tmessage.Dialog, 0, len(dialogs))
	for _, group := range dialogs {
		ng := make([]*tmessage.Dialog, 0, len(group))
		for _, d := range group {
			nd := &tmessage.Dialog{
				Peer:     d.Peer,
				Messages: make([]int, 0, len(d.Messages)),
				Dates:    map[int]int64{},
			}
			for _, id := range d.Messages {
				switch typ {
				case RangeTypeID:
					if id < from || id > to {
						continue
					}
					nd.Messages = append(nd.Messages, id)
					if d.Dates != nil {
						if dt, ok := d.Dates[id]; ok {
							nd.Dates[id] = dt
						}
					}
				case RangeTypeTime:
					if d.Dates != nil {
						if dt, ok := d.Dates[id]; ok && dt > 0 {
							if int(dt) < from || int(dt) > to {
								continue
							}
						}
					}
					nd.Messages = append(nd.Messages, id)
					if d.Dates != nil {
						if dt, ok := d.Dates[id]; ok {
							nd.Dates[id] = dt
						}
					}
				}
			}
			if len(nd.Messages) > 0 {
				ng = append(ng, nd)
			}
		}
		if len(ng) > 0 {
			out = append(out, ng)
		}
	}
	return out
}

func filterRichMessages(msgs []tmessage.MediaInfo, typ string, from, to int) []tmessage.MediaInfo {
	if typ == "" {
		return msgs
	}
	out := make([]tmessage.MediaInfo, 0, len(msgs))
	for _, m := range msgs {
		switch typ {
		case RangeTypeID:
			if m.ID < from || m.ID > to {
				continue
			}
		case RangeTypeTime:
			if m.Date > 0 && (int(m.Date) < from || int(m.Date) > to) {
				continue
			}
		}
		out = append(out, m)
	}
	return out
}

func countMessages(dialogs []*tmessage.Dialog) int {
	n := 0
	for _, d := range dialogs {
		n += len(d.Messages)
	}
	return n
}

func (s *Server) importSources(ctx context.Context, files, urls []string, rangeType string, rangeFrom, rangeTo int) error {
	// File exports: build list from JSON metadata (fast). URLs still need Telegram.
	if len(files) > 0 {
		if err := s.importFromJSONFiles(ctx, files, rangeType, rangeFrom, rangeTo); err != nil {
			return err
		}
	}
	if len(urls) > 0 {
		if err := s.importFromURLs(ctx, urls, rangeType, rangeFrom, rangeTo); err != nil {
			return err
		}
	}
	if len(files) == 0 && len(urls) == 0 {
		return nil
	}
	return nil
}

func (s *Server) importFromJSONFiles(ctx context.Context, files []string, rangeType string, rangeFrom, rangeTo int) error {
	s.setImportPhase(phaseParseJSON, sourceJSON, "读取 JSON 导出")
	rich, err := tmessage.FromFileRich(ctx, s.pool, s.kvd, files, true)
	if err != nil {
		return err
	}

	dialogs := make([]*tmessage.Dialog, 0, len(rich))
	filtered := make([]*tmessage.RichDialog, 0, len(rich))
	for _, rd := range rich {
		msgs := filterRichMessages(rd.Messages, rangeType, rangeFrom, rangeTo)
		if len(msgs) == 0 {
			continue
		}
		cp := *rd
		cp.Messages = msgs
		filtered = append(filtered, &cp)
		dialogs = append(dialogs, cp.ToDialog())
	}

	flat := dl.PrepareDialogsForResume([][]*tmessage.Dialog{dialogs}, s.opts.Desc)
	fingerprint := dl.FingerprintDialogs(flat)
	finished := map[int]struct{}{}
	if s.opts.Continue {
		loaded, err := s.loadFinished(ctx, fingerprint)
		if err != nil {
			return err
		}
		finished = loaded
	}

	total := 0
	for _, rd := range filtered {
		total += len(rd.Messages)
	}
	s.resetImportState(fingerprint, finished, total)
	s.notify()

	s.setImportPhase(phaseMetaCache, sourceCache, "加载本地列表缓存")
	if cached, ok := s.loadMetaCache(fingerprint, total); ok {
		if err := s.applyCachedItems(ctx, cached); err != nil {
			return err
		}
		logctx.From(ctx).Info("imported list from meta cache",
			zap.Int("items", len(cached)),
			zap.Int("expected", total),
			zap.String("fingerprint", fingerprint))
		s.resumePausedIfContinue(ctx)
		return nil
	}

	tpl, err := template.New("web-dl").
		Funcs(tplfunc.FuncMap(tplfunc.All...)).
		Parse(s.opts.Template)
	if err != nil {
		return errors.Wrap(err, "parse template")
	}

	s.setImportPhase(phaseBuildList, sourceJSON, "从 JSON 生成列表")

	// Mirror PrepareDialogsForResume order: dialogs sorted by peer, messages sorted.
	// filtered dialogs were converted via ToDialog then PrepareDialogsForResume;
	// rebuild item order from `flat` message IDs using a lookup.
	byPeerMsg := map[string]tmessage.MediaInfo{}
	for _, rd := range filtered {
		for _, m := range rd.Messages {
			key := fmt.Sprintf("%d:%d", rd.PeerID, m.ID)
			byPeerMsg[key] = m
		}
	}

	manager := peers.Options{Storage: storage.NewPeers(s.kvd)}.Build(s.pool.Default(ctx))
	logical := 0
	added := 0
	peerResolved := false
	for _, d := range flat {
		if !peerResolved {
			s.setImportPhase(phaseResolvePeer, sourceTelegram, "解析 Telegram 会话")
			peerResolved = true
		}
		s.setImportPhase(phaseBuildList, sourceJSON, "从 JSON 生成列表")
		from, err := manager.FromInputPeer(ctx, d.Peer)
		if err != nil {
			return errors.Wrap(err, "resolve peer")
		}
		for _, msgID := range d.Messages {
			meta, ok := byPeerMsg[fmt.Sprintf("%d:%d", from.ID(), msgID)]
			if !ok {
				logical++
				s.bumpImportDone()
				continue
			}
			rel, err := renderNameFromMeta(tpl, from.ID(), meta)
			if err != nil {
				return err
			}
			targetPath := joinPath(s.opts.Dir, rel)
			id := stableIDFromMeta(fingerprint, from.ID(), meta.ID, logical, meta)
			kind := meta.Kind
			if kind == "" {
				kind = mediaType(meta.FileName, meta.MIME)
			}
			item := &Item{
				ID:          id,
				PeerID:      from.ID(),
				MessageID:   meta.ID,
				LogicalPos:  logical,
				Name:        baseName(rel),
				MIME:        meta.MIME,
				Type:        kind,
				Size:        meta.Size,
				Duration:    meta.Duration,
				Date:        meta.Date,
				Status:      statusQueued,
				TargetPath:  targetPath,
				DownloadURL: "/api/items/" + id + "/download",
			}
			switch item.Type {
			case mediaVideo:
				item.ThumbURL = "/api/items/" + id + "/thumb"
				item.CoverURL = item.ThumbURL
				item.StreamURL = "/api/items/" + id + "/stream"
			case mediaImage:
				item.ThumbURL = "/api/items/" + id + "/thumb"
				item.CoverURL = item.ThumbURL
				item.PreviewURL = "/api/items/" + id + "/preview"
			}

			s.mu.RLock()
			_, resumeOK := s.finished[logical]
			s.mu.RUnlock()
			if resumeOK {
				item.Status = statusCompleted
				item.Progress = item.Size
				item.ResumeCompleted = true
			} else if s.opts.SkipSame && sameFileExists(targetPath, item.Size) {
				item.Status = statusCompleted
				item.Progress = item.Size
				item.SkipSame = true
				s.mu.Lock()
				s.finished[logical] = struct{}{}
				s.mu.Unlock()
			} else {
				applyDiskProgress(item)
			}

			s.appendItem(item)
			added++
			logical++
			s.bumpImportDone()
			if added%importNotifyEvery == 0 {
				s.notify()
			}
		}
	}

	if err := s.saveFinishedOrClear(ctx); err != nil {
		return err
	}
	_ = s.saveMetaCache()
	s.notify()
	logctx.From(ctx).Info("imported list from JSON metadata",
		zap.Int("items", added),
		zap.Int("expected", total),
		zap.String("fingerprint", fingerprint))
	s.resumePausedIfContinue(ctx)
	return nil
}

func (s *Server) importFromURLs(ctx context.Context, urls []string, rangeType string, rangeFrom, rangeTo int) error {
	s.setImportPhase(phaseFetchMessages, sourceTelegram, "从 Telegram 拉取消息")
	raw, err := tmessage.FromURL(ctx, s.pool, s.kvd, urls)()
	if err != nil {
		return err
	}
	dialogs := applyDialogRange([][]*tmessage.Dialog{raw}, rangeType, rangeFrom, rangeTo)
	flat := dl.PrepareDialogsForResume(dialogs, s.opts.Desc)
	fingerprint := dl.FingerprintDialogs(flat)

	// Merge into existing state if JSON already imported.
	s.mu.RLock()
	baseLogical := len(s.order)
	existingFP := s.fingerprint
	s.mu.RUnlock()
	if existingFP == "" {
		finished := map[int]struct{}{}
		if s.opts.Continue {
			finished, err = s.loadFinished(ctx, fingerprint)
			if err != nil {
				return err
			}
		}
		s.resetImportState(fingerprint, finished, countMessages(flat))
	} else {
		s.mu.Lock()
		s.importTotal += countMessages(flat)
		s.mu.Unlock()
	}

	manager := peers.Options{Storage: storage.NewPeers(s.kvd)}.Build(s.pool.Default(ctx))
	tpl, err := template.New("web-dl").
		Funcs(tplfunc.FuncMap(tplfunc.All...)).
		Parse(s.opts.Template)
	if err != nil {
		return errors.Wrap(err, "parse template")
	}

	logical := baseLogical
	added := 0
	for _, d := range flat {
		from, err := manager.FromInputPeer(ctx, d.Peer)
		if err != nil {
			return err
		}
		for _, msgID := range d.Messages {
			msg, err := tutil.GetSingleMessage(ctx, s.pool.Default(ctx), from.InputPeer(), msgID)
			if err != nil {
				logical++
				s.bumpImportDone()
				continue
			}
			main, thumb, mime, duration, err := convertMedia(msg)
			if err != nil {
				logical++
				s.bumpImportDone()
				continue
			}
			name, err := renderName(tpl, from, msg, main)
			if err != nil {
				return err
			}
			targetPath := joinPath(s.opts.Dir, name)
			id := stableID(fingerprint, from.ID(), msgID, logical, main)
			item := &Item{
				ID:          id,
				PeerID:      from.ID(),
				MessageID:   msgID,
				LogicalPos:  logical,
				Name:        baseName(name),
				MIME:        mime,
				Type:        mediaType(name, mime),
				Size:        main.Size,
				Duration:    duration,
				Date:        int64(msg.Date),
				Status:      statusQueued,
				TargetPath:  targetPath,
				DownloadURL: "/api/items/" + id + "/download",
				media:       main,
				thumb:       thumb,
			}
			switch item.Type {
			case mediaVideo:
				item.ThumbURL = "/api/items/" + id + "/thumb"
				item.CoverURL = item.ThumbURL
				item.StreamURL = "/api/items/" + id + "/stream"
			case mediaImage:
				item.ThumbURL = "/api/items/" + id + "/thumb"
				item.CoverURL = item.ThumbURL
				item.PreviewURL = "/api/items/" + id + "/preview"
			}
			if sameFileExists(targetPath, item.Size) {
				item.Status = statusCompleted
				item.Progress = item.Size
				item.SkipSame = true
			} else {
				applyDiskProgress(item)
			}
			s.appendItem(item)
			added++
			logical++
			s.bumpImportDone()
			if added%25 == 0 {
				s.notify()
			}
		}
	}
	_ = s.saveMetaCache()
	s.notify()
	logctx.From(ctx).Info("imported list from URLs",
		zap.Int("items", added),
		zap.String("fingerprint", fingerprint))
	s.resumePausedIfContinue(ctx)
	return nil
}

func renderNameFromMeta(tpl *template.Template, dialogID int64, meta tmessage.MediaInfo) (string, error) {
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
		DialogID:     dialogID,
		MessageID:    meta.ID,
		MessageDate:  meta.Date,
		FileName:     meta.FileName,
		FileCaption:  meta.Caption,
		FileSize:     utils.Byte.FormatBinaryBytes(meta.Size),
		DownloadDate: time.Now().Unix(),
	})
	if err != nil {
		return "", errors.Wrap(err, "execute template")
	}
	return b.String(), nil
}

func stableIDFromMeta(fingerprint string, peerID int64, msgID, logical int, meta tmessage.MediaInfo) string {
	h := sha256Hex(fmt.Sprintf("%s:%d:%d:%d:%s:%d", fingerprint, peerID, msgID, logical, meta.FileName, meta.Size))
	return h[:16]
}

func (s *Server) applyCachedItems(ctx context.Context, cached []*Item) error {
	s.setImportPhase(phaseScanDisk, sourceDisk, "检查本地已下载文件")
	total := len(cached)
	items := make(map[string]*Item, total)
	order := make([]string, 0, total)

	s.mu.Lock()
	finished := s.finished
	s.mu.Unlock()

	for _, item := range cached {
		logical := item.LogicalPos
		if _, resumeOK := finished[logical]; resumeOK {
			item.Status = statusCompleted
			item.Progress = item.Size
			item.Error = ""
			item.ResumeCompleted = true
		} else if s.opts.SkipSame && sameFileExists(item.TargetPath, item.Size) {
			item.Status = statusCompleted
			item.Progress = item.Size
			item.Error = ""
			item.SkipSame = true
			finished[logical] = struct{}{}
		} else if item.Status == statusError {
			// Keep cached permanent failure; do not reset to queued.
		} else {
			applyDiskProgress(item)
		}
		items[item.ID] = item
		order = append(order, item.ID)
	}

	s.mu.Lock()
	s.items = items
	s.order = order
	s.finished = finished
	s.importTotal = total
	s.importDone = total
	s.mu.Unlock()

	if err := s.saveFinishedOrClear(ctx); err != nil {
		return err
	}
	s.notify()
	return nil
}

// resumePausedIfContinue enqueues interrupted downloads when --continue is set.
// Permanent errors (deleted media, etc.) are not auto-retried.
func (s *Server) resumePausedIfContinue(ctx context.Context) {
	if !s.opts.Continue {
		return
	}
	ids := make([]string, 0)
	s.mu.RLock()
	for _, id := range s.order {
		it := s.items[id]
		if it == nil || it.Status == statusCompleted || it.Status == statusError {
			continue
		}
		if it.Status == statusPaused || tmpProgress(it.TargetPath, it.Size) > 0 {
			ids = append(ids, id)
		}
	}
	s.mu.RUnlock()
	if len(ids) == 0 {
		return
	}
	s.setImportPhase(phaseResumeDownloads, sourceTelegram, "续传未完成下载")
	logctx.From(ctx).Info("auto-resuming paused downloads", zap.Int("count", len(ids)))
	s.enqueueDownloads(ctx, ids)
}

func (s *Server) resetImportState(fingerprint string, finished map[int]struct{}, total int) {
	s.mu.Lock()
	s.items = map[string]*Item{}
	s.order = make([]string, 0, total)
	s.fingerprint = fingerprint
	s.finished = finished
	s.downloading = map[string]struct{}{}
	s.importTotal = total
	s.importDone = 0
	s.importError = ""
	s.mu.Unlock()
}

func (s *Server) appendItem(item *Item) {
	s.mu.Lock()
	s.items[item.ID] = item
	s.order = append(s.order, item.ID)
	s.mu.Unlock()
}

func (s *Server) bumpImportDone() {
	s.mu.Lock()
	s.importDone++
	done, total := s.importDone, s.importTotal
	s.mu.Unlock()
	if done == total || done%importNotifyEvery == 0 {
		s.notify()
	}
}

func (s *Server) loadFinished(ctx context.Context, fingerprint string) (map[int]struct{}, error) {
	ret := map[int]struct{}{}
	if fingerprint == "" {
		return ret, nil
	}
	b, err := s.kvd.Get(ctx, key.Resume(fingerprint))
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return ret, nil
		}
		return nil, err
	}
	if len(b) == 0 {
		return ret, nil
	}
	if err := json.Unmarshal(b, &ret); err != nil {
		return nil, err
	}
	return ret, nil
}

func (s *Server) saveFinishedOrClear(ctx context.Context) error {
	s.mu.RLock()
	fingerprint := s.fingerprint
	finished := make(map[int]struct{}, len(s.finished))
	for k, v := range s.finished {
		finished[k] = v
	}
	allDone := len(s.items) > 0
	for _, it := range s.items {
		if it.Status != statusCompleted && it.Status != statusSkipped {
			allDone = false
			break
		}
	}
	s.mu.RUnlock()

	if fingerprint == "" {
		return nil
	}
	if allDone {
		return s.kvd.Delete(ctx, key.Resume(fingerprint))
	}
	b, err := json.Marshal(finished)
	if err != nil {
		return err
	}
	return s.kvd.Set(ctx, key.Resume(fingerprint), b)
}

func parseRangeForm(rType, fromStr, toStr string) (string, int, int, error) {
	typ := strings.ToLower(strings.TrimSpace(rType))
	if typ == "" {
		return "", 0, 0, nil
	}
	if typ != RangeTypeID && typ != RangeTypeTime {
		return "", 0, 0, fmt.Errorf("invalid type %q", rType)
	}
	from, to := 0, math.MaxInt
	if fromStr != "" {
		v, err := strconv.Atoi(fromStr)
		if err != nil {
			return "", 0, 0, fmt.Errorf("invalid from: %w", err)
		}
		from = v
	}
	if toStr != "" {
		v, err := strconv.Atoi(toStr)
		if err != nil {
			return "", 0, 0, fmt.Errorf("invalid to: %w", err)
		}
		to = v
	}
	if from > to {
		from, to = to, from
	}
	return typ, from, to, nil
}
