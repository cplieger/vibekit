// ---------------------------------------------------------------------------
// The run bar: one line per LIVE workflow run the active chat launched, in the
// bottom bar between the interaction dock and the steer stack.
//
// WHERE IT SITS, AND WHY. A sibling of `.prompt-box` inside `#prompt-form`,
// sharing the band's measure with the three regions around it. The ordering rule
// is by SCOPE: a dock card blocks the turn, so it outranks everything; a run
// spans turns, so it outranks the steer stack, which is scoped to the turn
// running now; the box is what comes next. The bar grows upward and
// `#messages-wrap` shrinks by exactly its height, so it covers nothing — the same
// mechanical property `26-dock.css` states for the dock.
//
// WHY IT EXISTS. `run_workflow` returns as soon as the run is created, so the
// launching turn ends while the run carries on for minutes: the turn's own card
// scrolls away, the turn folds, and the chat's dot goes green. Nothing in the
// composer said a run was still going. This is that surface, and it is
// PERSISTENT in both directions a reader cares about — across turns, because it
// is not anchored in the transcript, and across a reload, because the inventory
// behind it is rebuilt from `GET /api/runs/live` at boot.
//
// It REPLACED a duplicate run card mounted into every folded turn's face. That
// duplicate was lossy (a generic label, no step bodies) and cost one refetch plus
// one clock hold per folded turn, and it could only ever speak for a run whose
// launching turn was still resident.
//
// SCOPE IS THE ACTIVE CHAT'S RUNS, and nothing else. A run launched from another
// chat is already surfaced by its own tab's activity dot (`run-dots.ts`), and a
// PARENTLESS run has no launching chat at all, so `liveRunIDsForChat` excludes
// both. Every chat shows its own list.
//
// A PURE PROJECTION of `run-store.ts` plus the dock's ask queue. It fetches
// nothing of its own except the one invalidation below, records nothing, and
// decides nothing about a run: `render` reads store state and paints. A click
// NAVIGATES to that run's own tab through `openRunView` — the app's one manual
// door into a run tab — rather than scrolling the transcript or unfolding a turn.
//
// THE ROW'S CLICK NEVER BRANCHES ON STATE, including a paused run holding an
// unanswered ask. Four reasons, strongest first: the dock renders that ask and
// `#decision-dock` is the sibling immediately above this bar, so "route to the
// dock" would move the reader a few pixels to a card they are already looking at;
// one gesture must mean one thing, and a destination that depends on state the
// pointer cannot see is a gesture nobody can learn; the run tab renders the ask
// too, so the tab is the ask PLUS the step tree and the run's verbs; and the row
// already says why to go, because an unanswered ask takes the `input` state's
// yellow `?` and its own word. Marking is the row's job, answering is the dock's,
// navigating is the click's.
// ---------------------------------------------------------------------------

import { el, computed, effect, touch, untracked } from "@cplieger/reactive";
import { announce } from "@cplieger/ui-primitives/announce";
import { $ } from "./dom.js";
import { watchActiveId } from "./store.js";
import {
  liveRunIDsForChat,
  runCounters,
  runElapsedMs,
  runIsLive,
  runState,
  invalidateRun,
  type RunState,
} from "./run-store.js";
import { runPendingAsks } from "./decision-dock.js";
import { holdRunClock, releaseRunClock, type RunClockHolder } from "./messages-blocks.js";
import { openRunView } from "./run-view.js";
import {
  STATE_WORD,
  paintStateMark,
  stateOf,
  withAsk,
  type ExecState,
} from "./exec-view/status.js";
import { formatElapsed } from "./strings.js";

/** The state a row paints when nothing has been fetched for its run yet.
 *
 *  NOT `stateOf(undefined)`, which is `pending` and reads "not started" — a run in
 *  the live inventory has demonstrably started, so that word would be false. The
 *  app's own precedent for "we do not know yet" is `run-dots.ts`, which paints
 *  NOTHING rather than claiming a state: the row shows its name, a reserved empty
 *  glyph box and no word until the first fetch lands. */
const UNKNOWN_STATE = "unknown";

/** The label a run with no fetched state carries. The store's `runLabelOf` answers
 *  `""` there, and a row with no name at all is unclickable in practice. */
const FALLBACK_NAME = "Workflow run";

/** A row's clock hold. The element is re-pointed on every render rather than
 *  re-registered, so the refcount in `messages-blocks.ts` sees one holder per run
 *  for as long as the bar shows it. */
interface Hold extends RunClockHolder {
  clock: HTMLElement | null;
}

