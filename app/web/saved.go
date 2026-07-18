package web

import (
	"context"
	"fmt"
	"text/template"

	"github.com/go-faster/errors"
	tgpeer "github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/tg"
	"github.com/iyear/tdl/pkg/tplfunc"
)

const savedMessageBatch = 100

type savedChatSource struct {
	key     string
	peer    tg.InputPeerClass
	cursor  int
	done    bool
	pending []savedMessage
}

type savedMessage struct {
	msg      tg.NotEmptyMessage
	entities tgpeer.Entities
}

type savedDialogsPage struct {
	dialogs  []tg.SavedDialogClass
	entities tgpeer.Entities
	count    int
	messages []tg.MessageClass
}

func (s *Server) loadSavedChatPage(ctx context.Context, _ bool) error {
	if err := s.ensureSavedSources(ctx); err != nil {
		return err
	}

	tpl, err := templateForChat(s.opts.Template)
	if err != nil {
		return err
	}

	loaded := 0
	for loaded < chatPageSize {
		if err := s.fillSavedPending(ctx); err != nil {
			return err
		}

		next, ok := s.popSavedMessage()
		if !ok {
			break
		}
		item, err := s.chatItem(tpl, chatSaved, next.msg, next.entities)
		if err != nil {
			continue
		}
		if s.appendChatItem(chatSaved, item) {
			loaded++
		}
	}

	s.mu.Lock()
	done := true
	for _, source := range s.savedSources {
		if !source.done || len(source.pending) > 0 {
			done = false
			break
		}
	}
	s.chatDone[chatSaved] = done
	s.chatCursor[chatSaved] = 0
	total := len(s.chatOrder[chatSaved])
	s.importTotal = total
	s.importDone = total
	s.mu.Unlock()
	s.notify()
	return nil
}

func (s *Server) ensureSavedSources(ctx context.Context) error {
	s.mu.RLock()
	loaded := s.savedLoaded
	s.mu.RUnlock()
	if loaded {
		return nil
	}

	api := s.pool.Default(ctx)
	if s.opts.Takeout {
		api = s.pool.DefaultTakeout(ctx)
	}
	offsetDate, offsetID := 0, 0
	offsetPeer := tg.InputPeerClass(&tg.InputPeerEmpty{})
	sources := make([]*savedChatSource, 0)
	seen := make(map[string]struct{})
	for {
		result, err := api.MessagesGetSavedDialogs(ctx, &tg.MessagesGetSavedDialogsRequest{
			OffsetDate: offsetDate,
			OffsetID:   offsetID,
			OffsetPeer: offsetPeer,
			Limit:      100,
		})
		if err != nil {
			return errors.Wrap(err, "get saved dialogs")
		}

		page, err := savedDialogsResult(result)
		if err != nil {
			return err
		}
		for _, raw := range page.dialogs {
			dialog, ok := raw.(*tg.SavedDialog)
			if !ok || dialog.Peer == nil {
				continue
			}

			input, err := savedDialogInputPeer(dialog.Peer, page.entities, s.selfID)
			if err != nil {
				continue
			}
			key := savedSourceKey(dialog.Peer)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			sources = append(sources, &savedChatSource{
				key:  key,
				peer: input,
			})
		}

		if len(page.dialogs) == 0 || page.count <= len(seen) || len(page.dialogs) < 100 {
			break
		}
		last, ok := page.dialogs[len(page.dialogs)-1].(*tg.SavedDialog)
		if !ok || last.Peer == nil {
			break
		}
		nextPeer, err := savedDialogInputPeer(last.Peer, page.entities, s.selfID)
		if err != nil {
			break
		}
		nextDate := 0
		for _, message := range page.messages {
			if message.GetID() != last.TopMessage {
				continue
			}
			if value, ok := message.AsNotEmpty(); ok {
				nextDate = value.GetDate()
				break
			}
		}
		if offsetID == last.TopMessage && offsetPeer.TypeName() == nextPeer.TypeName() {
			break
		}
		offsetDate = nextDate
		offsetID = last.TopMessage
		offsetPeer = nextPeer
	}

	s.mu.Lock()
	if !s.savedLoaded {
		s.savedSources = sources
		s.savedLoaded = true
	}
	s.mu.Unlock()
	return nil
}

