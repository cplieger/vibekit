// ---------------------------------------------------------------------------
// tab-view: the LOCAL half of a tab.
//
// A tab is two things with two different owners, and this file is the second of
// them.
//
// The SHARED half is `TabSubject` (internal/vibekit/domain_tabs.go): what is
// open, where it sits, whether closing it tears the thing down. It is persisted
// in tabs.json, it is the only tab shape that crosses the wire, and every
// connected device holds the same copy of it.
//
// The LOCAL half is `TabViewSpec` below: a view selector, a typed route, an
// activation hook, a teardown hook, an icon, a display name, an activity dot.
// None of that is membership — it is behaviour and pixels — so none of it is the
// server's business, and none of it is persisted or transmitted. A subject
// carries no behaviour at all, which is what stops activation and teardown
// drifting server-side one field at a time.
//
// DOM-FREE on purpose. Two tables, one type and one interface need no document,
// so they test without one; the code that paints from them stays in tabs.ts, and
// the code that PRODUCES one from a subject is tab-materialize.ts.
// ---------------------------------------------------------------------------

import type { Route } from "./router.js";
// The nine tab kinds have ONE definition and it is the Go const block in
// internal/vibekit/domain_tabs.go, emitted here by wire-codegen as a registered
// enum. Both tables below are typed as exhaustive records over it, so a new
// kind added server-side fails the client type gate here rather than reaching a
// switch with no case for it on every connected device at once.
import type { TabKind } from "./types.js";
import {
  ICON_TAB_CHAT,
  ICON_TAB_SETTINGS,
  ICON_TAB_GIT,
  ICON_TAB_FILES,
  ICON_TAB_RUN,
  ICON_TAB_AGENT,
  ICON_TAB_EDITOR,
  ICON_TAB_HISTORY,
  ICON_TAB_DOCS,
} from "./icons.js";

/** The view element each tab kind shows. Callers can omit `view` from a spec
 *  they build by hand when the standard mapping applies.
 *
 *  Typed as an exhaustive record over the WIRE's TabKind rather than as the
 *  thing that DEFINES the vocabulary, which is the reversal that matters: this
 *  table used to be the client's only enumeration of the kinds, so a new one
 *  added server-side reached a switch here with no case for it and no build
 *  error anywhere.
 *
 *  It lives here rather than in tabs.ts because it is not the store's: it is a
 *  per-kind LOCAL fact, the same class of thing as the icon beside it and the
 *  route the factory builds, and the factory needs it without needing the store.
 *  tabs.ts re-exports it, so its dozen consumers are unaffected. */
export const TAB_VIEWS: Readonly<Record<TabKind, string>> = {
  chat: "#chat-view",
  settings: "#settings-view",
  git: "#git-view",
  files: "#files-view",
  editor: "#editor-view",
  history: "#history-view",
  docs: "#docs-view",
  run: "#run-view",
  subagent: "#subagent-view",
};

/** The leading glyph each tab kind renders.
 *
 *  There is deliberately no per-tab override. One existed for exactly one
 *  purpose — a per-agent-role glyph on chat tabs — and a chat tab's leading
 *  element is the activity dot now, so the field had one producer feeding a slot
 *  that no longer renders one.
 *
 *  A `subagent` tab therefore takes the shared agent hexagon rather than the
 *  per-known-subagent glyph its card carries (roles.ts `iconForSubagent`): the
 *  strip has one glyph per KIND, and that is the same trade a chat tab already
 *  makes for its mode. */
export const TAB_ICONS: Readonly<Record<TabKind, string>> = {
  chat: ICON_TAB_CHAT,
  settings: ICON_TAB_SETTINGS,
  git: ICON_TAB_GIT,
  files: ICON_TAB_FILES,
  editor: ICON_TAB_EDITOR,
  history: ICON_TAB_HISTORY,
  docs: ICON_TAB_DOCS,
  run: ICON_TAB_RUN,
  subagent: ICON_TAB_AGENT,
};

/** The activity dot's states. Six come from a chat's live state (derived by
 *  `tabStatusFor` in store.ts); "dirty" is the editor's unsaved mark, which
 *  rides the same element because a tab is never both a chat and a file.
 *
 *  Here rather than in tabs.ts because it is part of the view CONTRACT — a
 *  materialized spec carries one — while the word each state is announced with
 *  and the element it paints stay with the DOM that owns them.
 *
 *  Ported from @cplieger/web-terminal-ui's `.wt-status-dot`; the visual grammar
 *  and the reasoning behind each state live in css/12-tabs.css. */
export type TabDotStatus = "idle" | "working" | "waiting" | "input" | "failed" | "done" | "dirty";

