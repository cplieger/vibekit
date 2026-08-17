// attention.ts — the unseen-cue set, rendered onto the surfaces that live OUTSIDE
// this page: the browser tab title's count, the installed app's icon badge, and
// the tab icon.
//
// Why they are needed at all: a chat holding a latched status paints a dot on its
// sidebar row, and that dot is not reliably on screen. `#tab-list` scrolls, so a
// long chat list puts the forgotten background chat below the fold; on mobile the
// sidebar is a drawer parked off-viewport, so no row is visible at all; and a
// hidden page shows no chrome of any kind. notify.ts covers the last case with a
// browser Notification, but that API is absent on iOS Safari outside an installed
// web app, which is where this UI spends much of its time.
//
// STATE, not events. A Notification fires once per occurrence and is deduped on a
// timestamp (handlers/turn.ts `_lastNotifyMs`); everything here is a pure render
// of what is currently true, so it is safe to recompute on every status write and
// each sink no-ops when nothing changed. That split is why this module and
// notify.ts stay separate, and why nothing here has a timer.
//
// PORTED from @cplieger/web-terminal-ui's features/tabs/attention.ts rather than
// imported. 5.6.0's `exports` map publishes `./features/tabs` and nothing below
// it, so `@cplieger/web-terminal-ui/features/tabs/attention` resolves to
// ERR_PACKAGE_PATH_NOT_EXPORTED even though `files: ["src/"]` puts the file on
// disk under node_modules. `features/tabs/model` (the cue vocabulary) is exported
// at no subpath in any version. Three things also diverge on purpose and are
// commented where they land: the cue set, the severity order, and the
// acknowledgement rule.
//
// DOM-free and global-free above the binding line: every capability arrives
// through an injected env, so the decisions are unit-testable with no document,
// no navigator and no icon assets. Everything below "The browser binding" is the
// one place that touches globals.

import { getActiveId } from "./store.js";
import { cueCandidates, subscribeTabCues, setOnTabClosed } from "./tabs.js";
import { BUS_TAB_CHANGED, onBus } from "./bus.js";
import { $ } from "./dom.js";

// ---------------------------------------------------------------------------
// The cue vocabulary
// ---------------------------------------------------------------------------

/** The dot states that raise an attention cue: the states that WANT the reader.
 *
 *  A chat blocked on a decision (`input`), one whose agent asked a question and
 *  is standing by (`waiting`), one whose last operation failed (`failed`), and
 *  one whose turn finished (`done`).
 *
 *  `working` and `idle` are excluded because they are ongoing or absent: a count
 *  that ticked up while an agent worked would nag with nothing to act on.
 *  `dirty` is the editor's unsaved mark, which rides the same dot element and is
 *  not a chat state at all — it is excluded here AND by the candidate filter
 *  (tabs.ts `cueCandidates`), so it cannot reach the fold by either route. */
export type CueStatus = "input" | "waiting" | "failed" | "done";

/** Severity order over CueStatus, most severe first, AND the complete set: this
 *  array is what isCueStatus tests against, so the type and the runtime list
 *  cannot drift the way two hand-maintained four-way checks could.
 *
 *  Read by the one surface that can show a single state (the tab icon), which
 *  paints the most severe unseen cue. That is a total order and therefore a
 *  defensible rule, unlike picking a chat.
 *
 *  THIS ORDER DIVERGES FROM THE REFERENCE, deliberately. web-terminal-ui ranks
 *  `failed` above `input`; vibekit ranks the pending ask first, because an ask
 *  BLOCKS the turn while a failure is a result the agent parked and will not
 *  revisit. That is the same reasoning `tabStatusFor` (store.ts) already used to
 *  put `input` ahead of everything, and the icon disagreeing with the tab dot
 *  about which chat matters most would be a real defect — so one order wins and
 *  it is this app's. */
export const CUE_SEVERITY: readonly CueStatus[] = ["input", "failed", "waiting", "done"];

/** isCueStatus narrows a raw dot state to a cue-worthy one. */
export function isCueStatus(status: string): status is CueStatus {
  return (CUE_SEVERITY as readonly string[]).includes(status);
}

