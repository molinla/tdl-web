package tmessage

import (
	"github.com/gotd/td/tg"
)

type Dialog struct {
	Peer     tg.InputPeerClass
	Messages []int
	// Dates maps message ID -> unix seconds from export JSON (date_unixtime).
	// May be nil or incomplete when the field is missing.
	Dates map[int]int64
}

type ParseSource func() ([]*Dialog, error)

func Parse(src ParseSource) ([]*Dialog, error) {
	return src()
}
