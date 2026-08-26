// Single source of truth for localStorage keys used across modules.
// The anti-FOUC theme-init script inlined in static/index.html can't import
// this (it runs before modules load), so it hardcodes the same literal — kept
// in sync by theme-init-snippet.test.ts.

/** The blob holding everything about THIS SCREEN: the active tab, the two shell
 *  fields, and the theme's pre-paint cache. Owned end to end by
 *  `device-view.ts`, which is the only writer — every write is a
 *  read-modify-write of one JSON document, so a second writer drops whatever
 *  landed between its read and its write.
 *
 *  The name is a leftover from when this key held the whole UI arrangement, and
 *  it is KEPT deliberately: nothing is migrated, and renaming it would silently
 *  reset every reader's shell height and theme. The arrangement itself is
 *  server-owned: the tab SET is its own collection (`internal/tabs`, projected by
 *  `tabs.ts`), and the theme and browser path are `config.json` keys. */
export const LS_UI_STATE_KEY = "vibekit.ui-state";

/** Per-chat, per-turn fold overrides: which turns THIS reader has opened or
 *  folded by hand.
 *
 *  Per-device rather than in the server-owned arrangement, and that is a
 *  reversal: it used to be `ui-state.turn_folds`. A fold is a DISCLOSURE state,
 *  which the companion audit's table files as correctly the viewer's ("which
 *  sections this reader has open"), and sharing it reproduced the very defect
 *  that moved the arrangement server-side — a fold on one screen rearranged a
 *  transcript someone else was reading. */
export const LS_TURN_FOLDS_KEY = "vibekit.turn-folds";

/** Per-chat dismissed banner codes: which notices THIS reader has acknowledged.
 *
 *  Per-device for the fold's reason, one step stronger: an acknowledgement is
 *  the viewer's (web-terminal's rule verbatim), so a phone dismissing a banner
 *  must not silence the desktop. It used to be `ui-state.dismissed_banners`. */
export const LS_DISMISSED_BANNERS_KEY = "vibekit.dismissed-banners";