/** worseCue returns whichever of two cues is more severe; "" means no cue. */
export function worseCue(a: CueStatus | "", b: CueStatus | ""): CueStatus | "" {
  if (a === "") {
    return b;
  }
  if (b === "") {
    return a;
  }
  return CUE_SEVERITY.indexOf(a) <= CUE_SEVERITY.indexOf(b) ? a : b;
}

/** The icon variant a cue paints, which is NOT one per cue.
 *
 *  `waiting` maps onto the `input` asset. On the tab DOT the two are deliberately
 *  distinct (hollow disc plus ring versus solid plus ring, css/12-tabs.css)
 *  because they are different kinds of wanting; a 16px favicon badge cannot carry
 *  that distinction — the whole badge is 5.5 units across in the generator's
 *  32-unit space — and both states mean "this chat wants you". So they share one
 *  asset rather than the icon claiming a fidelity it does not have. That also
 *  keeps the shipped set at three variants, which is what
 *  favicon-variants.test.ts pins.
 *
 *  A Record rather than a switch, so the map is exhaustive over CueStatus by type
 *  and a new cue cannot ship unmapped (the same reason DOT_PHRASE is one). */
const CUE_ICON: Readonly<Record<CueStatus, "input" | "done" | "alert">> = {
  input: "input",
  waiting: "input",
  failed: "alert",
  done: "done",
};

export function cueIconName(status: CueStatus): "input" | "done" | "alert" {
  return CUE_ICON[status];
}

/** isUnseenCue reports whether a chat's CURRENT dot state is a cue this reader
 *  has not acknowledged. The single predicate behind both the count and the
 *  icon, so the two surfaces cannot disagree about what is outstanding.
 *
 *  Note what it does NOT consider: whether the chat is the one on screen. A cue
 *  on a watched chat is acknowledged as it is observed (the refresh pass below),
 *  so it is already absent from the unseen set by the time anything folds over
 *  it, and no caller needs a special case. */
export function isUnseenCue(
  status: string,
  id: string,
  seen: ReadonlyMap<string, CueStatus>,
): status is CueStatus {
  return isCueStatus(status) && seen.get(id) !== status;
}

// ---------------------------------------------------------------------------
// The fold
// ---------------------------------------------------------------------------

/** What the fold reads per chat tab. A structural type, so tabs.ts passes its own
 *  projection without this module knowing what else a TabSpec carries. */
export interface CueCandidate {
  readonly id: string;
  readonly status: string;
}

/** The whole attention state, and the only thing the sinks are allowed to see. */
export interface Attention {
  /** How many chats hold an unacknowledged cue. */
  readonly count: number;
  /** The most severe of them, or "" when there is none. */
  readonly worst: CueStatus | "";
}

export const NO_ATTENTION: Attention = { count: 0, worst: "" };

/** summarize folds the chat tabs and this reader's acknowledgements into the one
 *  value every surface renders.
 *
 *  A COUNT for the title and the badge, a single WORST for the icon. The split is
 *  deliberate: a count is set-valued, so it needs no rule for choosing among
 *  chats, and severity is a total order, so the icon's choice is not arbitrary
 *  either. Neither surface can name a chat, which is the standing constraint on
 *  anything written to a page-wide surface.
 *
 *  A FOLD over current state rather than incremental bookkeeping, which is what
 *  makes it impossible to leave stale: it holds no state of its own, so no path
 *  can forget to update it, and it is cheap enough to run on every dot write (a
 *  loop over a handful of tabs, then sinks that no-op when nothing changed). */
export function summarize(
  candidates: readonly CueCandidate[],
  seen: ReadonlyMap<string, CueStatus>,
): Attention {
  let count = 0;
  let worst: CueStatus | "" = "";
  for (const candidate of candidates) {
    if (!isUnseenCue(candidate.status, candidate.id, seen)) {
      continue;
    }
    count += 1;
    worst = worseCue(worst, candidate.status);
  }
  return { count, worst };
}

// ---------------------------------------------------------------------------
// The sinks
// ---------------------------------------------------------------------------

/** titlePrefixFor is the title text for a count, and the format is load-bearing
 *  enough to name: the count goes FIRST, because a browser tab strip truncates a
 *  title to its first few characters and a suffix would be the part that is cut.
 *  Parenthesised digits are also the convention every mail and chat client uses,
 *  so it needs no legend. Byte-identical to the format the retired `setBadge`
 *  wrote, so no reader has to relearn it. */