func (s *Server) fillSavedPending(ctx context.Context) error {
	s.mu.RLock()
	sources := append([]*savedChatSource(nil), s.savedSources...)
	s.mu.RUnlock()

	for _, source := range sources {
		if len(source.pending) > 0 || source.done {
			continue
		}
		if err := s.fetchSavedSource(ctx, source); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) fetchSavedSource(ctx context.Context, source *savedChatSource) error {
	api := s.pool.Default(ctx)
	if s.opts.Takeout {
		api = s.pool.DefaultTakeout(ctx)
	}
	result, err := api.MessagesGetSavedHistory(ctx, &tg.MessagesGetSavedHistoryRequest{
		Peer:     source.peer,
		OffsetID: source.cursor,
		Limit:    savedMessageBatch,
	})
	if err != nil {
		return errors.Wrapf(err, "get saved history %s", source.key)
	}

	messages, entities, err := savedMessagesResult(result)
	if err != nil {
		return err
	}
	oldest := 0
	for _, raw := range messages {
		msg, ok := raw.AsNotEmpty()
		if !ok {
			continue
		}
		if oldest == 0 || msg.GetID() < oldest {
			oldest = msg.GetID()
		}
		source.pending = append(source.pending, savedMessage{
			msg:      msg,
			entities: entities,
		})
	}

	if len(messages) == 0 || oldest == 0 || (source.cursor > 0 && oldest >= source.cursor) || len(messages) < savedMessageBatch {
		source.done = true
	} else {
		source.cursor = oldest
	}
	return nil
}

func (s *Server) popSavedMessage() (savedMessage, bool) {
	s.mu.RLock()
	sources := append([]*savedChatSource(nil), s.savedSources...)
	s.mu.RUnlock()

	var (
		bestSource *savedChatSource
		bestIndex  = -1
		bestID     = 0
	)
	for _, source := range sources {
		for i, candidate := range source.pending {
			id := candidate.msg.GetID()
			if bestIndex == -1 || id > bestID {
				bestSource = source
				bestIndex = i
				bestID = id
			}
		}
	}
	if bestSource == nil {
		return savedMessage{}, false
	}
	result := bestSource.pending[bestIndex]
	bestSource.pending = append(bestSource.pending[:bestIndex], bestSource.pending[bestIndex+1:]...)
	return result, true
}

func savedDialogsResult(result tg.MessagesSavedDialogsClass) (savedDialogsPage, error) {
	switch value := result.(type) {
	case *tg.MessagesSavedDialogs:
		return savedDialogsPage{
			dialogs:  value.Dialogs,
			entities: tgpeer.EntitiesFromResult(value),
			count:    len(value.Dialogs),
			messages: value.Messages,
		}, nil
	case *tg.MessagesSavedDialogsSlice:
		return savedDialogsPage{
			dialogs:  value.Dialogs,
			entities: tgpeer.EntitiesFromResult(value),
			count:    value.Count,
			messages: value.Messages,
		}, nil
	default:
		return savedDialogsPage{}, errors.Errorf("unsupported saved dialogs result %T", result)
	}
}

func savedMessagesResult(result tg.MessagesMessagesClass) ([]tg.MessageClass, tgpeer.Entities, error) {
	switch value := result.(type) {
	case *tg.MessagesMessages:
		return value.Messages, tgpeer.EntitiesFromResult(value), nil
	case *tg.MessagesMessagesSlice:
		return value.Messages, tgpeer.EntitiesFromResult(value), nil
	default:
		return nil, tgpeer.Entities{}, errors.Errorf("unsupported saved messages result %T", result)
	}
}

func savedDialogInputPeer(peer tg.PeerClass, entities tgpeer.Entities, selfID int64) (tg.InputPeerClass, error) {
	if user, ok := peer.(*tg.PeerUser); ok && user.UserID == selfID {
		return &tg.InputPeerSelf{}, nil
	}
	return entities.ExtractPeer(peer)
}

func savedSourceKey(peer tg.PeerClass) string {
	switch peer := peer.(type) {
	case *tg.PeerUser:
		return fmt.Sprintf("saved:user:%d", peer.UserID)
	case *tg.PeerChat:
		return fmt.Sprintf("saved:chat:%d", peer.ChatID)
	case *tg.PeerChannel:
		return fmt.Sprintf("saved:channel:%d", peer.ChannelID)
	default:
		return fmt.Sprintf("saved:%T", peer)
	}
}

func getSavedMessage(ctx context.Context, api *tg.Client, peer tg.InputPeerClass, messageID int) (*tg.Message, error) {
	result, err := api.MessagesGetSavedHistory(ctx, &tg.MessagesGetSavedHistoryRequest{
		Peer:     peer,
		OffsetID: messageID + 1,
		Limit:    1,
	})
	if err != nil {
		return nil, err
	}
	messages, _, err := savedMessagesResult(result)
	if err != nil {
		return nil, err
	}
	for _, raw := range messages {
		message, ok := raw.(*tg.Message)
		if ok && message.ID == messageID {
			return message, nil
		}
	}
	return nil, errors.Errorf("saved message %d not found", messageID)
}

func templateForChat(source string) (*template.Template, error) {
	return template.New("web-chat").
		Funcs(tplfunc.FuncMap(tplfunc.All...)).
		Parse(source)
}
