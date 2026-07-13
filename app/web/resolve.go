package web

import (
	"context"
	"strconv"

	"github.com/go-faster/errors"
	"github.com/gotd/td/telegram/peers"

	"github.com/iyear/tdl/core/storage"
	"github.com/iyear/tdl/core/util/tutil"
)

// ensureMedia fills Telegram file locations for an item (from cache miss or expired loc).
// For video/image, also refetches when thumb is missing even if media is already present.
func (s *Server) ensureMedia(ctx context.Context, id string) (*Item, error) {
	s.mu.RLock()
	item := s.items[id]
	if item == nil {
		s.mu.RUnlock()
		return nil, errors.New("item not found")
	}
	hasMedia := item.media != nil && item.media.Location != nil
	hasThumb := item.thumb != nil && item.thumb.Location != nil
	needThumb := item.Type == mediaVideo || item.Type == mediaImage
	if hasMedia && (!needThumb || hasThumb) {
		cp := item
		s.mu.RUnlock()
		return cp, nil
	}
	peerID, msgID := item.PeerID, item.MessageID
	s.mu.RUnlock()

	manager := peers.Options{Storage: storage.NewPeers(s.kvd)}.Build(s.pool.Default(ctx))
	from, err := tutil.GetInputPeer(ctx, manager, strconv.FormatInt(peerID, 10))
	if err != nil {
		return nil, errors.Wrap(err, "resolve peer")
	}
	msgClient := s.pool.Default(ctx)
	if s.opts.Takeout {
		msgClient = s.pool.DefaultTakeout(ctx)
	}
	msg, err := tutil.GetSingleMessage(ctx, msgClient, from.InputPeer(), msgID)
	if err != nil {
		return nil, errors.Wrap(err, "get message")
	}
	main, thumb, mime, duration, err := convertMedia(msg)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	item = s.items[id]
	if item == nil {
		s.mu.Unlock()
		return nil, errors.New("item not found")
	}
	item.media = main
	item.thumb = thumb
	if item.MIME == "" {
		item.MIME = mime
	}
	if item.Duration == 0 && duration > 0 {
		item.Duration = duration
	}
	if item.Size == 0 && main != nil {
		item.Size = main.Size
	}
	if aspect := coverAspect(main, thumb); aspect > 0 {
		item.CoverAspect = aspect
	}
	out := item
	s.mu.Unlock()

	_ = s.saveMetaCache()
	return out, nil
}