export function titlePrefixFor(count: number): string {
  return count > 0 ? `(${String(count)}) ` : "";
}

/** The capabilities the sinks need, all injected, all optional except the title.
 *
 *  An absent capability is an absent sink and therefore a silent no-op, the same
 *  contract notify.ts uses for a missing Notification constructor: an unsupported
 *  surface is a normal state of the world, not an error to report.
 *
 *  Two of these can also fail INVISIBLY, and that is accepted rather than
 *  handled. `setBadge` resolves on Linux where the desktop paints no badge at
 *  all, and `setIcon` assigns an href Safari ignores because it caches the first
 *  icon it fetched. Neither is detectable. Do NOT arrange the three into a
 *  fallback ladder where the title appears only if the others are missing: on
 *  those platforms the detection reports success and the reader is left with
 *  nothing. The title is gated on no capability and is therefore the floor. */
export interface AttentionEnv {
  /** Set (or clear, with "") the document-title prefix. A COMPOSING writer: it
   *  owns the base title, so repeated calls cannot compound a prefix. */
  titlePrefix: (text: string) => void;
  /** Set the installed app's icon badge to a count, or clear it at zero. */
  setBadge?: ((count: number) => void) | undefined;
  /** Point every icon link at a variant, or restore them with null. */
  setIcon?: ((variant: "input" | "done" | "alert" | null) => void) | undefined;
}

export interface AttentionSurfaces {
  /** Render an attention state. Idempotent: a value equal to the last one
   *  applied touches nothing. */
  apply: (next: Attention) => void;
}

export function createAttention(env: AttentionEnv): AttentionSurfaces {
  // Last applied, so each sink is called only on a real change. This matters
  // beyond cost: the document title doubles as the browser-tab label and the
  // bookmark name, and re-assigning an icon href makes some browsers re-fetch it.
  let applied: Attention = NO_ATTENTION;
  let first = true;

  return {
    apply(next: Attention): void {
      const countChanged = first || next.count !== applied.count;
      const worstChanged = first || next.worst !== applied.worst;
      first = false;
      applied = next;

      if (countChanged) {
        env.titlePrefix(titlePrefixFor(next.count));
        // The badge takes the SAME number as the title, which is the whole
        // reason both read one fold: two surfaces disagreeing about how many
        // things want you is worse than either being absent.
        env.setBadge?.(next.count);
      }
      if (worstChanged) {
        env.setIcon?.(next.worst === "" ? null : cueIconName(next.worst));
      }
    },
  };
}

/** iconVariantHref rewrites an icon URL to its variant, by the convention the
 *  asset generator writes (gen-attention-icons.py, in a web-terminal-ui
 *  checkout): the `favicon` token of the filename gains `-<variant>`, so
 *  `/favicon.svg` becomes `/favicon-input.svg` and `/favicon-32x32.png` becomes
 *  `/favicon-input-32x32.png`. The extension is preserved, so each link keeps
 *  pointing at its own format and no `type` attribute has to change.
 *
 *  A URL whose filename does not start with `favicon` returns null, which leaves
 *  that link alone rather than pointing it at a 404. */
export function iconVariantHref(href: string, variant: string): string | null {
  const match = /(^|\/)favicon(?=[-.])/.exec(href);
  if (match === null) {
    return null;
  }
  const at = match.index + match[0].length;
  return `${href.slice(0, at)}-${variant}${href.slice(at)}`;
}

// ---------------------------------------------------------------------------
// The acknowledgement store
// ---------------------------------------------------------------------------

