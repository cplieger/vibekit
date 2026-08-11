package command

import (
	"context"
	"encoding/base64"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/cplieger/vibekit/internal/api"
)

// documentExts maps a file extension to the MIME type vibekit sends when
// inlining an attachment as an ACP embedded `resource` block.
//
// It MUST stay a subset of KAS's SUPPORTED_DOCUMENT_MIME_TYPES
// (src/document-types.ts in the @kiro/agent bundle). KAS's
// extractContentFromPrompt keeps an inline blob resource ONLY when its MIME
// is in that allowlist, with no else branch — so any entry here that KAS
// does not accept is read, base64'd, transmitted, then SILENTLY DROPPED
// (the agent never sees the file and no error surfaces). The removed office
// formats (.pptx/.ppt/.rtf/.odt/.ods/.odp) live in unsupportedDocExts and
// route through the path-reference branch instead. TestDocumentExtsSubsetOfKAS
// pins the subset so future drift fails CI.
//
// KAS's allowlist also covers html/plain/markdown, but those plain-text
// formats are deliberately omitted here: they route through the
// path-reference branch so the agent reads them in context with its file
// tools rather than as an opaque base64 blob.
var documentExts = map[string]string{
	".pdf":  "application/pdf",
	".csv":  "text/csv",
	".doc":  "application/msword",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".xls":  "application/vnd.ms-excel",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
}

// unsupportedDocExts are binary document formats KAS's inline-document
// allowlist (documentExts) does NOT accept. They cannot be sent as embedded
// `resource` blocks — KAS silently drops a resource with an unsupported MIME
// — so they route through the path-reference branch with a note that the
// binary format may not be readable as text, instead of vanishing.
var unsupportedDocExts = map[string]bool{
	".pptx": true,
	".ppt":  true,
	".rtf":  true,
	".odt":  true,
	".ods":  true,
	".odp":  true,
}

// MaxDocumentBytes caps the size of ONE document attachment (10 MiB).
const MaxDocumentBytes = 10 * 1024 * 1024

// MaxInlineTurnBytes caps the total document bytes one prompt may inline.
//
// A per-attachment cap is not enough, and the reason is a property of the wire
// rather than of vibekit: kiro-cli REPLAYS the full conversation history on every
// turn, so an inlined document does not cost its bytes once, it costs them for
// the life of the session. Ten near-limit spreadsheets pass the per-file check
// individually and put ~100 MiB of file (~133 MiB after base64, which inflates by
// 4/3) at a fixed history index that every later turn carries again. That is the
// same permanent-wedge shape upstream capped image dimensions for, reached by a
// different door.
//
// 20 MiB is two full-size documents, which covers the real "compare these two
// spreadsheets" case while refusing the set that wedges a session. It bounds the
// FILE bytes rather than the encoded bytes because that is the number a user can
// predict from a file listing; the encoded size is 4/3 of it by construction.
const MaxInlineTurnBytes = 20 * 1024 * 1024

// BuildPromptBlocks constructs the ACP prompt content array: a leading text
// block followed by one block per attachment.
//
// On v3 (KAS) the agent always advertises
// promptCapabilities.embeddedContext:true, so a supported document type
// (documentExts) is always inlined as an ACP embedded `resource` content
// block; everything else becomes a text path reference the agent reads with
// its file tools.
func BuildPromptBlocks(ctx context.Context, text string, attachments []api.Attachment, resolve func(string) (string, error)) []map[string]any {
	blocks := []map[string]any{api.TextBlock(text)}
	// budget is the turn's remaining inline allowance, spent by each document
	// actually read. It is threaded rather than global because it describes THIS
	// prompt: the next turn gets a fresh one, and the history cost of what was
	// already sent is not something a later prompt can refund.
	budget := MaxInlineTurnBytes
	for _, att := range attachments {
		if ctx.Err() != nil {
			return blocks
		}
		block, spent := attachmentBlock(att, resolve, budget)
		budget -= spent
		blocks = append(blocks, block)
	}
	return blocks
}

