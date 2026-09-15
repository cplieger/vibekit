package vibekit

// DefaultUploadDir is where a composer upload lands when the client sends no
// "dir": one folder at the container root, modelled on an OS Downloads folder.
//
// A literal rather than a value derived from KIRO_WORK_DIR, for the reason the
// file handler cannot supply one: it holds a longest-first sorted mount list
// with no notion of which mount is "the workspace", and the client sends the
// same string for the composer's drop and paste uploads.
// TestUploadPolicyMatchesClient pins the two spellings together.
//
// It lives here rather than in internal/filebrowse because THREE packages now
// need it, and none of them may import the file handler for a string:
//
//   - internal/filebrowse resolves an upload with no "dir" against it.
//   - internal/composition grants it as a browse mount, because the handler
//     denies by default — an upload to an ungranted directory is refused, never
//     silently redirected.
//   - internal/command resolves an ATTACHMENT path against it, because an
//     uploaded file's path is handed back as an attachment and the prompt
//     builder confines every attachment to a known root.
//
// internal/vibekit is the one package all three already import and it imports
// no internal package itself, so this adds no cycle and no new coupling
// direction.
const DefaultUploadDir = "/uploads"