/** localStorage key for the cues this reader has already SEEN: chat id -> the
 *  dot state that was acknowledged.
 *
 *  Its own key rather than a field in the `vibekit.ui-state` blob: that blob is
 *  the window's ARRANGEMENT (tab order, pins, panel sizes) written on structural
 *  change, and this is written whenever a cue is observed. Different cadence,
 *  different subject.
 *
 *  It HAS to be remembered, and the reason reaches vibekit by a different route
 *  than the reference. `turn_done`, `turn_failed` and `agent_status` are client
 *  latches rebuilt from server state: `handlers/system.ts` refetches the active
 *  chat on `transport:gap`, the SSE connect replay re-delivers a `turn_state`
 *  per busy chat, and the connect handshake re-pushes every unanswered decision
 *  (which is what makes `input` true again). Without this, a dismissed cue came
 *  back on the next page load — and, since the replay runs on every reconnect,
 *  on a phone simply returning to a backgrounded page.
 *
 *  Per device, like the tab arrangement and for the same reason: "I have seen
 *  this" is a property of the READER, not of the chat, so a phone acknowledging a
 *  finished turn must not blank the count on the desktop watching the same
 *  server. That is why it needs no server API.
 *
 *  Keyed per chat rather than as one latest-wins slot: several chats can hold a
 *  latched status at once (each has its own independent bridge, and
 *  `handlers/turn.ts` latches per chat id), so a single-slot acknowledgement
 *  would let every other one re-raise the count on the next load. */
export const CUE_SEEN_KEY = "vibekit.cue-seen";

/** Bound on the acknowledgement map. Every key is a chat with an open tab, so a
 *  real map is nowhere near this, and a corrupted or hostile stored value cannot
 *  make the restore path do unbounded work. */
export const MAX_PERSISTED_CUE_SEEN = 200;

/** parseCueSeen reads stored acknowledgements into a clean map. Anything it
 *  cannot trust is dropped (or, for a broken document, all of it): a lost
 *  acknowledgement only re-lights a cue the reader can dismiss again, so
 *  degrading to "nothing acknowledged" is always safe. Pure, so it is testable
 *  without a storage backend; the caller owns the read and its try/catch. */
export function parseCueSeen(raw: string | null): Map<string, CueStatus> {
  const out = new Map<string, CueStatus>();
  if (raw === null || raw === "") {
    return out;
  }
  let data: unknown;
  try {
    data = JSON.parse(raw);
  } catch {
    return out;
  }
  // Arrays and null are typeof "object" too, and neither is a cue map.
  if (typeof data !== "object" || data === null || Array.isArray(data)) {
    return out;
  }
  for (const [id, status] of Object.entries(data)) {
    if (id === "" || typeof status !== "string" || !isCueStatus(status)) {
      continue;
    }
    out.set(id, status);
    if (out.size >= MAX_PERSISTED_CUE_SEEN) {
      break;
    }
  }
  return out;
}

/** serializeCueSeen encodes acknowledgements for storage. The live map is kept
 *  within the cap by `mark` below, so this does not truncate: a silent truncation
 *  here would drop whichever entries the parser happened to read last, which is
 *  the opposite of what an eviction should discard. */
export function serializeCueSeen(seen: ReadonlyMap<string, CueStatus>): string {
  return JSON.stringify(Object.fromEntries(seen));
}

/** Where acknowledgements are kept. Injected so the store is testable with no
 *  localStorage, and so the quota/disabled-storage try/catch has one home. */
export interface CueSeenStorage {
  read: () => string | null;
  write: (raw: string) => void;
}

export interface CueSeen {
  /** The live map, for the fold to read. */
  map: () => ReadonlyMap<string, CueStatus>;
  /** Record that this reader has seen `id` holding `status`. A non-cue status is
   *  not an acknowledgeable event, so it is ignored rather than stored. */
  mark: (id: string, status: string) => void;
  /** Drop an acknowledgement, so the chat's NEXT cue is a fresh one. */
  forget: (id: string) => void;
}

export function createCueSeen(storage: CueSeenStorage): CueSeen {
  const seen = parseCueSeen(storage.read());
  return {
    map: () => seen,
    mark(id: string, status: string): void {
      if (!isCueStatus(status) || seen.get(id) === status) {
        return;
      }
      seen.set(id, status);
      // Evict oldest-first so the live map obeys the same cap the parser does.
      // A chat closed while this page was open is pruned by the close hook, but
      // one that vanished while the page was CLOSED leaves an entry nothing else
      // collects; unbounded, that would eventually push the map past the cap and
      // make the parser discard whatever it read last — dropping fresh
      // acknowledgements to keep dead ones.
      while (seen.size > MAX_PERSISTED_CUE_SEEN) {
        const oldest = seen.keys().next().value;
        if (oldest === undefined) {
          break;
        }
        seen.delete(oldest);
      }
      storage.write(serializeCueSeen(seen));
    },
    forget(id: string): void {
      if (seen.delete(id)) {
        storage.write(serializeCueSeen(seen));
      }
    },
  };
}

