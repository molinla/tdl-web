package web

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMetaCacheRejectsIncomplete(t *testing.T) {
	dir := t.TempDir()
	s := &Server{
		opts: Options{
			CacheDir: dir,
			Dir:      filepath.Join(dir, "dl"),
			Template: "t",
		},
	}
	fp := "abc123"
	path := s.metaCachePath(fp)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// Legacy incomplete cache (no expected_total / wrong count).
	bad := metaCacheFile{
		Version:     1,
		Fingerprint: fp,
		Template:    "t",
		Items:       make([]metaCacheItem, 200),
	}
	for i := range bad.Items {
		bad.Items[i] = metaCacheItem{
			ID:         "id",
			MessageID:  i + 1,
			LogicalPos: i,
			Name:       "f.bin",
			Type:       mediaFile,
			Size:       1,
		}
	}
	b, _ := json.Marshal(bad)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.loadMetaCache(fp, 5000); ok {
		t.Fatal("expected incomplete cache to be rejected")
	}

	good := metaCacheFile{
		Version:       metaCacheVersion,
		Fingerprint:   fp,
		Template:      "t",
		ExpectedTotal: 2,
		Items: []metaCacheItem{
			{ID: "a", MessageID: 1, LogicalPos: 0, Name: "a.bin", Type: mediaFile, Size: 1},
			{ID: "b", MessageID: 2, LogicalPos: 1, Name: "b.bin", Type: mediaFile, Size: 2},
		},
	}
	b, _ = json.Marshal(good)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	items, ok := s.loadMetaCache(fp, 2)
	if !ok || len(items) != 2 {
		t.Fatalf("got ok=%v len=%d", ok, len(items))
	}
}

func TestSaveMetaCacheSkipsWhileImporting(t *testing.T) {
	dir := t.TempDir()
	s := &Server{
		opts: Options{
			CacheDir: dir,
			Dir:      filepath.Join(dir, "dl"),
			Template: "t",
		},
		fingerprint: "fp",
		importing:   true,
		importTotal: 100,
		items:       map[string]*Item{"1": {ID: "1", Name: "a", TargetPath: filepath.Join(dir, "dl", "a")}},
		order:       []string{"1"},
	}
	if err := s.saveMetaCache(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.metaCachePath("fp")); !os.IsNotExist(err) {
		t.Fatalf("cache should not be written while importing, err=%v", err)
	}
}
