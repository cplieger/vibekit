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

// imageExts maps an image extension to the MIME type vibekit sends when
// inlining an attachment as an ACP `image` content block.
//
// Measured against KAS 2.18.1's extractContentFromPrompt (acp-server.js): the
// image branch pushes `{data, mimeType, dataUrl: toDataUrl(block)}`
// UNCONDITIONALLY. That is the material difference from the document branch,
// which gates on isSupportedDocumentMimeType with no else — so a document with
// an off-list MIME is silently dropped while an image is not. There is no MIME
// allowlist to stay a subset of here.
//
// The extension set mirrors KAS's own IMAGE_EXTENSIONS (the set its image read
// tool accepts, alongside MAX_IMAGE_SIZE = 10 MiB, which MaxDocumentBytes
// already matches). Deliberately NOT svg or avif: KAS excludes both, and an SVG
// is XML a vision model cannot see as a picture. Do not "unify" this with
// utils-url.ts's INLINE_IMAGE_EXT, which is wider on purpose — that list is
// what a BROWSER can render inline, a different consumer with a different
// answer.
//
// A block carrying `uri` is not what we want: toDataUrl returns the uri INSTEAD
// of the base64 data URL when present, which would hand the model a path it
// cannot fetch. Send data + mimeType only.
var imageExts = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
}

// MaxDocumentBytes caps the size of ONE document or image attachment (10 MiB),
// which is also KAS's own MAX_IMAGE_SIZE.
//
// Kept in FILE bytes deliberately, unlike the two caps below. It is the number a
// user can predict from a file listing, and it is the cheap pre-read gate that
// stops an oversized file being allocated at all.
const MaxDocumentBytes = 10 * 1024 * 1024

// MaxInlineEncodedBytes caps the BASE64 payload of one inlined attachment.
//
// The gate MaxDocumentBytes cannot be. Base64 inflates by 4/3, and that inflation
// is invisible to every check that measures file bytes: a ~3.9 MiB raster passes
// the per-file cap and is still refused on the wire, and a full-size 10 MiB image
// ships ~13.3 MiB with nothing in the process having computed that number. So the
// check has to run on the ENCODED payload, after the read.
//
// Why refusing matters more here than a wasted upload: kiro-cli replays the full
// conversation history on every turn, so a payload the backend REFUSES sits at a
// fixed history index and is re-sent on every later turn. The session is wedged
// permanently, and a follow-up resize cannot evict the original — the only exits
// are a rewind to that turn or a new chat.
//
// THE NUMBER IS INFERRED, not measured here, and that is worth knowing before
// trusting it. 5 MiB (5 * 1024 * 1024 = 5242880) is the figure KiroCrew read off
// its own backend's refusal text, `image exceeds 5 MB maximum: 6714372 bytes >
// 5242880`. Both products front the same vendor's catalogue, which makes it the
// best available prior, and the error direction settles the rest: erring low ships
// a smaller image, erring high ships a payload the backend refuses and wedges the
// session. To replace the inference with a measurement, send a deliberately
// over-cap image and read the name the backend returns — it will be one of
// validationErrorNames in prompt.go, most likely `ImageSizeExceeded`, and the
// message carries the real ceiling in bytes.
//
// It bites only on an unresized photograph or a multi-megapixel export: a
// 2000px-long-edge screenshot, which is what the client's own paste-time downscale
// produces, weighs well under 1.5 MiB.
const MaxInlineEncodedBytes = 5 * 1024 * 1024

// MaxInlineTurnEncodedBytes caps the total ENCODED bytes one prompt may inline.
//
// A per-attachment cap is not enough, and the reason is a property of the wire
// rather than of vibekit: kiro-cli REPLAYS the full conversation history on every
// turn, so an inlined document does not cost its bytes once, it costs them for
// the life of the session. Several near-limit spreadsheets pass the per-file check
// individually and put a fixed history index's worth of payload into every later
// turn. That is the same permanent-wedge shape upstream capped image dimensions
// for, reached by a different door.
//
// Counted in ENCODED bytes, which is a correction rather than a preference: the
// cap exists to bound what the history carries, and the history carries the base64
// payload. The previous 20 MiB of FILE bytes was ~26.7 MiB on the wire, so this
// tighter-looking number is close to the same real ceiling. The visible effect is
// on documents: two full-size 10 MiB spreadsheets no longer both inline, and the
// second becomes a path reference the agent opens with its file tools.
const MaxInlineTurnEncodedBytes = 15 * 1024 * 1024