// ---------------------------------------------------------------------------
// The controller: every rule, still DOM-free
// ---------------------------------------------------------------------------

/** Everything the controller needs from outside itself. */
export interface AttentionWiring {
  /** The chat tabs and their current dot states. */
  candidates: () => readonly CueCandidate[];
  /** The chat the reader is looking at, "" for none. */
  activeChatID: () => string;
  /** Whether the page is in front of the reader at all. */
  pageVisible: () => boolean;
  /** The chat ids whose sidebar row the reader can actually SEE right now. */
  rowsInView: () => readonly string[];
  storage: CueSeenStorage;
  surfaces: AttentionSurfaces;
}

export interface AttentionController {
  /** Apply the observation rules to current state, then re-render the surfaces.
   *  The recompute funnel's target; idempotent, so calling it more often than
   *  necessary costs a loop over a handful of tabs. */
  refresh: () => void;
  /** Acknowledge what the reader can see: the chat on screen, plus every sidebar
   *  row actually in view. */
  ackSeen: () => void;
  /** Acknowledge the chat the reader just switched to. */
  ackSwitch: (chatID: string) => void;
  /** Drop a departed chat's acknowledgement. */
  forget: (chatID: string) => void;
}

export function createAttentionController(wiring: AttentionWiring): AttentionController {
  const seen = createCueSeen(wiring.storage);

  /** refresh is the RAISE rule, and it is two observations plus the fold.
   *
   *  A cue raises when the chat is NOT (active AND page-visible) and its state is
   *  an unacknowledged cue. Both halves of that condition are required, for the
   *  reason handlers/turn.ts states for `isWatching`: keyed on "is this the active
   *  chat" alone it swallows the cue of the very chat the reader left running,
   *  which is the single-chat case — one chat is necessarily the active one — and
   *  precisely the case these surfaces exist for. So a cue latching on a HIDDEN
   *  page raises them, and is acknowledged when the reader comes back.
   *
   *  Both rules are safe to run as a SWEEP rather than on a transition, which is
   *  what lets this be the funnel: the un-acknowledge is idempotent, and
   *  observing a watched chat's cue is a fact about the present, not an event.
   *
   *  Which branches this leaves, given what vibekit already does structurally:
   *  `handlers/turn.ts` skips `setTurnDone` for a watched chat, so `done` from
   *  the `turn_ended` transport verdict never latches on a chat the reader is
   *  looking at and is pre-acknowledged by construction. Its OTHER producer,
   *  `agent_status === "completed"`, latches regardless — as do `input` (an
   *  unanswered decision), `waiting` (`waiting_on_user`) and `failed`
   *  (`turn_failed`). So all four cues need the acknowledgement path; `done` just
   *  reaches it less often. */
  function refresh(): void {
    const watched = wiring.pageVisible() ? wiring.activeChatID() : "";
    for (const candidate of wiring.candidates()) {
      if (candidate.status === "") {
        // NO INFORMATION, not a state. `TabSpec.dotStatus` is absent on a tab
        // whose dot has never been written (the tick between `openTab` and the
        // store effect's first sweep), and `tabStatusFor` answers "" for a chat
        // the store does not know. Neither is evidence that a cue ENDED, so
        // neither may drop an acknowledgement — doing so re-lit a dismissed cue
        // on every reload, because the restore opens the tab before the sweep
        // paints it.
        continue;
      }
      if (!isCueStatus(candidate.status)) {
        // The state moved off a cue (a new turn started, a decision was
        // answered, the chat went idle), so the acknowledgement has done its job
        // and the NEXT cue must be fresh. Also what keeps the map from holding an
        // entry per chat forever.
        seen.forget(candidate.id);
      } else if (candidate.id === watched) {
        seen.mark(candidate.id, candidate.status);
      }
    }
    wiring.surfaces.apply(summarize(wiring.candidates(), seen.map()));
  }

  return {
    refresh,

    ackSeen(): void {
      if (!wiring.pageVisible()) {
        return;
      }
      const inView = new Set(wiring.rowsInView());
      for (const candidate of wiring.candidates()) {
        if (inView.has(candidate.id)) {
          seen.mark(candidate.id, candidate.status);
        }
      }
      // The ACTIVE chat is acknowledged whether or not its row is in view — its
      // transcript is what fills the screen, which is the case the mobile drawer
      // hides the row for — and refresh's watched-chat rule is what does it, since
      // this method has already established that the page is visible. Naming the
      // active chat in the loop above as well was dead: it could only ever mark
      // what the line below marks anyway.
      refresh();
    },

    ackSwitch(chatID: string): void {
      // Switching to a chat means looking at it. Gated on page visibility
      // because the boot restore activates a tab too, and a page restored into a
      // background browser tab must not have its cue swallowed by that.
      //
      // Not covered by refresh's watched-chat rule: the tab store announces the
      // switch from inside its own emit, BEFORE `store.setActive` runs, so
      // `activeChatID()` still names the outgoing chat at this moment.
      if (wiring.pageVisible()) {
        const candidate = wiring.candidates().find((c) => c.id === chatID);
        if (candidate !== undefined) {
          seen.mark(candidate.id, candidate.status);
        }
      }
      refresh();
    },

    forget(chatID: string): void {
      seen.forget(chatID);
      refresh();
    },
  };
}