let bound = false;
let prevCount = 0;
let prevChatID = "";
const holds = new Map<string, Hold>();
/** The render effect's disposer, held only so `_resetRunBarForTest` can stop it.
 *  Production never tears the bar down — it lives as long as the page. */
let stopRender: (() => void) | null = null;

/** Wire the reactive render. Idempotent. Called once from app.ts. */
export function initRunBar(): void {
  if (bound) {
    return;
  }
  bound = true;
  const bar = $.runBar;
  const sig = computed(() => key());
  stopRender = effect(() => {
    touch(sig);
    // The computed IS the dedupe. A tracked read inside `render` would subscribe
    // this effect to every run cell directly, so a `run_progress` refetch that did
    // not move the key would still re-run `replaceChildren` and re-fire every row's
    // @starting-style entry animation.
    untracked(() => {
      render(bar);
    });
  });
}

/** Everything the bar's CONTENT depends on, as one string so the computed dedupes
 *  by value.
 *
 *  `watchActiveId()` rather than `activeSession.value`: the bar needs only the chat
 *  id, and `activeSession` re-derives on every streaming chunk, so reading it would
 *  re-evaluate this computed per chunk for nothing.
 *
 *  The elapsed clock is deliberately NOT in the key. That is the tick's job, and a
 *  per-second key would rebuild every row once a second. */
function key(): string {
  const chatID = watchActiveId();
  const parts: string[] = [chatID];
  for (const id of liveRunIDsForChat(chatID)) {
    const st = runState(id);
    const asks = runPendingAsks(id);
    const c = runCounters(st);
    parts.push(
      [
        id,
        st?.status ?? "",
        st?.runLabel ?? "",
        st?.workflowName ?? "",
        String(asks.count),
        String(c.total),
        String(c.done),
        String(c.current),
      ].join("\u0002"),
    );
  }
  return parts.join("\u0001");
}

/** The runs this bar shows for a chat: the live inventory minus anything the store
 *  can PROVE has settled.
 *
 *  An id whose cell is still `undefined` is kept — nothing has been fetched yet,
 *  which is the honest starting case. A fetched non-live state still sitting in the
 *  inventory means a `run_finished` this client missed, and a settled row in a
 *  live-runs bar is the one wrong thing the bar can say. */
function rows(chatID: string): string[] {
  return liveRunIDsForChat(chatID).filter((id) => {
    const st = runState(id);
    return st === undefined || runIsLive(st);
  });
}

function render(bar: HTMLUListElement): void {
  const chatID = watchActiveId();
  const ids = rows(chatID);

  bar.replaceChildren();
  if (ids.length === 0) {
    bar.classList.add("hidden");
    // Reset the announce baseline so arriving at a chat that already has runs reads
    // them out fresh, while the empty case stays silent.
    reconcileHolds([]);
    prevCount = 0;
    prevChatID = chatID;
    return;
  }
  bar.classList.remove("hidden");
  // BEFORE the rows, not after: `buildRow` points its clock span at this run's hold,
  // so a hold created afterwards would leave the FIRST render's clock unaddressable —
  // which is every render for a run whose state was already in the store when the bar
  // started showing it, and the tick would then never write to it.
  reconcileHolds(ids);
  for (const id of ids) {
    bar.appendChild(buildRow(id, chatID));
  }

  // Announce only on the same chat, and only when the COUNT moves — a chat switch
  // is not news, and a step advancing inside a run the reader already knows about
  // is not either.
  if (chatID === prevChatID && ids.length !== prevCount) {
    announce(
      ids.length === 1
        ? "1 workflow run in progress"
        : `${String(ids.length)} workflow runs in progress`,
    );
  }
  prevCount = ids.length;
  prevChatID = chatID;
}

/** One row: an `<li>` carrying the state, holding the `<button>` that opens the
 *  run's own tab. */
function buildRow(id: string, chatID: string): HTMLElement {
  const st = runState(id);
  const asks = runPendingAsks(id);
  const state = execStateOf(st, asks.count > 0);
  const name = runName(st);
  const word = state === UNKNOWN_STATE ? "" : STATE_WORD[state];
  const steps = stepText(st);

  const glyph = el("span", { className: "run-bar-glyph", "aria-hidden": "true" });
  if (state !== UNKNOWN_STATE) {
    // The one writer of a state's mark, so this row, the transcript's step rows and
    // the exec tree cannot spell one state three ways.
    paintStateMark(glyph, state);
  }

  const clock = el("span", { className: "run-bar-clock" }, elapsedText(st));

  const btn = el(
    "button",
    {
      type: "button",
      className: "run-bar-open",
      // The visible state is a glyph plus a word, both visual, so the name has to
      // carry it too. The step counter rides along because it is the row's other
      // non-textual claim about progress.
      "aria-label": accessibleName(name, word, steps),
    },
    glyph,
    el("span", { className: "run-bar-name" }, name),
    el("span", { className: "run-bar-state" }, word),
    el("span", { className: "run-bar-steps" }, steps),
    clock,
  );
  btn.addEventListener("click", () => {
    openRunView(id, name, chatID);
  });

  const row = el("li", { className: "run-bar-row", "data-state": state }, btn);
  // Re-point this run's hold at THIS render's clock span, so the shared tick writes
  // into the row that is actually on screen. The hold exists by construction — the
  // caller reconciles before it builds — and the guard is the type's, not a doubt.
  const hold = holds.get(id);
  if (hold !== undefined) {
    hold.clock = clock;
  }
  return row;
}

