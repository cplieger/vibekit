package command

import "testing"

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