// ---------------------------------------------------------------------------
// The browser binding — the only part of this file that touches globals
// ---------------------------------------------------------------------------

/** Is the page in front of the reader? visibilityState is the only reliable
 *  test: document.hasFocus() is false for a visible-but-unfocused window, where
 *  the chat IS on screen. Same signal notify.ts reads, for the same decision. */
export function pageVisible(): boolean {
  return document.visibilityState !== "hidden";
}

/** Is the reader looking at this chat right now? The active chat with the page in
 *  front of them, which is the one case where a finished-turn mark says nothing
 *  the transcript is not already showing.
 *
 *  Here rather than in the store because the store just holds the latch; this is
 *  the layer that knows what is on screen. `handlers/turn.ts` reads it to decide
 *  whether to latch `turn_done`, and the refresh pass above reads the same rule
 *  to decide whether a cue is already observed — one definition, so the latch and
 *  the acknowledgement cannot disagree. */
export function isWatching(chatID: string): boolean {
  return chatID === getActiveId() && pageVisible();
}

/** Whether an element is presented to the reader at all: not hidden by CSS, and
 *  somewhere inside the viewport.
 *
 *  Both tests, and the second is not a fallback for the first. checkVisibility()
 *  answers the CSS questions (display:none, visibility:hidden,
 *  content-visibility) and says nothing about geometry, so a drawer parked at
 *  `translateX(-100%)` is fully "visible" to it — and that drawer is the case
 *  that matters most here. Between them the closed drawer, the mobile breakpoint
 *  and any future desktop collapse are all covered without this code knowing that
 *  any of them exist. */
function surfaceVisible(el: HTMLElement): boolean {
  const probe = (el as { checkVisibility?: () => boolean }).checkVisibility;
  if (typeof probe === "function" && !probe.call(el)) {
    return false;
  }
  const rect = el.getBoundingClientRect();
  return (
    rect.width > 0 &&
    rect.height > 0 &&
    rect.right > 0 &&
    rect.bottom > 0 &&
    rect.left < window.innerWidth &&
    rect.top < window.innerHeight
  );
}

/** The chat ids whose sidebar row the reader can actually see: the sidebar itself
 *  presented, and the row's box FULLY inside `#tab-list`'s own box.
 *
 *  Fully, not partially, and the asymmetry is deliberate. A half-clipped row
 *  stays unacknowledged, so the count occasionally lingers over a row the reader
 *  could in fact read — the opposite error would blank a cue nobody ever saw,
 *  which is the one failure this whole feature exists to prevent. Lingering is
 *  dismissible; a lost cue is not recoverable.
 *
 *  The containment test is transform-invariant, which is what makes it correct
 *  while the mobile drawer is still animating in: the rows and the clip box sit
 *  in the same transformed subtree, so a translate moves both identically. */
