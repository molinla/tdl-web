package web

import (
	"context"
	"sort"
	"text/template"

	"github.com/go-faster/errors"
	tgpeer "github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/tg"

	"github.com/iyear/tdl/pkg/tplfunc"
)

const takeoutChatBatch = 100

type takeoutChatState struct {
	ranges []tg.MessageRange
	index  int
	cursor int
}

func (s *Server) loadTakeoutChatPage(ctx context.Context, chatID string, peer tg.InputPeerClass) error {
	tpl, err := template.New("web-chat-takeout").
		Funcs(tplfunc.FuncMap(tplfunc.All...)).
		Parse(s.opts.Template)
	if err != nil {
		return errors.Wrap(err, "parse template")
	}

	state, err := s.takeoutChatState(ctx, chatID)
	if err != nil {
		return err
	}

	loaded := 0
	for loaded < chatPageSize && state.index < len(state.ranges) {
		api := s.pool.DefaultTakeout(ctx)
		result, err := invokeMessagesRange(ctx, api, state.ranges[state.index], &tg.MessagesGetHistoryRequest{
			Peer:     peer,
			OffsetID: state.cursor,
			Limit:    takeoutChatBatch,
		})
		if err != nil {
			return errors.Wrap(err, "get takeout chat history")
		}

		messages, entities, err := chatMessagesResult(result)
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
			item, err := s.chatItem(tpl, chatID, msg, entities)
			if err != nil {
				continue
			}
			if s.appendChatItem(chatID, item) {
				loaded++
				if loaded >= chatPageSize {
					break
				}
			}
		}

		if len(messages) == 0 || oldest == 0 || (state.cursor > 0 && oldest >= state.cursor) || len(messages) < takeoutChatBatch {
			state.index++
			state.cursor = 0
		} else {
			state.cursor = oldest
		}
	}

	s.mu.Lock()
	s.chatDone[chatID] = state.index >= len(state.ranges)
	total := len(s.chatOrder[chatID])
	s.importTotal = total
	s.importDone = total
	s.mu.Unlock()
	s.notify()
	return nil
}

func (s *Server) takeoutChatState(ctx context.Context, chatID string) (*takeoutChatState, error) {
	s.mu.Lock()
	if s.takeoutChats == nil {
		s.takeoutChats = map[string]*takeoutChatState{}
	}
	if state := s.takeoutChats[chatID]; state != nil {
		s.mu.Unlock()
		return state, nil
	}
	s.mu.Unlock()

	ranges, err := s.pool.DefaultTakeout(ctx).MessagesGetSplitRanges(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "get takeout message ranges")
	}
	sortMessageRanges(ranges)
	state := &takeoutChatState{ranges: ranges}

	s.mu.Lock()
	if existing := s.takeoutChats[chatID]; existing != nil {
		state = existing
	} else {
		s.takeoutChats[chatID] = state
	}
	s.mu.Unlock()
	return state, nil
}

func invokeMessagesRange(ctx context.Context, api *tg.Client, r tg.MessageRange, query *tg.MessagesGetHistoryRequest) (tg.MessagesMessagesClass, error) {
	request := &tg.InvokeWithMessagesRangeRequest{
		Range: r,
		Query: query,
	}
	var result tg.MessagesMessagesBox
	if err := api.Invoker().Invoke(ctx, request, &result); err != nil {
		return nil, err
	}
	return result.Messages, nil
}

func chatMessagesResult(result tg.MessagesMessagesClass) ([]tg.MessageClass, tgpeer.Entities, error) {
	switch value := result.(type) {
	case *tg.MessagesMessages:
		return value.Messages, tgpeer.EntitiesFromResult(value), nil
	case *tg.MessagesMessagesSlice:
		return value.Messages, tgpeer.EntitiesFromResult(value), nil
	case *tg.MessagesChannelMessages:
		return value.Messages, tgpeer.EntitiesFromResult(value), nil
	default:
		return nil, tgpeer.Entities{}, errors.Errorf("unsupported chat messages result %T", result)
	}
}

func sortMessageRanges(ranges []tg.MessageRange) {
	sort.SliceStable(ranges, func(i, j int) bool {
		if ranges[i].MaxID == ranges[j].MaxID {
			return ranges[i].MinID > ranges[j].MinID
		}
		return ranges[i].MaxID > ranges[j].MaxID
	})
}