// BuildPromptBlocks constructs the ACP prompt content array: a leading text
// block followed by one block per attachment.
//
// On v3 (KAS) the agent always advertises
// promptCapabilities.embeddedContext:true, so a supported document type
// (documentExts) is always inlined as an ACP embedded `resource` content
// block; everything else becomes a text path reference the agent reads with
// its file tools.
func BuildPromptBlocks(ctx context.Context, text string, attachments []vibekit.Attachment, resolve func(string) (string, error)) []map[string]any {
	blocks := []map[string]any{vibekit.TextBlock(text)}
	// budget is the turn's remaining inline allowance in ENCODED bytes, spent by
	// each attachment actually read. It is threaded rather than global because it
	// describes THIS prompt: the next turn gets a fresh one, and the history cost
	// of what was already sent is not something a later prompt can refund.
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
// supported document type (documentExts) is inlined as an embedded `resource`
// block; everything else becomes a text path reference the agent reads with
// its file tools.
// The second return value is the ENCODED bytes this block consumed from the turn's
// inline budget: zero for every path-reference block, and the base64 length for one
// that was inlined. Encoded rather than file bytes because that is what the wire
// carries and therefore what the history replays; see MaxInlineTurnEncodedBytes.
func attachmentBlock(att vibekit.Attachment, resolve func(string) (string, error), budget int) (block map[string]any, spentBytes int) {
	displayName := filepath.Base(att.Path)
	ext := strings.ToLower(filepath.Ext(att.Path))

	// Inline a supported document as an embedded resource block.
	if mime, isDoc := documentExts[ext]; isDoc {
		return inlineResourceBlock(att, displayName, mime, resolve, budget)
	}
	// Inline an image as an `image` content block. KAS advertises
	// promptCapabilities.image:true on v3, so this is the sanctioned channel
	// for putting a picture in front of the model; the path-reference branch
	// below only tells it a file exists, costing a tool call to look at a
	// screenshot the user already chose to show it.
	if mime, isImg := imageExts[ext]; isImg {
		return inlineImageBlock(att, displayName, mime, resolve, budget)
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
		return vibekit.TextBlock("Attached file (invalid path): " + displayName), 0
	}
	if unsupportedDocExts[ext] {
		return vibekit.TextBlock("Attached file: " + att.Path +
			" (binary document — read it with your file tools; this format may not be readable as text)"), 0
	}
	return vibekit.TextBlock("Attached file: " + att.Path), 0
}

// inlineResourceBlock reads a document attachment from disk and returns an
// ACP embedded `resource` content block (v3/KAS: the content-block union
// has no `document` type — it is text | image | audio | resource_link |
// resource). The document rides the blob variant of EmbeddedResourceResource
// (uri + mimeType + base64 blob). On any failure (path escape, stat/read
// error, or oversize) it returns a descriptive text block instead so the
// agent is never left silently dropping the attachment.
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
// `image` content block. Failure degrades to the same descriptive text blocks
// the document path uses, which for an image is a genuine fallback rather than
// a consolation prize: KAS has an image read tool over the same extension set,
// so the agent can still open it — it just costs a tool call.
//
// The block carries `data` and `mimeType` and deliberately no `uri`: KAS's
// toDataUrl returns a present uri INSTEAD of building the base64 data URL, so
// including one would replace the bytes with a path the model cannot fetch.
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

// readForInline runs the gauntlet every inlined attachment must pass — path
// containment, stat, the per-file cap, the turn's cumulative budget, the read, and
// finally the encoded-payload cap — and returns either the bytes or the text block
// to send instead.
//
// Note where the last gate sits: three of the checks measure FILE bytes and run
// before the read, and the encoded one has to run AFTER it, because base64's 4/3
// inflation is invisible to all of them.
//
// It is shared by the document and image paths because the gauntlet is the same
// for both, and a second copy of it is a second place for the budget accounting
// to drift. Every failure degrades to a path reference in the same fail-closed
// direction: the agent still learns the file exists and can open it with its
// file tools, which is strictly better than a session wedged by history it
// cannot drop.
//
// The note on that path reference is branched by cause AND class, because
// "read it with your file tools" is not followable everywhere: an image the
// per-file cap refused is over KAS's image-read limit too (they are the same
// number), and a binary document needs the caveat the unsupportedDocExts
// branch already carries. mime is what decides both, taken from the caller
// rather than re-derived, so there is one classification of an extension in
// this file.
func readForInline(
	att vibekit.Attachment,
	displayName, mime string,
	resolve func(string) (string, error),
	budget int,
) (abs string, data []byte, fallback map[string]any) {
	isImage := strings.HasPrefix(mime, "image/")
	// Not every inlined document is binary: text/csv is the one member of
	// documentExts whose bytes a file tool reads as text, so the caveat is keyed
	// on the MIME rather than applied to the whole document path.
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
		// att.Path, not displayName. This is the one branch that most needs to be
		// actionable — the agent cannot inline the file, so opening it is the only
		// way it sees the contents — and a bare basename is not something a file
		// tool can open. The budget branch below has always had this right.
		if isImage {
			// MaxDocumentBytes IS KAS's MAX_IMAGE_SIZE, so the one tool that could
			// look at the picture refuses it at the same threshold this gate just
			// did. Telling the agent to read it is advice it cannot follow; the
			// only remedy left is a smaller file.
			return "", nil, vibekit.TextBlock("Attached file: " + att.Path +
				" (too large to inline, and the image tool refuses it at this size too — attach a smaller or resized image)")
		}
		return "", nil, vibekit.TextBlock("Attached file: " + att.Path +
			" (too large to inline — read it with your file tools)")
	}
	// An ESTIMATE against the same encoded unit the budget is denominated in, so
	// an over-budget set still costs no allocation. The authority is the post-read
	// check below; EncodedLen of the stat size can only differ from it if the file
	// changed between the stat and the read.
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
	// The encoded gate, and it must be HERE: after the read, on len(data), and
	// before the caller encodes. info.Size() cannot answer it (a file can grow
	// between the stat and the read) and no pre-read check can see base64's 4/3
	// inflation at all.
	//
	// EncodedLen rather than len(EncodeToString(data)): it is exact for
	// StdEncoding and pure integer arithmetic, so a rejection does not first
	// allocate the megabytes it is about to throw away.
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