/** Fold a run's status and its ask onto ONE axis, the way the card's STEP rows and
 *  the exec tree do. The card's ROOT keeps two axes because its rail and its ask
 *  paint at the same time; a single line has one slot. */
function execStateOf(st: RunState | undefined, asking: boolean): ExecState | typeof UNKNOWN_STATE {
  if (st === undefined) {
    return UNKNOWN_STATE;
  }
  return withAsk(stateOf(st.status), asking);
}

/** The run's name, RAW. It is also the run TAB's name and the button's accessible
 *  name, and `.run-bar-name` ellipsizes the visible span responsively — so a length
 *  cut here would travel into the tab strip and into what a screen reader reads. */
function runName(st: RunState | undefined): string {
  const label = st?.runLabel ?? "";
  const name = label === "" ? (st?.workflowName ?? "") : label;
  return name === "" ? FALLBACK_NAME : name;
}

/** The step counter, in the card's own wording so the two surfaces agree. */
function stepText(st: RunState | undefined): string {
  const c = runCounters(st);
  if (c.total === 0) {
    return "";
  }
  return c.current > 0
    ? `step ${String(c.current)} of ${String(c.total)}`
    : `${String(c.done)} of ${String(c.total)}`;
}

function elapsedText(st: RunState | undefined): string {
  const ms = runElapsedMs(st);
  return ms > 0 ? formatElapsed(ms) : "";
}

function accessibleName(name: string, word: string, steps: string): string {
  const parts = [name];
  if (word !== "") {
    parts.push(word);
  }
  if (steps !== "") {
    parts.push(steps);
  }
  return `Open workflow run ${parts.join(", ")}`;
}

/** Join the shared 1s clock for every run on screen and leave it for every run that
 *  left. ONE holder per run for as long as the bar shows it, so the refcount in
 *  `messages-blocks.ts` is not churned by a repaint. */
function reconcileHolds(ids: readonly string[]): void {
  const wanted = new Set(ids);
  for (const [id, hold] of holds) {
    if (!wanted.has(id)) {
      releaseRunClock(id, hold);
      holds.delete(id);
    }
  }
  for (const id of ids) {
    const held = holds.has(id);
    if (!held) {
      const hold: Hold = {
        clock: null,
        tick(): void {
          // A tracked read is inert here: a setInterval callback runs outside any
          // effect's eval context, so nothing subscribes.
          if (hold.clock !== null) {
            hold.clock.textContent = elapsedText(runState(id));
          }
        },
      };
      holds.set(id, hold);
      holdRunClock(id, hold);
    }
    // The bar's one fetch, and its only non-projection act: when it starts showing a
    // run, and again whenever a rendered run has NO state to read. Both arms serve a
    // run nothing else will fetch — a PAUSED run emits no frames, and `runCardFor`'s
    // disposer forgets the cell of a run whose transcript card ages past TURNS_WARM
    // with no run tab open, leaving that row nameless and stateless for good. It
    // cannot loop: an unchanged cell does not move the computed's key.
    if (!held || runState(id) === undefined) {
      invalidateRun(id);
    }
  }
}

/** Stop the render effect, release every hold and forget the module state. For tests
 *  only: all of it is module state, and the browser project's module registry is
 *  URL-keyed, so `vi.resetModules()` does not re-evaluate this file.
 *
 *  Stopping the effect is the load-bearing half. Without it a suite that calls
 *  `initRunBar` per case accumulates one live effect per case, all rendering into the
 *  same element — so a row rebuilt by a LATER effect papers over whatever the first
 *  one got wrong, and an assertion about a single render can no longer fail. */
export function _resetRunBarForTest(): void {
  stopRender?.();
  stopRender = null;
  for (const [id, hold] of holds) {
    releaseRunClock(id, hold);
  }
  holds.clear();
  bound = false;
  prevCount = 0;
  prevChatID = "";
}
