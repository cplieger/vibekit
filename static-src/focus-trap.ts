// ---------------------------------------------------------------------------
// Focus trap — adopted from @cplieger/ui-primitives.
//
// The hand-rolled Tab-cycle implementation was replaced by the library's
// `trapFocus`, which has the same `trapFocus(container) => release` contract
// (release restores the previously-focused element) but adds initialFocus /
// returnFocus options, a fail-closed path when nothing is focusable, and
// position:fixed-aware visibility detection (getClientRects + checkVisibility
// instead of offsetParent).
//
// Kept as a thin re-export so the existing `./focus-trap.js` import path
// (modals.ts, elicitation.ts) is unchanged.
// ---------------------------------------------------------------------------

export { trapFocus } from "@cplieger/ui-primitives/focus-trap";
