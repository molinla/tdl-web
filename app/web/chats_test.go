package web

import (
	"strings"
	"testing"
	"text/template"

	tgpeer "github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/tg"
)

func TestChatKeys(t *testing.T) {
	if got := chatKeyFromInputPeer(&tg.InputPeerChat{ChatID: 42}); got != "chat:42" {
		t.Fatalf("input chat key = %q", got)
	}
	if got := chatKeyFromPeer(&tg.PeerChannel{ChannelID: 7}, 0); got != "channel:7" {
		t.Fatalf("channel key = %q", got)
	}
	if got := chatKeyFromPeer(&tg.PeerUser{UserID: 9}, 9); got != chatSaved {
		t.Fatalf("saved key = %q", got)
	}
	if got := chatKeyFromPeer(&tg.PeerUser{UserID: 8}, 9); got != "user:8" {
		t.Fatalf("private chat key = %q", got)
	}
	cursor, done := advanceChatPage(0, 100, chatPageSize)
	if cursor != 100 || done {
		t.Fatalf("first page cursor=%d done=%v", cursor, done)
	}
	cursor, done = advanceChatPage(cursor, 50, 20)
	if cursor != 50 || !done {
		t.Fatalf("last page cursor=%d done=%v", cursor, done)
	}
}

func TestItemListUsesSelectedChat(t *testing.T) {
	s := &Server{
		items: map[string]*Item{
			"local": {ID: "local"},
			"live":  {ID: "live", ChatID: "chat:42"},
		},
		order:      []string{"local"},
		chatOrder:  map[string][]string{"chat:42": {"live"}},
		activeChat: "chat:42",
	}
	items := s.itemListLocked(nil)
	if len(items) != 1 || items[0].ID != "live" {
		t.Fatalf("selected chat items = %#v", items)
	}
}

func TestChatItemKeepsTextAndServiceMessages(t *testing.T) {
	s := &Server{
		opts:   Options{Dir: t.TempDir()},
		selfID: 99,
	}
	tpl := template.Must(template.New("name").Parse(`{{.DialogID}}_{{.MessageID}}_{{.FileName}}`))
	entities := tgpeer.NewEntities(nil, nil, nil)

	textItem, err := s.chatItem(tpl, "chat:42", &tg.Message{
		ID:      7,
		PeerID:  &tg.PeerChat{ChatID: 42},
		Date:    100,
		Message: "hello",
	}, entities)
	if err != nil {
		t.Fatal(err)
	}
	if textItem.Type != mediaMessage || textItem.Text != "hello" || textItem.Status != statusCompleted {
		t.Fatalf("text item = %#v", textItem)
	}
	if textItem.DownloadURL != "" {
		t.Fatalf("text download URL = %q", textItem.DownloadURL)
	}

	serviceItem, err := s.chatItem(tpl, "chat:42", &tg.MessageService{
		ID:     8,
		PeerID: &tg.PeerChat{ChatID: 42},
		Date:   101,
		Action: &tg.MessageActionHistoryClear{},
	}, entities)
	if err != nil {
		t.Fatal(err)
	}
	if serviceItem.MessageKind != "service" || !strings.Contains(serviceItem.Text, "HistoryClear") {
		t.Fatalf("service item = %#v", serviceItem)
	}
}

