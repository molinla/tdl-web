package cmd

import (
	"context"
	"errors"
	"testing"

	"github.com/iyear/tdl/core/storage"
)

type webMemoryStorage map[string][]byte

func (s webMemoryStorage) Get(_ context.Context, key string) ([]byte, error) {
	value, ok := s[key]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return append([]byte(nil), value...), nil
}

func (s webMemoryStorage) Set(_ context.Context, key string, value []byte) error {
	s[key] = append([]byte(nil), value...)
	return nil
}

func (s webMemoryStorage) Delete(_ context.Context, key string) error {
	delete(s, key)
	return nil
}

func TestNewWebSessionKeepsCurrentSession(t *testing.T) {
	ctx := context.Background()
	kvd := webMemoryStorage{}
	current := storage.NewSession(kvd, false)
	if err := current.StoreSession(ctx, []byte("current")); err != nil {
		t.Fatal(err)
	}

	key, err := newWebSession(ctx, kvd, "")
	if err != nil {
		t.Fatal(err)
	}
	if key == "" {
		t.Fatal("new web session key is empty")
	}
	active, err := currentWebSession(ctx, kvd)
	if err != nil || active != key {
		t.Fatalf("active session = %q, %v; want %q", active, err, key)
	}

	next := storage.NewSessionWithKey(kvd, false, key)
	if err := next.StoreSession(ctx, []byte("next")); err != nil {
		t.Fatal(err)
	}
	got, err := current.LoadSession(ctx)
	if err != nil || string(got) != "current" {
		t.Fatalf("current session = %q, %v", got, err)
	}
	back, err := usePreviousWebSession(ctx, kvd, key)
	if err != nil || back != "" {
		t.Fatalf("previous session = %q, %v", back, err)
	}
	previous, ok, err := previousWebSession(ctx, kvd)
	if err != nil || !ok || previous != key {
		t.Fatalf("saved previous = %q, %v, %v", previous, ok, err)
	}
}

func TestCancelWebSessionRestoresCurrentAccount(t *testing.T) {
	ctx := context.Background()
	kvd := webMemoryStorage{}
	current := storage.NewSession(kvd, false)
	if err := current.StoreSession(ctx, []byte("current")); err != nil {
		t.Fatal(err)
	}
	pending, err := newWebSession(ctx, kvd, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.NewSessionWithKey(kvd, false, pending).StoreSession(ctx, []byte("pending")); err != nil {
		t.Fatal(err)
	}

	restored, err := cancelWebSession(ctx, kvd, pending)
	if err != nil || restored != "" {
		t.Fatalf("restored session = %q, %v", restored, err)
	}
	if active, err := currentWebSession(ctx, kvd); err != nil || active != "" {
		t.Fatalf("active session = %q, %v", active, err)
	}
	if _, ok, err := previousWebSession(ctx, kvd); err != nil || ok {
		t.Fatalf("previous session still exists: ok=%v, err=%v", ok, err)
	}
	if _, err := kvd.Get(ctx, pending); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("pending session was not deleted: %v", err)
	}
	got, err := current.LoadSession(ctx)
	if err != nil || string(got) != "current" {
		t.Fatalf("current session = %q, %v", got, err)
	}
}