export function rowsInView(sidebar: HTMLElement, tabList: HTMLElement): string[] {
  if (!surfaceVisible(sidebar)) {
    return [];
  }
  const clip = tabList.getBoundingClientRect();
  const ids: string[] = [];
  for (const row of tabList.querySelectorAll<HTMLElement>("[data-tab-id]")) {
    const id = row.dataset["tabId"] ?? "";
    if (id === "") {
      continue;
    }
    const rect = row.getBoundingClientRect();
    if (rect.height > 0 && rect.top >= clip.top && rect.bottom <= clip.bottom) {
      ids.push(id);
    }
  }
  return ids;
}

/** localStorage-backed acknowledgements, with the two try/catch guards a
 *  disabled or full store needs. Module-private: `initAttention` is the only
 *  caller, and the tests reach it through the key rather than the constructor. */
function browserCueSeenStorage(): CueSeenStorage {
  return {
    read(): string | null {
      try {
        return localStorage.getItem(CUE_SEEN_KEY);
      } catch {
        return null; // storage unavailable (private mode / disabled)
      }
    },
    write(raw: string): void {
      try {
        localStorage.setItem(CUE_SEEN_KEY, raw);
      } catch {
        // ignore quota / disabled storage, like ui-state.save
      }
    },
  };
}

/** browserAttentionEnv binds the three sinks to the real browser. Every
 *  capability decision is made HERE, once, so the core never probes for one. */
export function browserAttentionEnv(): AttentionEnv {
  // The base title is captured ONCE, from what the document was served with, so
  // the sink composes prefix + base rather than asserting a constant over it.
  // The retired `setBadge` hardcoded its own copy of the <title> literal with
  // nothing pinning the two together, so editing static/index.html's title lost
  // silently to the first count write. Capturing it deletes the second literal
  // instead of adding a test to guard it. Reading document.title back on each
  // write would be the bug this avoids: the current value already carries a
  // prefix, so repeated writes would compound.
  const base = document.title;
  const env: AttentionEnv = {
    titlePrefix: (text: string): void => {
      // No guard here: createAttention only calls this when the count changed,
      // and this is the only writer of document.title in the app.
      document.title = text + base;
    },
  };

  // The Badging API, read through `unknown` for the same reason notify.ts reads
  // Notification that way: it is absent on most browsers and must degrade rather
  // than be asserted. Installed apps only — a badge lives on an app icon, which
  // exists only after the app is installed.
  const nav: unknown = globalThis.navigator;
  const setAppBadge = (nav as { setAppBadge?: unknown } | undefined)?.setAppBadge;
  const clearAppBadge = (nav as { clearAppBadge?: unknown } | undefined)?.clearAppBadge;
  if (typeof setAppBadge === "function") {
    env.setBadge = (count: number): void => {
      // Always a NUMBER, never the spec's bare flag form: iOS renders nothing at
      // all for `setAppBadge()` with no argument. Zero clears, via clearAppBadge
      // where it exists (the documented way) and setAppBadge(0) otherwise.
      //
      // Both return promises that reject on an unsupported platform, so the
      // rejection is swallowed: a badge the OS declines to paint is not an error
      // this page can act on, and an unhandled rejection inside a status sweep
      // surfaces as a page fault. A synchronous throw is the same non-event.
      try {
        const call =
          count > 0
            ? (setAppBadge as (n: number) => unknown).call(nav, count)
            : typeof clearAppBadge === "function"
              ? (clearAppBadge as () => unknown).call(nav)
              : (setAppBadge as (n: number) => unknown).call(nav, 0);
        void Promise.resolve(call).catch(() => {
          /* an OS that will not paint a badge is a title-only OS */
        });
      } catch {
        /* a synchronous throw is the same non-event */
      }
    };
  }

  // EVERY icon link, not one of them: which link a browser picks differs (Chrome
  // prefers the SVG), so mutating a single element is unreliable.
  // apple-touch-icon is deliberately NOT matched — `rel~="icon"` does not select
  // it — because the OS caches that icon when the app is installed and a swap
  // cannot reach it.
  const links = [...document.querySelectorAll<HTMLLinkElement>('link[rel~="icon"]')];
  // Each original captured once, and every variant computed from the ORIGINAL
  // rather than from the current value, so repeated swaps cannot compound.
  const originals = new Map<HTMLLinkElement, string>();
  for (const link of links) {
    originals.set(link, link.getAttribute("href") ?? "");
  }
  if (links.length > 0) {
    env.setIcon = (variant): void => {
      for (const link of links) {
        const original = originals.get(link) ?? "";
        if (variant === null) {
          link.setAttribute("href", original);
          continue;
        }
        const next = iconVariantHref(original, variant);
        if (next !== null) {
          link.setAttribute("href", next);
        }
      }
    };
  }

  return env;
}