/** Everything the strip needs about one tab that the server has no business
 *  knowing. Produced from a `TabSubject` by `materializeTab` (tab-materialize.ts)
 *  and by nothing else, so there is one answer per kind rather than one per
 *  door.
 *
 *  What is NOT here is the point of the split. `id`, `kind` and `ref` are the
 *  subject's IDENTITY: duplicating them into the local half would create a
 *  second copy that can disagree with the persisted one, and the strip already
 *  holds the subject beside this. `pinned` is absent for a sharper reason — see
 *  below.
 *
 *  Every field is readonly because a spec is a SNAPSHOT taken at open. Later
 *  changes to a tab (a rename, a dot write, a pin) go through the store's own
 *  mutators, which is where they already went. */
export interface TabViewSpec {
  /** What the strip shows as this tab's label.
   *
   *  A DERIVED DEFAULT, and the one field the factory cannot always answer: a
   *  chat's name lives in the chat store and a run's in the run store, so a
   *  subject whose record has not been fetched yet has no better name than a
   *  placeholder. A caller holding a better one (History's row title, a
   *  `run_started` payload's name, a recipe's name) overrides this field; see
   *  tab-materialize.ts's header for the full statement of the gap. */
  readonly name: string;
  /** The leading glyph, from TAB_ICONS. On the spec rather than looked up at
   *  render time so a spec is a complete description of a row. */
  readonly icon: string;
  /** CSS selector for the view element to show, from TAB_VIEWS. */
  readonly view: string;
  /** The URL route this tab maps to.
   *
   *  Typed, not a string, so a tab cannot carry a location the router does not
   *  know. Every tab must carry a route that uniquely identifies its logical
   *  location — the store's "one mutation, three subscribers" promise (view,
   *  render, URL) only holds when each tab has a real one. */
  readonly route: Route;
  /** Called when the tab becomes active. */
  readonly onShow?: (() => void) | undefined;
  /** Called when the tab is closed.
   *
   *  CLIENT-LOCAL teardown only, identical on every device: the arrangement is
   *  server-owned, and everything a close destroys beyond this device's own
   *  state — the process, the runs, a retention-off chat's record — is the
   *  server's close operation, run once wherever the gesture happened. So
   *  there is nothing here to suppress per provenance, and no flag saying
   *  whose gesture it was. For a close THIS device dispatched it runs deferred,
   *  at the pending-op machine's confirmation; for another device's close (or a
   *  snapshot that dropped the tab) it runs as the removal is applied. */
  readonly onClose?: (() => void) | undefined;
  /** Whether closing this tab tears down what it shows.
   *
   *  A SUBJECT fact, copied through rather than decided here, and the factory is
   *  forbidden from deriving it from the kind: a launcher-owned run and a run
   *  REVIEW opened from History share `(kind, ref)` and differ only in this, so
   *  closing the owned one cancels the run while closing the review dismisses a
   *  view. `owns: false` makes the tab a VIEW, which is what lets a sub-tab
   *  watching work another chat owns be closed without killing that work.
   *
   *  It rides the local spec because the STRIP is what reads it — closeTab
   *  consults it to decide whether to fire `onClose` — and it is safe to
   *  snapshot because a subject's `owns` is set at open and never reassigned. */
  readonly owns: boolean;
  /** The tab this one hangs under, making it a SUB-TAB: indented under its
   *  parent, sorted immediately after it, not independently draggable, closed
   *  when its parent closes.
   *
   *  Also a subject fact copied through, and safe to snapshot for the same
   *  reason `owns` is: `Parent` is set at open and never reassigned, which is
   *  what makes a cycle unrepresentable.
   *
   *  `pinned` is deliberately NOT here even though it is the same shape of
   *  subject fact, because it MUTATES after open — `pin_tab` changes it — so a
   *  snapshot of it would go stale the moment someone pinned the tab. That is
   *  the line: an immutable-at-open subject fact may ride the spec, a mutable
   *  one must be read from the subject. */
  readonly parentId?: string | undefined;
  /** The activity dot's state at materialization, so a row that is CREATED
   *  already knows what it should show.
   *
   *  The dot used to live only in the DOM, so any path that built a row without
   *  a following state change showed the seeded `idle` whatever the chat was
   *  doing — and the boot restore is exactly such a path, because it populates
   *  sessions BEFORE it opens their tabs.
   *
   *  LIVE state, and it must never be persisted: a dot restored from a previous
   *  process would be a claim about a turn that ended before the page loaded. */
  readonly dotStatus?: TabDotStatus | undefined;
}
