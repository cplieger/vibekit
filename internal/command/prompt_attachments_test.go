package command

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// kasSupportedDocumentMIMEs mirrors KAS's SUPPORTED_DOCUMENT_MIME_TYPES
// (src/document-types.ts in the @kiro/agent bundle, verified against KAS
// 2.12.0). KAS's extractContentFromPrompt keeps an inline blob resource ONLY
// when its MIME is in this set, with no else branch — so any documentExts
// entry outside it is read, base64'd, transmitted, and then silently dropped.
// Keep this in sync with the bundle if KAS's allowlist ever changes.
var kasSupportedDocumentMIMEs = map[string]bool{
	"application/pdf":    true,
	"text/csv":           true,
	"application/msword": true,
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": true,
	"application/vnd.ms-excel": true,
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet": true,
	"text/html":     true,
	"text/plain":    true,
	"text/markdown": true,
}

// TestDocumentExtsSubsetOfKAS is the drift guard for the v3 attachment bug:
// documentExts must be a subset of KAS's SUPPORTED_DOCUMENT_MIME_TYPES.
// Anything vibekit inlines as an embedded `resource` block whose MIME KAS
// does not accept is silently dropped (the agent never sees the file and no
// error surfaces). If this fails, either drop the extension from documentExts
// or route it through unsupportedDocExts / the path-reference branch.
func TestDocumentExtsSubsetOfKAS(t *testing.T) {
	for ext, mime := range documentExts {
		if !kasSupportedDocumentMIMEs[mime] {
			t.Errorf("documentExts[%q] = %q is not in KAS SUPPORTED_DOCUMENT_MIME_TYPES; "+
				"KAS would silently drop it — remove it or route %s through the path-reference branch",
				ext, mime, ext)
		}
	}
}

// TestUnsupportedDocExtsDisjointFromDocumentExts guards the two attachment
// maps against overlap: an extension inlined as a resource block
// (documentExts) must never also be listed as an unsupported format routed
// to a path reference (unsupportedDocExts), or the intent of one is dead.
func TestUnsupportedDocExtsDisjointFromDocumentExts(t *testing.T) {
	for ext := range unsupportedDocExts {
		if _, ok := documentExts[ext]; ok {
			t.Errorf("extension %q is in both documentExts and unsupportedDocExts", ext)
		}
	}
}

// kasImageExtensions mirrors KAS's IMAGE_EXTENSIONS (verified against the
// 2.18.1 bundle's acp-server.js, beside MAX_IMAGE_SIZE = 10 MiB). It is the set
// KAS's own image read tool accepts, which is what makes the path-reference
// fallback work for an image that cannot be inlined.
var kasImageExtensions = map[string]bool{
	".png":  true,
	".jpg":  true,
	".jpeg": true,
	".gif":  true,
	".webp": true,
}

// TestImageExtsMatchesKAS pins imageExts to KAS's own image extension set in
// BOTH directions, and the two directions fail differently.
//
// An extension here that KAS does not know is one whose path-reference fallback
// cannot be read either, so the attachment is unreachable by both routes. An
// extension KAS accepts that is missing here silently costs the user the inline
// channel, sending a path where a picture was meant.
//
// Note this is NOT the same list as utils-url.ts's INLINE_IMAGE_EXT, which also
// carries svg and avif because a browser renders those. Two consumers, two
// answers; do not unify them.
func TestImageExtsMatchesKAS(t *testing.T) {
	for ext := range imageExts {
		if !kasImageExtensions[ext] {
			t.Errorf("imageExts has %q, which KAS's IMAGE_EXTENSIONS does not: "+
				"neither the inline block nor the path-reference fallback can be read", ext)
		}
	}
	for ext := range kasImageExtensions {
		if _, ok := imageExts[ext]; !ok {
			t.Errorf("KAS accepts %q but imageExts omits it, so it degrades to a path "+
				"reference when it could have been shown to the model", ext)
		}
	}
}

// TestImageExtsExcludesUnviewableFormats pins the two exclusions that look like
// oversights. KAS omits both, and an SVG is XML rather than pixels, so inlining
// one hands a vision model markup to look at.
func TestImageExtsExcludesUnviewableFormats(t *testing.T) {
	for _, ext := range []string{".svg", ".avif", ".bmp", ".tiff"} {
		if _, ok := imageExts[ext]; ok {
			t.Errorf("imageExts must not carry %q: KAS does not accept it as an image", ext)
		}
	}
}

