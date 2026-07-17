package main

// Tests for the prompt image-reference syntax (US-010, #126): @image:<path> and
// the Markdown ![alt](<path>) form. They write a tiny real PNG to a temp file so
// loadImageContent exercises the real read + base64 + mime path, and assert that
// a missing file is an error (not a silently dropped image).

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

// pngBytes is a minimal 1x1 PNG (valid signature + IHDR) so mime detection and
// the image/ prefix check pass without pulling in an image library.
var pngBytes = []byte{
	0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
	0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4,
	0x89,
}

func writeTempPNG(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "pixel.png")
	if err := os.WriteFile(path, pngBytes, 0o644); err != nil {
		t.Fatalf("write temp png: %v", err)
	}
	return path
}

// TestBuildUserContentNoImage: a plain prompt yields a single text block.
func TestBuildUserContentNoImage(t *testing.T) {
	content, err := buildUserContent("just some text")
	if err != nil {
		t.Fatalf("buildUserContent: %v", err)
	}
	if len(content) != 1 {
		t.Fatalf("content len = %d, want 1", len(content))
	}
	if tc, ok := content[0].(agentcore.TextContent); !ok || tc.Text != "just some text" {
		t.Errorf("content[0] = %#v, want text \"just some text\"", content[0])
	}
}

// TestBuildUserContentAtImageSyntax: @image:<path> attaches an ImageContent and
// keeps the surrounding text.
func TestBuildUserContentAtImageSyntax(t *testing.T) {
	path := writeTempPNG(t)
	content, err := buildUserContent("look at @image:" + path + " please")
	if err != nil {
		t.Fatalf("buildUserContent: %v", err)
	}
	assertTextThenImage(t, content, "look at", "image/png")
}

// TestBuildUserContentMarkdownSyntax: ![alt](path) attaches an ImageContent.
func TestBuildUserContentMarkdownSyntax(t *testing.T) {
	path := writeTempPNG(t)
	content, err := buildUserContent("before ![a pixel](" + path + ") after")
	if err != nil {
		t.Fatalf("buildUserContent: %v", err)
	}
	// text "before", image, text "after"
	if len(content) != 3 {
		t.Fatalf("content len = %d, want 3", len(content))
	}
	if _, ok := content[1].(agentcore.ImageContent); !ok {
		t.Errorf("content[1] = %T, want ImageContent", content[1])
	}
}

// TestBuildUserContentMissingFile: a missing referenced file is an error.
func TestBuildUserContentMissingFile(t *testing.T) {
	_, err := buildUserContent("@image:/no/such/file.png")
	if err == nil {
		t.Fatal("buildUserContent accepted a missing image file, want error")
	}
}

// TestLoadImageContentBase64: the encoded data must match the file bytes.
func TestLoadImageContentBase64(t *testing.T) {
	path := writeTempPNG(t)
	img, err := loadImageContent(path)
	if err != nil {
		t.Fatalf("loadImageContent: %v", err)
	}
	if img.MimeType != "image/png" {
		t.Errorf("mime = %q, want image/png", img.MimeType)
	}
	if want := base64.StdEncoding.EncodeToString(pngBytes); img.Data != want {
		t.Errorf("data mismatch")
	}
}

func assertTextThenImage(t *testing.T, content agentcore.ContentList, wantText, wantMime string) {
	t.Helper()
	var sawText, sawImage bool
	for _, c := range content {
		switch b := c.(type) {
		case agentcore.TextContent:
			if strings.Contains(b.Text, wantText) {
				sawText = true
			}
		case agentcore.ImageContent:
			if b.MimeType == wantMime {
				sawImage = true
			}
		}
	}
	if !sawText {
		t.Errorf("no text block containing %q in %#v", wantText, content)
	}
	if !sawImage {
		t.Errorf("no image block with mime %q in %#v", wantMime, content)
	}
}
