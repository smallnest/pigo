// Package agentcore defines the core "leaf" data types and control flow for the
// pigo agent harness, a Go reimplementation of the pi agent loop. It is the
// foundation package that every other agent sub-package depends on and imports
// nothing from them.
//
// This file defines the Content model: a sealed interface implemented by the
// four content block kinds (text, thinking, toolCall, image). Because Go's
// encoding/json cannot dispatch to an interface based on a discriminant field,
// containers holding []Content implement custom UnmarshalJSON that peeks at the
// "type" field and decodes into the concrete struct. Mirrors pi's discriminated
// union (packages/ai/src/types.ts) as interface + type switch.
package agentcore

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Content is a sealed interface implemented by every content block kind.
// Consumers dispatch with a type switch. The interface is sealed via the
// unexported isContent marker so no type outside this package can satisfy it.
type Content interface {
	isContent()
}

// Content type discriminants, matching pi's wire format.
const (
	ContentTypeText     = "text"
	ContentTypeThinking = "thinking"
	ContentTypeToolCall = "toolCall"
	ContentTypeImage    = "image"
)

// TextContent is a plain text block.
type TextContent struct {
	Type          string `json:"type"`
	Text          string `json:"text"`
	TextSignature string `json:"textSignature,omitempty"`
}

// ThinkingContent is a reasoning/thinking block. Never folded into text.
type ThinkingContent struct {
	Type              string `json:"type"`
	Thinking          string `json:"thinking"`
	ThinkingSignature string `json:"thinkingSignature,omitempty"`
	Redacted          bool   `json:"redacted,omitempty"`
}

// ToolCallContent is a request from the model to invoke a tool. Arguments are
// kept as raw JSON so validation (JSON Schema) and shaping happen downstream.
type ToolCallContent struct {
	Type             string          `json:"type"`
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	Arguments        json.RawMessage `json:"arguments"`
	ThoughtSignature string          `json:"thoughtSignature,omitempty"`
}

// ImageContent is an image block (base64 data + mime type).
type ImageContent struct {
	Type     string `json:"type"`
	Data     string `json:"data"`
	MimeType string `json:"mimeType"`
}

func (TextContent) isContent()     {}
func (ThinkingContent) isContent() {}
func (ToolCallContent) isContent() {}
func (ImageContent) isContent()    {}

// MarshalJSON encodes a ToolCallContent, tolerating malformed Arguments. A
// model can stream syntactically invalid tool-call JSON (a truncated or
// duplicated key, e.g. `{"todos": []{}...`); such bytes are kept verbatim in
// Arguments so schema validation can report "not valid JSON" to the model, but
// json.RawMessage.MarshalJSON rejects them, which would otherwise abort every
// downstream serialization (session persistence, provider re-serialization) and
// take the whole turn down. To keep those paths crash-free we emit invalid
// arguments as a JSON string of the raw bytes: valid JSON that round-trips the
// original text. Well-formed arguments are emitted unchanged.
func (t ToolCallContent) MarshalJSON() ([]byte, error) {
	args := t.Arguments
	if len(bytes.TrimSpace(args)) == 0 {
		args = json.RawMessage("{}")
	} else if !json.Valid(args) {
		s, err := json.Marshal(string(args))
		if err != nil {
			return nil, fmt.Errorf("content: encode invalid tool arguments: %w", err)
		}
		args = s
	}
	// A named alias avoids recursing into this MarshalJSON.
	type wire struct {
		Type             string          `json:"type"`
		ID               string          `json:"id"`
		Name             string          `json:"name"`
		Arguments        json.RawMessage `json:"arguments"`
		ThoughtSignature string          `json:"thoughtSignature,omitempty"`
	}
	return json.Marshal(wire{
		Type:             t.Type,
		ID:               t.ID,
		Name:             t.Name,
		Arguments:        args,
		ThoughtSignature: t.ThoughtSignature,
	})
}

// Constructors set the Type discriminant so callers never desync it.

// NewTextContent returns a TextContent with the type discriminant set.
func NewTextContent(text string) TextContent {
	return TextContent{Type: ContentTypeText, Text: text}
}

// NewThinkingContent returns a ThinkingContent with the type discriminant set.
func NewThinkingContent(thinking string) ThinkingContent {
	return ThinkingContent{Type: ContentTypeThinking, Thinking: thinking}
}

// NewToolCallContent returns a ToolCallContent with the type discriminant set.
func NewToolCallContent(id, name string, arguments json.RawMessage) ToolCallContent {
	return ToolCallContent{Type: ContentTypeToolCall, ID: id, Name: name, Arguments: arguments}
}

// NewImageContent returns an ImageContent with the type discriminant set.
func NewImageContent(data, mimeType string) ImageContent {
	return ImageContent{Type: ContentTypeImage, Data: data, MimeType: mimeType}
}

// decodeContent peeks at the "type" field of a JSON object and decodes it into
// the matching concrete Content struct. This is the single dispatch point used
// by every container that holds Content (messages, tool results, session
// entries, provider parsing).
func decodeContent(raw json.RawMessage) (Content, error) {
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("content: peek type: %w", err)
	}
	switch probe.Type {
	case ContentTypeText:
		var c TextContent
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, err
		}
		return c, nil
	case ContentTypeThinking:
		var c ThinkingContent
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, err
		}
		return c, nil
	case ContentTypeToolCall:
		var c ToolCallContent
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, err
		}
		return c, nil
	case ContentTypeImage:
		var c ImageContent
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, err
		}
		return c, nil
	case "":
		return nil, fmt.Errorf("content: missing type discriminant")
	default:
		return nil, fmt.Errorf("content: unknown type %q", probe.Type)
	}
}

// ContentList is a slice of Content with discriminated JSON (un)marshalling.
// Fields typed []Content in messages use this so decoding dispatches on "type".
type ContentList []Content

// UnmarshalJSON decodes a JSON array of content blocks, dispatching each element
// on its "type" discriminant.
func (cl *ContentList) UnmarshalJSON(data []byte) error {
	var raws []json.RawMessage
	if err := json.Unmarshal(data, &raws); err != nil {
		return err
	}
	out := make(ContentList, 0, len(raws))
	for i, raw := range raws {
		c, err := decodeContent(raw)
		if err != nil {
			return fmt.Errorf("content[%d]: %w", i, err)
		}
		out = append(out, c)
	}
	*cl = out
	return nil
}