// TestAttachmentBlock_ImageInlinesAsImageBlock pins the wire shape measured off
// the bundle: `{type:"image", data:<base64>, mimeType:<mime>}` and NO uri.
//
// The uri assertion is the load-bearing one. KAS's toDataUrl returns a present
// uri INSTEAD of building the base64 data URL, so shipping one would replace the
// bytes with a path the model cannot fetch — a silent failure with no error on
// either side.
func TestAttachmentBlock_ImageInlinesAsImageBlock(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "shot.png")
	pixels := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x01}
	if err := os.WriteFile(abs, pixels, 0o600); err != nil {
		t.Fatal(err)
	}
	resolve := func(string) (string, error) { return abs, nil }

	block, spent := attachmentBlock(vibekit.Attachment{Path: abs, Name: "shot.png"},
		resolve, MaxInlineTurnBytes)

	if got := block[keyType]; got != "image" {
		t.Fatalf("block type = %v, want image", got)
	}
	if got := block["mimeType"]; got != "image/png" {
		t.Errorf("mimeType = %v, want image/png", got)
	}
	if got, want := block["data"], base64.StdEncoding.EncodeToString(pixels); got != want {
		t.Errorf("data = %v, want the base64 of the file bytes", got)
	}
	if _, ok := block["uri"]; ok {
		t.Error("block carries a uri; KAS's toDataUrl would return it instead of the bytes")
	}
	if spent != len(pixels) {
		t.Errorf("spent = %d, want %d (the image is charged against the turn budget)",
			spent, len(pixels))
	}
}

// TestAttachmentBlock_ImageDegradesToPathReference covers the two failures that
// must not lose the attachment: over the turn's cumulative budget, and over the
// per-file cap. Both fall back to a path the agent can read with its image tool.
func TestAttachmentBlock_ImageDegradesToPathReference(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "big.png")
	if err := os.WriteFile(abs, make([]byte, 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	resolve := func(string) (string, error) { return abs, nil }

	t.Run("over_turn_budget", func(t *testing.T) {
		block, spent := attachmentBlock(vibekit.Attachment{Path: abs}, resolve, 10)
		if got := block[keyType]; got != vibekit.ContentTypeText {
			t.Errorf("block type = %v, want text (a path reference)", got)
		}
		if spent != 0 {
			t.Errorf("spent = %d, want 0 for a block that inlined nothing", spent)
		}
	})

	t.Run("path_escapes_workspace", func(t *testing.T) {
		deny := func(string) (string, error) { return "", errors.New("escapes") }
		block, spent := attachmentBlock(vibekit.Attachment{Path: abs}, deny, MaxInlineTurnBytes)
		if got := block[keyType]; got != vibekit.ContentTypeText {
			t.Errorf("block type = %v, want text", got)
		}
		if spent != 0 {
			t.Errorf("spent = %d, want 0", spent)
		}
	})
}

// A .txt attachment takes the path-reference branch, and D6's large-paste spill
// rests entirely on that: a paste over the threshold is written as a .txt in the
// uploads folder and attached like any other file, so the agent reads it with
// its file tools instead of receiving an opaque base64 blob.
//
// Do NOT add .txt to documentExts to "improve" this. TestDocumentExtsSubsetOfKAS
// would still pass (KAS's allowlist covers text/plain), so nothing would fail —
// the spilled paste would just silently become base64 the model cannot skim, and
// it would start spending the turn's inline budget.
func TestAttachmentBlock_TextFileTakesPathReference(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "paste-2026-08-16T01-42-33.txt")
	if err := os.WriteFile(abs, []byte("a pasted log\nsecond line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolve := func(string) (string, error) { return abs, nil }

	block, spent := attachmentBlock(vibekit.Attachment{Path: abs, Name: filepath.Base(abs)},
		resolve, MaxInlineTurnBytes)

	if got := block[keyType]; got != vibekit.ContentTypeText {
		t.Fatalf("block type = %v, want text (a path reference)", got)
	}
	wantText := "Attached file: " + abs
	if got := block["text"]; got != wantText {
		t.Errorf("text = %v, want %q", got, wantText)
	}
	if spent != 0 {
		t.Errorf("spent = %d, want 0: a path reference inlines nothing and so costs no budget", spent)
	}
	if _, isDoc := documentExts[".txt"]; isDoc {
		t.Error(".txt is in documentExts; a spilled paste would arrive as base64 instead of a readable file")
	}
	if unsupportedDocExts[".txt"] {
		t.Error(".txt is in unsupportedDocExts; it would carry a misleading binary-format note")
	}
}