// attachmentBlock builds the single ACP content block for one attachment. A
// supported document type (documentExts) is inlined as an embedded `resource`
// block; everything else becomes a text path reference the agent reads with
// its file tools.
// The second return value is the file bytes this block consumed from the turn's
// inline budget: zero for every path-reference block, and the document's size for
// one that was inlined.
func attachmentBlock(att api.Attachment, resolve func(string) (string, error), budget int) (block map[string]any, spentBytes int) {
	displayName := filepath.Base(att.Path)
	ext := strings.ToLower(filepath.Ext(att.Path))
	mime, isDoc := documentExts[ext]

	// Inline a supported document as an embedded resource block.
	if isDoc {
		return inlineResourceBlock(att, displayName, mime, resolve, budget)
	}

	// Path-reference branch: validate containment, then hand the agent the
	// path to read with its file tools. Used for code/text files, and for
	// binary document formats KAS does not accept as inline resources
	// (unsupportedDocExts) — which would otherwise be silently dropped as a
	// resource block. The note stops the agent from silently doing nothing
	// with a binary format it can't read as text.
	if _, err := resolve(att.Path); err != nil {
		slog.Warn("attachment: path escapes workspace",
			"path", displayName, keyError, err)
		return api.TextBlock("Attached file (invalid path): " + displayName), 0
	}
	if unsupportedDocExts[ext] {
		return api.TextBlock("Attached file: " + att.Path +
			" (binary document — read it with your file tools; this format may not be readable as text)"), 0
	}
	return api.TextBlock("Attached file: " + att.Path), 0
}

// inlineResourceBlock reads a document attachment from disk and returns an
// ACP embedded `resource` content block (v3/KAS: the content-block union
// has no `document` type — it is text | image | audio | resource_link |
// resource). The document rides the blob variant of EmbeddedResourceResource
// (uri + mimeType + base64 blob). On any failure (path escape, stat/read
// error, or oversize) it returns a descriptive text block instead so the
// agent is never left silently dropping the attachment.
func inlineResourceBlock(att api.Attachment, displayName, mime string, resolve func(string) (string, error), budget int) (block map[string]any, spentBytes int) {
	abs, err := resolve(att.Path)
	if err != nil {
		slog.Warn("attachment: path escapes workspace",
			"path", displayName, keyError, err)
		return api.TextBlock("Attached file (invalid path): " + displayName), 0
	}
	info, err := os.Stat(abs)
	if err != nil {
		slog.Warn("attachment: stat failed", "path", displayName, keyError, err)
		return api.TextBlock("Attached file (unreadable): " + displayName), 0
	}
	if info.Size() > MaxDocumentBytes {
		slog.Warn("attachment: too large",
			"path", displayName, "size", info.Size())
		return api.TextBlock("Attached file (too large for inline): " + displayName), 0
	}
	// The turn's cumulative allowance, checked BEFORE the read so an over-budget
	// set costs no allocation. Degrading to a path reference is the same
	// fail-closed direction every other branch here takes: the agent still learns
	// the file exists and can open it with its file tools, which is strictly
	// better than a session wedged by history it cannot drop.
	if int(info.Size()) > budget {
		slog.Warn("attachment: turn inline budget exhausted, sending a path reference",
			"path", displayName, "size", info.Size(), "remaining", budget)
		return api.TextBlock("Attached file: " + att.Path +
			" (not inlined: this turn's attachment budget is spent — read it with your file tools)"), 0
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		slog.Warn("attachment: read failed",
			"path", displayName, keyError, err)
		return api.TextBlock("Attached file (unreadable): " + displayName), 0
	}
	return map[string]any{
		keyType: "resource",
		"resource": map[string]any{
			"uri":      "file://" + abs,
			"mimeType": mime,
			"blob":     base64.StdEncoding.EncodeToString(data),
		},
	}, len(data)
}
