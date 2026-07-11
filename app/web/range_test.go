package web

import (
	"math"
	"testing"

	"github.com/gotd/td/tg"

	"github.com/iyear/tdl/pkg/tmessage"
)

func TestApplyDialogRangeByID(t *testing.T) {
	in := [][]*tmessage.Dialog{{
		{
			Peer:     &tg.InputPeerChannel{ChannelID: 1, AccessHash: 1},
			Messages: []int{10, 20, 30, 40},
		},
	}}
	out := applyDialogRange(in, RangeTypeID, 20, 30)
	if len(out) != 1 || len(out[0]) != 1 {
		t.Fatalf("unexpected shape: %+v", out)
	}
	got := out[0][0].Messages
	if len(got) != 2 || got[0] != 20 || got[1] != 30 {
		t.Fatalf("got %v", got)
	}
}

func TestApplyDialogRangeByTime(t *testing.T) {
	in := [][]*tmessage.Dialog{{
		{
			Peer:     &tg.InputPeerChannel{ChannelID: 1, AccessHash: 1},
			Messages: []int{1, 2, 3},
			Dates: map[int]int64{
				1: 100,
				2: 200,
				3: 300,
			},
		},
	}}
	out := applyDialogRange(in, RangeTypeTime, 150, 250)
	got := out[0][0].Messages
	if len(got) != 1 || got[0] != 2 {
		t.Fatalf("got %v want [2]", got)
	}
}

func TestParseRangeForm(t *testing.T) {
	typ, from, to, err := parseRangeForm("id", "5", "10")
	if err != nil || typ != RangeTypeID || from != 5 || to != 10 {
		t.Fatalf("%s %d %d %v", typ, from, to, err)
	}
	typ, from, to, err = parseRangeForm("time", "", "")
	if err != nil || typ != RangeTypeTime || from != 0 || to != math.MaxInt {
		t.Fatalf("%s %d %d %v", typ, from, to, err)
	}
}
