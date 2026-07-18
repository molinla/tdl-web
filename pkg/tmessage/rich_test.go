package tmessage

import (
	"strings"
	"testing"
)

func TestClassifyKind(t *testing.T) {
	if g := classifyKind("video_file", "video/mp4", "a.mp4", false); g != "video" {
		t.Fatalf("got %s", g)
	}
	if g := classifyKind("", "image/jpeg", "", true); g != "image" {
		t.Fatalf("got %s", g)
	}
	if g := classifyKind("", "application/zip", "a.zip", false); g != "file" {
		t.Fatalf("got %s", g)
	}
}

func TestPickFileName(t *testing.T) {
	name := pickFileName(richMessage{
		ID:       1,
		File:     "(File exceeds maximum size. Change data exporting settings to download.)",
		FileName: `path\foo.mp4`,
		MimeType: "video/mp4",
	})
	if name != "foo.mp4" {
		t.Fatalf("got %q", name)
	}
	name = pickFileName(richMessage{ID: 9, MimeType: "video/mp4"})
	if !strings.HasSuffix(name, ".mp4") {
		t.Fatalf("got %q", name)
	}
}

func TestTextAsStringFlattensTelegramExportEntities(t *testing.T) {
	got := textAsString([]interface{}{
		"hello ",
		map[string]interface{}{"type": "bold", "text": "world"},
		"\n",
		map[string]interface{}{"type": "text_link", "text": "link"},
	})
	if got != "hello world\nlink" {
		t.Fatalf("got %q", got)
	}
}