func TestUserChatTitleHidesUnavailableIDs(t *testing.T) {
	tests := []struct {
		name string
		user *tg.User
		want string
	}{
		{
			name: "restricted bot",
			user: &tg.User{ID: 8031847458, Bot: true, Restricted: true},
			want: "受限机器人",
		},
		{
			name: "unnamed bot",
			user: &tg.User{ID: 42, Bot: true},
			want: "机器人",
		},
		{
			name: "deleted account",
			user: &tg.User{ID: 42, Deleted: true},
			want: "已删除账号",
		},
		{
			name: "unnamed user",
			user: &tg.User{ID: 42},
			want: "私聊",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := userChatTitle(tt.user); got != tt.want {
				t.Fatalf("userChatTitle() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestForwardSourcesUseSafeUserTitle(t *testing.T) {
	user := &tg.User{ID: 8031847458, Bot: true, Restricted: true}
	entities := tgpeer.NewEntities(map[int64]*tg.User{user.ID: user}, nil, nil)
	header := &tg.MessageFwdHeader{}
	header.SetFromID(&tg.PeerUser{UserID: user.ID})
	header.SetSavedFromPeer(&tg.PeerUser{UserID: user.ID})
	msg := &tg.Message{}
	msg.SetFwdFrom(*header)

	forwarded, saved := forwardSources(msg, entities)
	if forwarded != "受限机器人" || saved != "受限机器人" {
		t.Fatalf("forward sources = %q, %q", forwarded, saved)
	}
}

func TestSavedMessagesResultKeepsEntities(t *testing.T) {
	result := &tg.MessagesMessagesSlice{
		Messages: []tg.MessageClass{
			&tg.Message{
				ID:     12,
				PeerID: &tg.PeerUser{UserID: 7},
			},
		},
		Users: []tg.UserClass{
			&tg.User{ID: 7, FirstName: "Bot"},
		},
	}

	messages, entities, err := savedMessagesResult(result)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].GetID() != 12 {
		t.Fatalf("saved messages = %#v", messages)
	}
	user, ok := entities.User(7)
	if !ok || user.FirstName != "Bot" {
		t.Fatalf("saved entities = %#v, ok=%v", user, ok)
	}
}

func TestChatMessagesResultSupportsChannels(t *testing.T) {
	result := &tg.MessagesChannelMessages{
		Messages: []tg.MessageClass{
			&tg.Message{ID: 12, PeerID: &tg.PeerChannel{ChannelID: 7}},
		},
		Chats: []tg.ChatClass{
			&tg.Channel{ID: 7, Title: "Channel"},
		},
	}

	messages, entities, err := chatMessagesResult(result)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].GetID() != 12 {
		t.Fatalf("channel messages = %#v", messages)
	}
	channel, ok := entities.Channel(7)
	if !ok || channel.Title != "Channel" {
		t.Fatalf("channel entity = %#v, ok=%v", channel, ok)
	}
}

func TestSavedDialogInputPeerSelf(t *testing.T) {
	peer, err := savedDialogInputPeer(&tg.PeerUser{UserID: 99}, tgpeer.NewEntities(nil, nil, nil), 99)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := peer.(*tg.InputPeerSelf); !ok {
		t.Fatalf("saved self peer = %T", peer)
	}
}

func TestChatItemUsesSavedPeerSource(t *testing.T) {
	s := &Server{opts: Options{Dir: t.TempDir()}, selfID: 99}
	tpl := template.Must(template.New("name").Parse(`{{.DialogID}}_{{.MessageID}}_{{.FileName}}`))
	source := &tg.User{ID: 7, FirstName: "Source"}
	entities := tgpeer.NewEntities(map[int64]*tg.User{source.ID: source}, nil, nil)
	msg := &tg.Message{ID: 12, PeerID: &tg.PeerUser{UserID: 99}, Message: "saved"}
	msg.SetSavedPeerID(&tg.PeerUser{UserID: source.ID})

	item, err := s.chatItem(tpl, chatSaved, msg, entities)
	if err != nil {
		t.Fatal(err)
	}
	if item.SavedFrom != "Source" {
		t.Fatalf("saved source = %q", item.SavedFrom)
	}
	if _, ok := item.savedPeer.(*tg.InputPeerUser); !ok {
		t.Fatalf("saved peer = %T", item.savedPeer)
	}
}

func TestSortMessageRangesNewestFirst(t *testing.T) {
	ranges := []tg.MessageRange{
		{MinID: 1, MaxID: 10},
		{MinID: 11, MaxID: 20},
		{MinID: 21, MaxID: 30},
	}
	sortMessageRanges(ranges)
	if ranges[0].MaxID != 30 || ranges[2].MaxID != 10 {
		t.Fatalf("sorted ranges = %#v", ranges)
	}
}