/** initAttention wires the controller to the app and returns its disposer.
 *
 *  THE RECOMPUTE FUNNEL is `subscribeTabCues` (tabs.ts), which watches the two
 *  disjoint write paths that can change the fold's input: `stateVersion` for the
 *  chat-tab SET (every list mutation ends in emit()) and `dotVersion` for every
 *  dot write (setTabStatus / setTabDirty deliberately do not emit). Covering one
 *  and not the other is how the count goes stale after a chat is closed.
 *
 *  The acknowledgement gestures are keyed on WHAT THE READER CAN SEE, which is
 *  one rule where the reference has two. It has no horizontal tab strip to hide
 *  and no switcher tray to expand: the tab list is a VERTICAL list inside a
 *  sidebar that is persistent on desktop and a full-viewport drawer on mobile, so
 *  "returning to the page" and "opening the drawer" are the same gesture in two
 *  layouts. But `#tab-list` scrolls, and the forgotten background chat is
 *  precisely the one likely to be below the fold, so a wholesale clear on either
 *  gesture would blank a cue the reader never saw. */
export function initAttention(): () => void {
  const controller = createAttentionController({
    candidates: cueCandidates,
    activeChatID: getActiveId,
    pageVisible,
    rowsInView: () => rowsInView($.sidebar, $.tabList),
    storage: browserCueSeenStorage(),
    surfaces: createAttention(browserAttentionEnv()),
  });

  const stop: (() => void)[] = [subscribeTabCues(controller.refresh)];

  stop.push(
    onBus(BUS_TAB_CHANGED, (e) => {
      if (e.kind === "chat") {
        controller.ackSwitch(e.to);
      }
    }),
  );

  // A chat that goes away takes its acknowledgement with it. closeTab is the only
  // production path that removes a tab from the store, so one hook there is the
  // whole coverage; a non-chat id is a no-op because the map has no entry for it.
  setOnTabClosed(controller.forget);

  const onVisible = (): void => {
    controller.ackSeen();
  };
  document.addEventListener("visibilitychange", onVisible);
  stop.push(() => {
    document.removeEventListener("visibilitychange", onVisible);
  });

  // The drawer opening is a CLASS toggle, which is not an event, so it is watched
  // rather than called: an observer on the attribute covers the menu button, the
  // edge swipe (platform.ts) and any future gesture by construction, where a call
  // at each toggle site would be a rule living in several places — there are two
  // sites that open it today against five that close it, and the button's is a
  // `toggle`, which would also need a did-it-end-open check.
  //
  // Paired with transitionend because the mutation lands BEFORE the transform
  // animates: at that instant the drawer's rect is still off-viewport, so the
  // geometric test correctly declines and the settled event is what acknowledges.
  // The pair covers both directions honestly — on a CLOSE the mutation fires
  // while the rows are still on screen (the reader was just looking at them, so
  // acknowledging is right) and the settled event then finds nothing in view —
  // and it covers a platform with the transition disabled, where the mutation's
  // rect is already exact. Scoped to the sidebar itself: transitionend bubbles,
  // and every tab row animates its own background on hover.
  const onSettled = (e: TransitionEvent): void => {
    if (e.target === $.sidebar) {
      controller.ackSeen();
    }
  };
  $.sidebar.addEventListener("transitionend", onSettled);
  stop.push(() => {
    $.sidebar.removeEventListener("transitionend", onSettled);
  });

  const drawer = new MutationObserver(() => {
    controller.ackSeen();
  });
  drawer.observe($.sidebar, { attributes: true, attributeFilter: ["class"] });
  stop.push(() => {
    drawer.disconnect();
  });

  controller.refresh();

  return () => {
    for (const dispose of stop) {
      dispose();
    }
  };
}
