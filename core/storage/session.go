package storage

import (
	"context"
	"errors"

	"github.com/gotd/td/telegram"

	"github.com/iyear/tdl/core/storage/keygen"
)

type Session struct {
	kv         Storage
	login      bool
	storageKey string
}

func NewSession(kv Storage, login bool) telegram.SessionStorage {
	return &Session{kv: kv, login: login, storageKey: keygen.New("session")}
}

// NewSessionWithKey keeps an additional Telegram session in the same namespace.
func NewSessionWithKey(kv Storage, login bool, key string) telegram.SessionStorage {
	if key == "" {
		return NewSession(kv, login)
	}
	return &Session{kv: kv, login: login, storageKey: key}
}

func (s *Session) LoadSession(ctx context.Context) ([]byte, error) {
	if s.login {
		return nil, nil
	}

	b, err := s.kv.Get(ctx, s.storageKey)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return b, nil
}

func (s *Session) StoreSession(ctx context.Context, data []byte) error {
	return s.kv.Set(ctx, s.storageKey, data)
}
