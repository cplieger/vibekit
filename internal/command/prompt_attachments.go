package command

import (
	"context"
	"encoding/base64"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// documentExts maps a file extension to the MIME type vibekit sends when
// inlining an attachment as an ACP embedded `resource` block.
//
// Must stay a subset of KAS's SUPPORTED_DOCUMENT_MIME_TYPES:
// extractContentFromPrompt keeps an inline blob resource only when its MIME
// is in that allowlist with no else branch, so an entry here KAS does not
// accept is read, base64'd, transmitted, then silently dropped.
// TestDocumentExtsSubsetOfKAS pins the subset.
//
// KAS's allowlist also covers html/plain/markdown; those are omitted here
// so the agent reads them in context with its file tools instead of as an
// opaque base64 blob.
var documentExts = map[string]string{
	".pdf":  "application/pdf",
	".csv":  "text/csv",
	".doc":  "application/msword",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".xls":  "application/vnd.ms-excel",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
}

// unsupportedDocExts are binary document formats KAS's inline-document
// allowlist does not accept, so they route through the path-reference
// branch instead of being silently dropped.
var unsupportedDocExts = map[string]bool{
	".pptx": true,
	".ppt":  true,
	".rtf":  true,
	".odt":  true,
	".ods":  true,
	".odp":  true,
}

// imageExts maps an image extension to the MIME type vibekit sends when
// inlining an attachment as an ACP `image` content block.
//
// KAS's extractContentFromPrompt pushes the image block unconditionally
// (no allowlist to stay a subset of, unlike the document branch). Mirrors
// KAS's own IMAGE_EXTENSIONS. Deliberately not svg or avif: KAS excludes
// both, and an SVG is XML a vision model cannot see as a picture. Do not
// unify with utils-url.ts's INLINE_IMAGE_EXT, which answers what a browser
// renders inline — a different consumer.
//
// A block carrying `uri` is not sent: toDataUrl returns the uri instead of
// the base64 data URL when present, which would hand the model a path it
// cannot fetch.
var imageExts = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
}

// MaxDocumentBytes caps the size of one document or image attachment
// (10 MiB), also KAS's own MAX_IMAGE_SIZE. Kept in file bytes: the number a
// user can predict from a file listing, and the cheap pre-read gate.
const MaxDocumentBytes = 10 * 1024 * 1024

// MaxInlineEncodedBytes caps the base64 payload of one inlined attachment.
// MaxDocumentBytes cannot be this gate: base64 inflates by 4/3, invisible
// to any check that measures file bytes.
//
// kiro-cli replays the full conversation history on every turn, so a
// payload the backend refuses sits at a fixed history index and re-sends
// every later turn, wedging the session permanently — only a rewind or a
// new chat escapes it.
//
// The number is inferred, not measured: 5 MiB is the figure KiroCrew read
// off its own backend's refusal text for an image over that size. Both
// products front the same vendor's catalogue, and erring low ships a
// smaller image while erring high wedges the session.
const MaxInlineEncodedBytes = 5 * 1024 * 1024

// MaxInlineTurnEncodedBytes caps the total encoded bytes one prompt may
// inline. A per-attachment cap is not enough: kiro-cli replays history on
// every turn, so several near-limit files individually under cap can still
// put a fixed history index's worth of payload into every later turn.
//
// Counted in encoded bytes since that is what the history carries.
const MaxInlineTurnEncodedBytes = 15 * 1024 * 1024

