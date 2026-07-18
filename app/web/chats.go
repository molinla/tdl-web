package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"text/template"

	"github.com/go-faster/errors"
	"github.com/gotd/td/telegram"
	tgpeer "github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/telegram/query"
	"github.com/gotd/td/tg"
	"go.uber.org/zap"

	appchat "github.com/iyear/tdl/app/chat"
	"github.com/iyear/tdl/core/logctx"
	"github.com/iyear/tdl/core/util/tutil"
	"github.com/iyear/tdl/pkg/tplfunc"
)

const (
	chatSaved    = "saved"
	chatPrivate  = "private"
	chatGroup    = "group"
	chatChannel  = "channel"
	chatPageSize = 300
)

type ChatInfo struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Type  string `json:"type"`
}

func (s *Server) handleChats(ctx context.Context, c *telegram.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		chats, err := s.loadChats(ctx, c)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, map[string]any{"chats": chats})
	}
}

func (s *Server) handleSelectChat(ctx context.Context, c *telegram.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
		var req struct {
			ID string `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		req.ID = strings.TrimSpace(req.ID)
		if req.ID == "" {
			s.mu.Lock()
			s.activeChat = ""
			s.activeTitle = ""
			s.mu.Unlock()
			s.notify()
			writeJSON(w, s.snapshot())
			return
		}

		chats, err := s.loadChats(ctx, c)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		title := ""
		for _, chat := range chats {
			if chat.ID == req.ID {
				title = chat.Title
				break
			}
		}
		s.mu.Lock()
		peer := s.chatPeers[req.ID]
		if peer == nil || title == "" {
			s.mu.Unlock()
			http.Error(w, "chat not found", http.StatusNotFound)
			return
		}
		if s.importing {
			s.mu.Unlock()
			http.Error(w, "another import is in progress", http.StatusConflict)
			return
		}
		s.activeChat = req.ID
		s.activeTitle = title
		s.importing = true
		s.importError = ""
		s.importPhase = phaseFetchMessages
		s.importSource = sourceTelegram
		s.importDetail = "加载群组最近消息"
		s.mu.Unlock()
		s.notify()

		if err := s.loadChatPage(ctx, req.ID, peer, false); err != nil {
			s.setImporting(false, err.Error())
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		s.setImporting(false, "")
		writeJSON(w, s.snapshot())
	}
}

func (s *Server) handleMoreChat(ctx context.Context) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		chatID := s.activeChat
		peer := s.chatPeers[chatID]
		switch {
		case chatID == "" || peer == nil:
			s.mu.Unlock()
			http.Error(w, "no active chat", http.StatusBadRequest)
			return
		case s.chatDone[chatID]:
			s.mu.Unlock()
			writeJSON(w, s.snapshot())
			return
		case s.importing:
			s.mu.Unlock()
			http.Error(w, "another import is in progress", http.StatusConflict)
			return
		}
		s.importing = true
		s.importError = ""
		s.importPhase = phaseFetchMessages
		s.importSource = sourceTelegram
		s.importDetail = "加载更早消息"
		s.mu.Unlock()
		s.notify()

		if err := s.loadChatPage(ctx, chatID, peer, true); err != nil {
			s.setImporting(false, err.Error())
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		s.setImporting(false, "")
		writeJSON(w, s.snapshot())
	}
}

func (s *Server) loadChats(ctx context.Context, c *telegram.Client) ([]ChatInfo, error) {
	s.mu.RLock()
	if len(s.chatList) > 0 {
		cached := append([]ChatInfo(nil), s.chatList...)
		s.mu.RUnlock()
		return cached, nil
	}
	s.mu.RUnlock()

	self, err := c.Self(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "get current account")
	}

	chats := []ChatInfo{{ID: chatSaved, Title: "我的收藏", Type: chatSaved}}
	peers := map[string]tg.InputPeerClass{chatSaved: &tg.InputPeerSelf{}}
	seen := map[string]struct{}{chatSaved: {}}
	dialogs, _ := appchat.FetchDialogsWithErrorHandling(ctx, c.API())
	for _, dialog := range dialogs {
		key := chatKeyFromInputPeer(dialog.Peer)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}

		var info ChatInfo
		switch peer := dialog.Peer.(type) {
		case *tg.InputPeerUser:
			if peer.UserID == self.ID {
				continue
			}
			entity, ok := dialog.Entities.User(peer.UserID)
			if !ok {
				continue
			}
			info = ChatInfo{ID: key, Title: userChatTitle(entity), Type: chatPrivate}
		case *tg.InputPeerChat:
			entity, ok := dialog.Entities.Chat(peer.ChatID)
			if !ok || entity.Left || entity.Deactivated {
				continue
			}
			info = ChatInfo{ID: key, Title: entity.Title, Type: chatGroup}
		case *tg.InputPeerChannel:
			entity, ok := dialog.Entities.Channel(peer.ChannelID)
			if !ok || entity.Left {
				continue
			}
			switch {
			case entity.Megagroup, entity.Gigagroup:
				info = ChatInfo{ID: key, Title: entity.Title, Type: chatGroup}
			case entity.Broadcast:
				info = ChatInfo{ID: key, Title: entity.Title, Type: chatChannel}
			default:
				continue
			}
		default:
			continue
		}
		if strings.TrimSpace(info.Title) == "" {
			continue
		}
		seen[key] = struct{}{}
		peers[key] = dialog.Peer
		chats = append(chats, info)
	}

	s.mu.Lock()
	s.chatList = append([]ChatInfo(nil), chats...)
	s.chatPeers = peers
	s.selfID = self.ID
	s.mu.Unlock()
	return chats, nil
}

func (s *Server) loadChatPage(ctx context.Context, chatID string, peer tg.InputPeerClass, older bool) error {
	if chatID == chatSaved {
		return s.loadSavedChatPage(ctx, older)
	}
	if s.opts.Takeout {
		return s.loadTakeoutChatPage(ctx, chatID, peer)
	}

	tpl, err := template.New("web-chat").
		Funcs(tplfunc.FuncMap(tplfunc.All...)).
		Parse(s.opts.Template)
	if err != nil {
		return errors.Wrap(err, "parse template")
	}

	s.mu.RLock()
	cursor := s.chatCursor[chatID]
	hadItems := len(s.chatOrder[chatID]) > 0
	s.mu.RUnlock()

	history := query.Messages(s.pool.Default(ctx)).GetHistory(peer).BatchSize(100)
	if older && cursor > 0 {
		history = history.OffsetID(cursor)
	}
	iter := history.Iter()
	scanned, oldest := 0, 0
	for scanned < chatPageSize {
		if !iter.Next(ctx) {
			break
		}
		scanned++
		elem := iter.Value()
		msgID := elem.Msg.GetID()
		if oldest == 0 || msgID < oldest {
			oldest = msgID
		}
		item, err := s.chatItem(tpl, chatID, elem.Msg, elem.Entities)
		if err != nil {
			continue
		}
		s.appendChatItem(chatID, item)
		if scanned%100 == 0 {
			s.mu.Lock()
			s.importDone = scanned
			s.mu.Unlock()
			s.notify()
		}
	}
	if err := iter.Err(); err != nil {
		return errors.Wrap(err, "get chat history")
	}

	s.mu.Lock()
	if !hadItems || older {
		next, done := advanceChatPage(cursor, oldest, scanned)
		s.chatCursor[chatID] = next
		s.chatDone[chatID] = done
	}
	total := len(s.chatOrder[chatID])
	s.importTotal = total
	s.importDone = total
	s.mu.Unlock()
	s.notify()
	return nil
}

func (s *Server) handleNewMessage(ctx context.Context, entities tg.Entities, update *tg.UpdateNewMessage) error {
	s.appendLiveMessage(ctx, entities, update.Message)
	return nil
}

func (s *Server) handleNewChannelMessage(ctx context.Context, entities tg.Entities, update *tg.UpdateNewChannelMessage) error {
	s.appendLiveMessage(ctx, entities, update.Message)
	return nil
}

func (s *Server) appendLiveMessage(ctx context.Context, entities tg.Entities, raw tg.MessageClass) {
	msg, ok := raw.AsNotEmpty()
	if !ok {
		return
	}
	s.mu.RLock()
	chatID := chatKeyFromPeer(msg.GetPeerID(), s.selfID)
	active := s.activeChat
	s.mu.RUnlock()
	if chatID == "" || chatID != active {
		return
	}

	tpl, err := template.New("web-chat-live").
		Funcs(tplfunc.FuncMap(tplfunc.All...)).
		Parse(s.opts.Template)
	if err != nil {
		logctx.From(ctx).Warn("failed to parse web chat template", zap.Error(err))
		return
	}
	item, err := s.chatItem(tpl, chatID, msg, tgpeer.EntitiesFromUpdate(entities))
	if err != nil {
		return
	}
	if s.appendChatItem(chatID, item) {
		s.notify()
	}
}

func (s *Server) chatItem(tpl *template.Template, chatID string, raw tg.NotEmptyMessage, entities tgpeer.Entities) (*Item, error) {
	s.mu.RLock()
	peerID := tutil.GetPeerID(raw.GetPeerID())
	if chatID == chatSaved {
		peerID = s.selfID
	}
	s.mu.RUnlock()

	id := sha256Hex(fmt.Sprintf("chat:%s:%d", chatID, raw.GetID()))[:16]
	item := &Item{
		ID:          id,
		ChatID:      chatID,
		PeerID:      peerID,
		MessageID:   raw.GetID(),
		Name:        fmt.Sprintf("消息 %d", raw.GetID()),
		Type:        mediaMessage,
		Date:        int64(raw.GetDate()),
		MessageKind: "message",
		Author:      messageAuthor(raw, entities),
		Status:      statusCompleted,
	}

	switch msg := raw.(type) {
	case *tg.MessageService:
		item.MessageKind = "service"
		item.Name = fmt.Sprintf("服务消息 %d", msg.ID)
		item.Text = serviceMessageText(msg.Action)
		return item, nil
	case *tg.Message:
		item.Text = msg.Message
		item.ForwardedFrom, item.SavedFrom = forwardSources(msg, entities)
		if savedPeer, ok := msg.GetSavedPeerID(); ok {
			item.savedPeer, _ = savedDialogInputPeer(savedPeer, entities, s.selfID)
			if item.SavedFrom == "" {
				item.SavedFrom = entityTitle(entities, savedPeer)
			}
		}
		if item.Author == "" {
			item.Author = msg.PostAuthor
		}
		if _, ok := msg.GetMedia(); !ok {
			return item, nil
		}

		main, thumb, mime, duration, err := convertMedia(msg)
		if err != nil {
			item.MediaUnavailable = err.Error()
			return item, nil
		}
		name, err := renderNameForPeer(tpl, peerID, msg, main)
		if err != nil {
			return nil, err
		}
		targetPath := joinPath(s.opts.Dir, name)
		item.Name = baseName(name)
		item.MIME = mime
		item.Type = mediaType(name, mime)
		item.Size = main.Size
		item.Duration = duration
		item.Status = statusQueued
		item.TargetPath = targetPath
		item.CoverAspect = coverAspect(main, thumb)
		item.DownloadURL = "/api/items/" + id + "/download"
		item.media = main
		item.thumb = thumb
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
		return item, nil
	default:
		return nil, errors.Errorf("unsupported message type %T", raw)
	}
}

func messageAuthor(msg tg.NotEmptyMessage, entities tgpeer.Entities) string {
	if msg.GetOut() {
		return "我"
	}
	from, ok := msg.GetFromID()
	if !ok {
		return ""
	}
	return entityTitle(entities, from)
}

func forwardSources(msg *tg.Message, entities tgpeer.Entities) (string, string) {
	fwd, ok := msg.GetFwdFrom()
	if !ok {
		return "", ""
	}
	forwarded, _ := fwd.GetFromName()
	if forwarded == "" {
		if from, ok := fwd.GetFromID(); ok {
			forwarded = entityTitle(entities, from)
		}
	}
	saved, _ := fwd.GetSavedFromName()
	if saved == "" {
		if from, ok := fwd.GetSavedFromPeer(); ok {
			saved = entityTitle(entities, from)
		}
	}
	return forwarded, saved
}

func entityTitle(entities tgpeer.Entities, p tg.PeerClass) string {
	switch p := p.(type) {
	case *tg.PeerUser:
		if user, ok := entities.User(p.UserID); ok {
			return userChatTitle(user)
		}
	case *tg.PeerChat:
		if chat, ok := entities.Chat(p.ChatID); ok {
			return chat.Title
		}
	case *tg.PeerChannel:
		if channel, ok := entities.Channel(p.ChannelID); ok {
			return channel.Title
		}
	}
	return ""
}

func serviceMessageText(action tg.MessageActionClass) string {
	if action == nil {
		return "服务消息"
	}
	name := strings.TrimPrefix(action.TypeName(), "messageAction")
	if name == "" {
		return "服务消息"
	}
	return "服务消息 · " + name
}

func (s *Server) appendChatItem(chatID string, item *Item) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if current := s.items[item.ID]; current != nil {
		item.Progress = current.Progress
		item.QueuePos = current.QueuePos
		item.ManualPaused = current.ManualPaused
		item.ResumeCompleted = current.ResumeCompleted
		item.SkipSame = current.SkipSame
		if current.Type == item.Type && current.Status != statusQueued {
			item.Status = current.Status
			item.Error = current.Error
		}
		s.items[item.ID] = item
		return false
	}
	s.items[item.ID] = item
	s.chatOrder[chatID] = append(s.chatOrder[chatID], item.ID)
	return true
}

func chatKeyFromInputPeer(peer tg.InputPeerClass) string {
	switch peer := peer.(type) {
	case *tg.InputPeerSelf:
		return chatSaved
	case *tg.InputPeerUser:
		return fmt.Sprintf("user:%d", peer.UserID)
	case *tg.InputPeerChat:
		return fmt.Sprintf("chat:%d", peer.ChatID)
	case *tg.InputPeerChannel:
		return fmt.Sprintf("channel:%d", peer.ChannelID)
	default:
		return ""
	}
}

func chatKeyFromPeer(peer tg.PeerClass, selfID int64) string {
	switch peer := peer.(type) {
	case *tg.PeerUser:
		if peer.UserID == selfID {
			return chatSaved
		}
		return fmt.Sprintf("user:%d", peer.UserID)
	case *tg.PeerChat:
		return fmt.Sprintf("chat:%d", peer.ChatID)
	case *tg.PeerChannel:
		return fmt.Sprintf("channel:%d", peer.ChannelID)
	}
	return ""
}

func advanceChatPage(cursor, oldest, scanned int) (int, bool) {
	if scanned == 0 || oldest == 0 || (cursor > 0 && oldest >= cursor) {
		return cursor, true
	}
	return oldest, scanned < chatPageSize
}

func userChatTitle(user *tg.User) string {
	if user.Deleted {
		return "已删除账号"
	}
	if user.Bot && user.Restricted {
		return "受限机器人"
	}
	if name := strings.TrimSpace(strings.TrimSpace(user.FirstName) + " " + strings.TrimSpace(user.LastName)); name != "" {
		return name
	}
	if user.Username != "" {
		return "@" + user.Username
	}
	if user.Bot {
		return "机器人"
	}
	return "私聊"
}
