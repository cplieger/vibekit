// ---------------------------------------------------------------------------
// Per-button async feedback: spinner during the operation, ✓ on
// success, ✗ on error, then revert. Disables the button while pending
// and guards against re-entry.
//
// The implementation was promoted verbatim into @cplieger/actions —
// the defaults (check/x glyphs, spinner class `btn-async-spinner`,
// RESET_MS=1200, sr-only "Action completed"/"Action failed" announce,
// keepLabel, data-async-status, removed-from-DOM handling) are
// identical, so this module is now a thin re-export. Every call site
// keeps importing `withAsyncFeedback` from "./async-button.js" and
// behaves exactly the same.
// ---------------------------------------------------------------------------

export { withAsyncFeedback } from "@cplieger/actions";
export type { AsyncFeedbackOptions } from "@cplieger/actions";