// BuildPromptBlocks constructs the ACP prompt content array: a leading text
// block followed by one block per attachment. On v3 (KAS) a supported
// document type is always inlined as an embedded `resource` block;
// everything else becomes a text path reference.
func BuildPromptBlocks(ctx context.Context, text string, attachments []vibekit.Attachment, resolve func(string) (string, error)) []map[string]any {
	blocks := []map[string]any{vibekit.TextBlock(text)}
	// budget is the turn's remaining inline allowance in encoded bytes,
	// spent by each attachment actually read — threaded rather than
	// global since it describes this prompt alone.
	budget := MaxInlineTurnEncodedBytes
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
// supported document type is inlined as an embedded `resource` block; an
// image as an `image` block; everything else becomes a path reference.
// Returns the encoded bytes this block consumed from the turn's inline
// budget (zero for a path-reference block).
func attachmentBlock(att vibekit.Attachment, resolve func(string) (string, error), budget int) (block map[string]any, spentBytes int) {
	displayName := filepath.Base(att.Path)
	ext := strings.ToLower(filepath.Ext(att.Path))

	if mime, isDoc := documentExts[ext]; isDoc {
		return inlineResourceBlock(att, displayName, mime, resolve, budget)
	}
	if mime, isImg := imageExts[ext]; isImg {
		return inlineImageBlock(att, displayName, mime, resolve, budget)
	}

	// Path-reference branch: validate containment, then hand the agent the
	// path to read with its file tools.
	if _, err := resolve(att.Path); err != nil {
		slog.Warn("attachment: path escapes workspace",
			"path", displayName, keyError, err)
		return vibekit.TextBlock("Attached file (invalid path): " + displayName), 0
	}
	if unsupportedDocExts[ext] {
		return vibekit.TextBlock("Attached file: " + att.Path +
			" (binary document — read it with your file tools; this format may not be readable as text)"), 0
	}
	return vibekit.TextBlock("Attached file: " + att.Path), 0
}

// inlineResourceBlock reads a document attachment from disk and returns an
// ACP embedded `resource` content block (v3/KAS has no `document` type; the
// document rides the blob variant of EmbeddedResourceResource). On any
// failure it returns a descriptive text block instead.
func inlineResourceBlock(att vibekit.Attachment, displayName, mime string, resolve func(string) (string, error), budget int) (block map[string]any, spentBytes int) {
	abs, data, fallback := readForInline(att, displayName, mime, resolve, budget)
	if fallback != nil {
		return fallback, 0
	}
	return map[string]any{
		keyType: "resource",
		"resource": map[string]any{
			"uri":      "file://" + abs,
			"mimeType": mime,
			"blob":     base64.StdEncoding.EncodeToString(data),
		},
	}, base64.StdEncoding.EncodedLen(len(data))
}

// inlineImageBlock reads an image attachment from disk and returns an ACP
// `image` content block. Failure degrades to a path reference — KAS has an
// image read tool over the same extension set, so the agent can still open
// it at the cost of one tool call.
//
// The block carries `data` and `mimeType`, deliberately no `uri`: KAS's
// toDataUrl returns a present uri instead of building the base64 data URL.
func inlineImageBlock(att vibekit.Attachment, displayName, mime string, resolve func(string) (string, error), budget int) (block map[string]any, spentBytes int) {
	_, data, fallback := readForInline(att, displayName, mime, resolve, budget)
	if fallback != nil {
		return fallback, 0
	}
	return map[string]any{
		keyType:    "image",
		"data":     base64.StdEncoding.EncodeToString(data),
		"mimeType": mime,
	}, base64.StdEncoding.EncodedLen(len(data))
}

// readForInline runs the gauntlet every inlined attachment must pass —
// path containment, stat, the per-file cap, the turn's cumulative budget,
// the read, and finally the encoded-payload cap — and returns either the
// bytes or the text block to send instead.
//
// The encoded gate runs after the read since base64's 4/3 inflation is
// invisible to a file-byte check. Shared by the document and image paths
// so the budget accounting cannot drift between two copies. Every failure
// degrades to a path reference: the agent still learns the file exists.
func readForInline(
	att vibekit.Attachment,
	displayName, mime string,
	resolve func(string) (string, error),
	budget int,
) (abs string, data []byte, fallback map[string]any) {
	isImage := strings.HasPrefix(mime, "image/")
	// text/csv is the one documentExts member whose bytes a file tool
	// reads as text, so the caveat is keyed on MIME.
	isBinaryDoc := !isImage && !strings.HasPrefix(mime, "text/")

	abs, err := resolve(att.Path)
	if err != nil {
		slog.Warn("attachment: path escapes workspace",
			"path", displayName, keyError, err)
		return "", nil, vibekit.TextBlock("Attached file (invalid path): " + displayName)
	}
	info, err := os.Stat(abs)
	if err != nil {
		slog.Warn("attachment: stat failed", "path", displayName, keyError, err)
		return "", nil, vibekit.TextBlock("Attached file (unreadable): " + displayName)
	}
	if info.Size() > MaxDocumentBytes {
		slog.Warn("attachment: too large",
			"path", displayName, "size", info.Size(), "cap", MaxDocumentBytes)
		// att.Path, not displayName: this branch needs a path a file tool
		// can open.
		if isImage {
			// MaxDocumentBytes is KAS's own MAX_IMAGE_SIZE, so the image
			// tool refuses at the same threshold — the only remedy is a
			// smaller file.
			return "", nil, vibekit.TextBlock("Attached file: " + att.Path +
				" (too large to inline, and the image tool refuses it at this size too — attach a smaller or resized image)")
		}
		return "", nil, vibekit.TextBlock("Attached file: " + att.Path +
			" (too large to inline — read it with your file tools)")
	}
	// An estimate against the same encoded unit the budget is denominated
	// in; the authority is the post-read check below.
	if base64.StdEncoding.EncodedLen(int(info.Size())) > budget {
		slog.Warn("attachment: turn inline budget exhausted, sending a path reference",
			"path", displayName, "size", info.Size(), "remaining_encoded", budget)
		if isBinaryDoc {
			return "", nil, vibekit.TextBlock("Attached file: " + att.Path +
				" (not inlined: this turn's attachment budget is spent — read it with your file tools; this format may not be readable as text)")
		}
		return "", nil, vibekit.TextBlock("Attached file: " + att.Path +
			" (not inlined: this turn's attachment budget is spent — read it with your file tools)")
	}
	data, err = os.ReadFile(abs)
	if err != nil {
		slog.Warn("attachment: read failed",
			"path", displayName, keyError, err)
		return "", nil, vibekit.TextBlock("Attached file (unreadable): " + displayName)
	}
	// The encoded gate must run here: after the read, on len(data), since
	// no pre-read check can see base64's 4/3 inflation. EncodedLen rather
	// than len(EncodeToString(data)) avoids allocating bytes about to be
	// thrown away.
	if encoded := base64.StdEncoding.EncodedLen(len(data)); encoded > MaxInlineEncodedBytes {
		slog.Warn("attachment: encoded payload over cap, sending a path reference",
			"path", displayName, "size", len(data), "encoded", encoded, "cap", MaxInlineEncodedBytes)
		if isBinaryDoc {
			return "", nil, vibekit.TextBlock("Attached file: " + att.Path +
				" (too large to inline — read it with your file tools; this format may not be readable as text)")
		}
		return "", nil, vibekit.TextBlock("Attached file: " + att.Path +
			" (too large to inline — read it with your file tools)")
	}
	return abs, data, nil
}
