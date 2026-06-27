package command

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/cplieger/vibekit/internal/api"
)

// documentExts are file extensions sent as ACP document content blocks.
var documentExts = map[string]string{
	".pdf":  "application/pdf",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".doc":  "application/msword",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".xls":  "application/vnd.ms-excel",
	".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	".ppt":  "application/vnd.ms-powerpoint",
	".rtf":  "application/rtf",
	".odt":  "application/vnd.oasis.opendocument.text",
	".ods":  "application/vnd.oasis.opendocument.spreadsheet",
	".odp":  "application/vnd.oasis.opendocument.presentation",
	".csv":  "text/csv",
}

// MaxDocumentBytes caps the size of a document attachment (10 MiB).
const MaxDocumentBytes = 10 * 1024 * 1024

// BuildPromptBlocks constructs the ACP prompt content array.
//
// supportsDocuments comes from the live ACP handshake
// (ACPBridge.SupportsDocuments, i.e. promptCapabilities.embeddedContext).
// When the agent can't consume inline document/embedded blocks (kiro-cli
// 2.7's `acp` advertises embeddedContext:false), document attachments fall
// back to a path-reference text block the agent reads with its file tools,
// instead of a `document` block the agent never sees.
func BuildPromptBlocks(ctx context.Context, text string, attachments []api.Attachment, resolve func(string) (string, error), supportsDocuments bool) []map[string]any {
	blocks := []map[string]any{api.TextBlock(text)}
	for _, att := range attachments {
		if ctx.Err() != nil {
			return blocks
		}
		blocks = append(blocks, attachmentBlock(att, resolve, supportsDocuments))
	}
	return blocks
}

// attachmentBlock builds the single ACP content block for one attachment.
// Document types are inlined only when the agent advertises support;
// everything else (and every document when support is absent) becomes a
// text path reference the agent reads with its file tools.
func attachmentBlock(att api.Attachment, resolve func(string) (string, error), supportsDocuments bool) map[string]any {
	displayName := filepath.Base(att.Path)
	ext := strings.ToLower(filepath.Ext(att.Path))
	mime, isDoc := documentExts[ext]

	// Inline a binary document only when the agent advertises support;
	// otherwise it is silently dropped (see acpSupportsDocumentBlocks).
	if isDoc && supportsDocuments {
		return inlineDocumentBlock(att, displayName, mime, resolve)
	}

	// Path-reference fallback: validate containment, then hand the agent
	// the path to read with its file tools. Used for code/text files
	// always, and for documents the agent can't inline. A binary document
	// surfaced this way may not be readable as text; the note stops the
	// agent from silently doing nothing with it.
	if _, err := resolve(att.Path); err != nil {
		slog.Warn("attachment: path escapes workspace",
			"path", displayName, keyError, err)
		return api.TextBlock("Attached file (invalid path): " + displayName)
	}
	if isDoc {
		return api.TextBlock("Attached file: " + att.Path +
			" (document — read it with your file tools; binary formats may not be readable as text)")
	}
	return api.TextBlock("Attached file: " + att.Path)
}

// inlineDocumentBlock reads a document attachment from disk and returns an
// ACP document content block. On any failure (path escape, stat/read error,
// or oversize) it returns a descriptive text block instead so the agent is
// never left silently dropping the attachment.
func inlineDocumentBlock(att api.Attachment, displayName, mime string, resolve func(string) (string, error)) map[string]any {
	abs, err := resolve(att.Path)
	if err != nil {
		slog.Warn("attachment: path escapes workspace",
			"path", displayName, keyError, err)
		return api.TextBlock("Attached file (invalid path): " + displayName)
	}
	info, err := os.Stat(abs)
	if err != nil {
		slog.Warn("attachment: stat failed", "path", displayName, keyError, err)
		return api.TextBlock("Attached file (unreadable): " + displayName)
	}
	if info.Size() > MaxDocumentBytes {
		slog.Warn("attachment: too large",
			"path", displayName, "size", info.Size())
		return api.TextBlock("Attached file (too large for inline): " + displayName)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		slog.Warn("attachment: read failed",
			"path", displayName, keyError, err)
		return api.TextBlock("Attached file (unreadable): " + displayName)
	}
	return map[string]any{
		keyType:    "document",
		keyName:    att.Name,
		"content":  data,
		"mimeType": mime,
	}
}
