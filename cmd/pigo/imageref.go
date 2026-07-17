// This file implements the local-image input syntax for prompts (US-010/#126).
// A prompt may reference local image files with either `@image:<path>` or the
// Markdown image form `![alt](<path>)`. Each reference is read from disk,
// base64-encoded, and attached to the user message as an agentcore.ImageContent
// block so a multimodal model can see it. The remaining (non-reference) text is
// kept as a TextContent block. References that cannot be read are reported as an
// error rather than silently dropped, so the user knows the image was not sent.
package main

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/smallnest/pigo/internal/agentcore"
)

// imageRefPattern matches the two supported image-reference syntaxes:
//   - @image:<path>              (path runs to the next whitespace)
//   - ![alt](<path>)             (Markdown image; alt text is ignored)
//
// The path in the Markdown form may contain spaces; the @image form may not
// (whitespace terminates it), matching the convention that @image is a bare
// token while the Markdown form is explicitly delimited by parentheses.
var imageRefPattern = regexp.MustCompile(`!\[[^\]]*\]\(([^)]+)\)|@image:(\S+)`)

// buildUserContent turns a raw prompt line into a content list. When the prompt
// contains no image references it returns a single TextContent (the common
// case). Otherwise it returns the interleaved text and image blocks, reading and
// base64-encoding each referenced file. It returns an error if any referenced
// file cannot be read, so the caller can surface it instead of sending a prompt
// that silently omits the image.
func buildUserContent(prompt string) (agentcore.ContentList, error) {
	locs := imageRefPattern.FindAllStringSubmatchIndex(prompt, -1)
	if len(locs) == 0 {
		return agentcore.ContentList{agentcore.NewTextContent(prompt)}, nil
	}

	var content agentcore.ContentList
	// addText appends a text block for the given [lo,hi) slice of prompt,
	// trimming surrounding whitespace and skipping empties so we don't emit
	// blank text blocks around the references.
	addText := func(lo, hi int) {
		if lo >= hi {
			return
		}
		if t := strings.TrimSpace(prompt[lo:hi]); t != "" {
			content = append(content, agentcore.NewTextContent(t))
		}
	}

	prev := 0
	for _, loc := range locs {
		start, end := loc[0], loc[1]
		addText(prev, start)
		// Group 1 is the Markdown path; group 2 is the @image path. Exactly one
		// is set per match.
		var path string
		if loc[2] >= 0 {
			path = prompt[loc[2]:loc[3]]
		} else if loc[4] >= 0 {
			path = prompt[loc[4]:loc[5]]
		}
		img, err := loadImageContent(strings.TrimSpace(path))
		if err != nil {
			return nil, err
		}
		content = append(content, img)
		prev = end
	}
	addText(prev, len(prompt))

	if len(content) == 0 {
		content = agentcore.ContentList{agentcore.NewTextContent("")}
	}
	return content, nil
}

// loadImageContent reads an image file and returns an ImageContent with the
// data base64-encoded and the mime type sniffed from the file extension (with a
// content-sniff fallback). It errors if the file cannot be read or is not a
// recognizable image type.
func loadImageContent(path string) (agentcore.ImageContent, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return agentcore.ImageContent{}, fmt.Errorf("read image %q: %w", path, err)
	}
	mime := mimeFromPath(path)
	if mime == "" {
		// Fall back to content sniffing when the extension is unknown.
		mime = http.DetectContentType(data)
	}
	if !strings.HasPrefix(mime, "image/") {
		return agentcore.ImageContent{}, fmt.Errorf("%q is not a recognized image (detected %q)", path, mime)
	}
	enc := base64.StdEncoding.EncodeToString(data)
	return agentcore.NewImageContent(enc, mime), nil
}

// mimeFromPath maps a file extension to an image mime type. It returns "" for
// unknown extensions so the caller can fall back to content sniffing.
func mimeFromPath(path string) string {
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".png"):
		return "image/png"
	case strings.HasSuffix(lower, ".jpg"), strings.HasSuffix(lower, ".jpeg"):
		return "image/jpeg"
	case strings.HasSuffix(lower, ".gif"):
		return "image/gif"
	case strings.HasSuffix(lower, ".webp"):
		return "image/webp"
	default:
		return ""
	}
}
