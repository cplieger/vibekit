// ---------------------------------------------------------------------------
// History: previous chats and previous workflow runs, both sourced from KAS.
//
// This replaces a list of vibekit's OWN archived chat files. vibekit no longer
// archives anything — KAS owns the session inventory and the transcript, so
// this page is a picker over `GET /api/sessions` and opening a row is a
// `session/load`, which the replay projection turns into the transcript.
//
// It is the adoption of kiro-cli's own `--resume-picker` ("Interactively select
// a conversation to resume from this directory"), with a UI instead of an ANSI
// list. Two rules the server's provenance notes explain in full and this file
// depends on:
//
//   - A CHAT row already owned by a vibekit chat carries `chat_id`, so opening
//     it is just opening that chat. Without one it is adopted first, via
//     `resume_session`.
//   - A RUN row is not a session. Workflow runs come from a separate verb
//     because session/list's workflow rows are per-STEP (76 rows for one loop),
//     so a run opens the read-only run view rather than a chat.
// ---------------------------------------------------------------------------

import { toggleHistoryView, hasTab } from "./tabs.js";
import { onBus, BUS_RUNS_CHANGED } from "./bus.js";
import { el } from "@cplieger/reactive";
import { reconcile } from "./reconcile.js";
import { skeletonTiming } from "@cplieger/ui-primitives/skeleton";
import { loadSessions } from "./actions/chat.js";
import { registerCleanup } from "./actions/index.js";
import { openPreviousSession, openChatTab } from "./chat.js";
import { searchChats } from "./actions/chat-search.js";
import type { ChatSearchMatch } from "./chat-search-types.js";
import { openRunView } from "./run-view.js";
import { deleteRun } from "./actions/runs.js";
import { deleteChat as deleteChatAction } from "./actions/chat.js";
import { confirm } from "./confirm.js";
import { get } from "./store.js";
import { labelForMode } from "./roles.js";
import { applyOutcome } from "./tool-card.js";
import type { ToolRenderInfo } from "./tool-schema.js";
import { ICON_TAB_RUN, ICON_TRASH } from "./icons.js";
import { iconEl } from "./icon-el.js";
import { createSearchPopup } from "./search-popup.js";
import type { SearchPopup } from "./search-popup.js";
import { registerFind } from "./find-registry.js";
import type { ResumableSessionRow, WorkflowRunRow } from "./types.js";

/** A chat row and a run row share the list, so they share a shape. */
interface HistoryRow {
  /** Reconcile key. Prefixed per kind so a session and a run can never collide. */
  key: string;
  kind: "chat" | "run";
  title: string;
  updatedAt: number;
  /** Secondary line: the agent's focus for a chat. On a run, empty unless one of
   *  vibekit's run bounds stopped it — the one ending the glyph and the status
   *  slot cannot express between them (see END_REASON_TEXT). */
  detail: string;
  status: string;
  /** The verdict this row states as a glyph, or null when there is none to
   *  state: any chat, an agent-parented run, and a run that is still moving. */
  outcome: RunVerdict | null;
  /** The row's third line: what this conversation or run WAS, in facts. Empty
   *  entries are dropped by the builder, so a chat vibekit knows nothing about
   *  renders two lines like before rather than an empty strip. */
  facts: string[];
  session?: ResumableSessionRow;
  run?: WorkflowRunRow;
}

/** The facts line for a chat row, from the chat record this client already holds.
 *
 *  Every field is read from the STORE rather than added to `/api/sessions`, and
 *  that is the whole reason this is cheap: `/api/chats` already carries the header
 *  for every chat (model, mode, usage, message count) and `loadList` has already
 *  put it in the store, so a richer row costs no request and no new wire field.
 *  KAS's session row carries none of it — no usage, no model, no message count —
 *  which is why the join happens here and not on the server.
 *
 *  A chat the store does not know yields an EMPTY list rather than placeholders:
 *  "unknown model · 0 turns" reads as fact and would be a lie.
 *
 *  Deliberately NOT here: lines changed. That total is not on the chat header —
 *  `changed_files` is stamped per TURN on each final assistant message, and the
 *  header read deliberately token-skips the message array without decoding it, so
 *  summing churn would mean decoding every message of every chat on every poll.
 *  It needs a counter maintained at turn end and persisted on the chat; see the
 *  note in vibekit-acp.md. */
function chatFacts(chatID: string): string[] {
  const s = get(chatID);
  if (s === undefined) {
    return [];
  }
  const facts: string[] = [];
  if (s.model !== "") {
    facts.push(s.model);
  }
  if (s.current_mode_id !== "") {
    facts.push(labelForMode(s.current_mode_id, s.available_modes));
  }
  // Turns is the agent's own count and messages is what is on disk; they answer
  // different questions ("how long a conversation" vs "how much is stored"), and
  // a chat resumed across sessions can have turns reset while messages persist.
  const turns = s.usage.turn_count;
  if (turns > 0) {
    facts.push(`${String(turns)} ${turns === 1 ? "turn" : "turns"}`);
  }
  if (s.message_count > 0) {
    facts.push(`${String(s.message_count)} msg`);
  }
  // Credits only when metered: a 0.00 on every unmetered row is noise, and this
  // is the one number that answers "what did that conversation cost".
  if (s.usage.credits > 0) {
    facts.push(`${s.usage.credits.toFixed(2)} cr`);
  }
  return facts;
}

/** The facts line for a run row. KAS's run inventory is thin by comparison, so
 *  this is the run's shape rather than its cost: how long it took, and the status
 *  word when the glyph is not already carrying it. */
function runFacts(r: WorkflowRunRow): string[] {
  const facts: string[] = [];
  const started = r.started_at ?? 0;
  const ended = r.updated_at;
  if (started > 0 && ended > started) {
    facts.push(formatDuration(ended - started));
  }
  return facts;
}

/** A coarse duration, because the reader wants an order of magnitude rather than
 *  a stopwatch: seconds under a minute, minutes under an hour, then hours. */
function formatDuration(ms: number): string {
  const secs = Math.round(ms / 1000);
  if (secs < 60) {
    return `${String(secs)}s`;
  }
  const mins = Math.round(secs / 60);
  if (mins < 60) {
    return `${String(mins)}m`;
  }
  const hours = Math.floor(mins / 60);
  return `${String(hours)}h ${String(mins % 60)}m`;
}

/** The run statuses that carry a verdict. Two are `ToolStatus` members and the
 *  third is the member `applyOutcome` was widened by, so these pass straight
 *  into the shared outcome vocabulary with no translation table here. */
type RunVerdict = "completed" | "failed" | "aborted";

/** How a bounded termination reads. The keys are the server's vocabulary
 *  (vibekit.WorkflowRun.EndReason); the sentences are the reader's.
 *
 *  A run stopped by one of vibekit's own bounds is the one ending KAS's status
 *  cannot describe: both bounds terminate through the same cancel a person uses,
 *  so the status is `aborted` for a backstop and for a click alike. This is where
 *  the difference is stated. */
const END_REASON_TEXT: Readonly<Record<string, string>> = {
  overran: "stopped: it ran past its time limit",
  step_cap: "stopped: a step ran past its turn limit",
  orphaned: "stopped: the server restarted while it was running",
};

/** A run's verdict, or null for a run with none to state.
 *
 *  Exhaustive over `RUN_STATUSES` (run-controls.ts): `running` and `paused`
 *  return null because their live status already renders in the status slot and
 *  a verdict is a claim only a settled run can make. An unknown status returns
 *  null for a different reason — it degrades to the status word rather than
 *  being guessed into a green check.
 *
 *  A RECOGNISED `end_reason` OUTRANKS the status, and has to: a bound cancels the
 *  run, so KAS reports it `aborted` at best and `running` if the frame has not
 *  landed yet, and a row that reads "running" for a run vibekit already stopped
 *  is the lie the field exists to remove. Recognised rather than merely non-empty,
 *  so one vocabulary decides both the sentence and the verdict: an unknown value
 *  degrades to the status word rather than repainting a completed run as aborted
 *  with nothing on the row to explain why. */
function runVerdict(status: string, endReason = ""): RunVerdict | null {
  if (END_REASON_TEXT[endReason] !== undefined) {
    return "aborted";
  }
  switch (status) {
    case "completed":
    case "failed":
    case "aborted":
      return status;
    default:
      return null;
  }
}

/** What `applyOutcome` needs from a caller that has no tool call behind it, the
 *  same stub shape `messages-tools.ts` passes on its own DOM-only path. Two
 *  fields reach the output and both matter: an empty `fileBasename` keeps the
 *  accessible name to the row's own label, and a null `denial` keeps the row
 *  from being repainted as a policy refusal. */
const ROW_RENDER_INFO: ToolRenderInfo = {
  kind: "other",
  writesFile: false,
  filePath: "",
  fileBasename: "",
  diffSources: null,
  mcp: null,
  disclosed: null,
  denial: null,
};

/** Whether a session's chat is open in a tab ON THIS DEVICE.
 *
 *  Ownership is the server's fact and travels as `chat_id`; "open here" is this
 *  device's, held in localStorage, which is why the predicate lives on the
 *  client and reuses the tab store's own `hasTab` rather than a second one. A
 *  chat tab's id IS its chat id, so no mapping is needed. */
function isOpenHere(s: ResumableSessionRow): boolean {
  const chatID = s.chat_id ?? "";
  return chatID !== "" && hasTab("chat", chatID);
}

function toRows(sessions: ResumableSessionRow[], runs: WorkflowRunRow[]): HistoryRow[] {
  const rows: HistoryRow[] = [];
  for (const s of sessions) {
    // A chat already open here is not history: its tab is one click away in the
    // strip, so listing it offers a second door to a room the user is standing
    // in. An owned-but-CLOSED session stays listed — reopening one is what this
    // page exists for. Filtered here rather than at build time so the dropped
    // key never reaches reconcile or the click lookup that closes over `rows`.
    if (isOpenHere(s)) {
      continue;
    }
    rows.push({
      key: `s:${s.session_id}`,
      kind: "chat",
      title: s.title === "" ? "Untitled session" : s.title,
      updatedAt: s.updated_at,
      detail: s.description ?? "",
      status: s.status ?? "",
      outcome: null,
      facts: chatFacts(s.chat_id ?? ""),
      session: s,
    });
  }
  for (const r of runs) {
    // A run's OUTCOME is stated only for a parentless (manually launched) run,
    // because that is the only kind whose recovery is the user's: a failed one
    // is a row to open and retry. An agent-parented run's failure is the
    // agent's to handle, and labelling it here would invite an action this
    // page deliberately does not offer.
    const parentless = (r.parent_chat_id ?? "") === "";
    const endReason = r.end_reason ?? "";
    rows.push({
      key: `r:${r.workflow_id}`,
      kind: "run",
      title: r.name === "" ? "Untitled run" : r.name,
      updatedAt: r.updated_at,
      // A bound's reason is stated whatever the run's parentage, unlike the
      // verdict below: it is a report of what VIBEKIT did to the run, not a
      // judgement of the run, so withholding it from an agent-parented row would
      // hide the app's own action from the only reader who can see it.
      detail: END_REASON_TEXT[endReason] ?? "",
      status: r.status ?? "",
      outcome: parentless ? runVerdict(r.status ?? "", endReason) : null,
      facts: runFacts(r),
      run: r,
    });
  }
  // One list, newest first — chats and runs interleaved by recency rather than
  // segregated, because "what was I doing" does not care which kind it was.
  rows.sort((a, b) => b.updatedAt - a.updatedAt);
  return rows;
}

class HistoryController {
  private abort: AbortController | null = null;
  /** The page's search box, as a popup.
   *
   *  It used to be a permanent in-flow field above the list, which is why the
   *  page had a `focus` verb and no way to close one. It is the transcript's
   *  popup now (search-popup.ts), reached by the toolbar's magnifier and Ctrl-F,
   *  and closing it clears the query — a hidden box holding `redis` would leave
   *  the page showing three of forty conversations with nothing on screen saying
   *  why.
   *
   *  It carries NO match-case toggle, and that is the endpoint's decision rather
   *  than a gap. `chat.searchOneChat` states it: "Case-INSENSITIVE, always. The
   *  match-case toggle belongs to the in-chat search, which is a different
   *  question on a different endpoint; a cross-chat 'which conversation was that
   *  in' is asked from memory, and memory does not remember capitalisation." So
   *  `GET /api/chats/search` reads no `case` parameter and `titleHits` folds
   *  unconditionally. A toggle here would be wired to nothing. */
  readonly search: SearchPopup = createSearchPopup<null>({
    id: "hist-search",
    // A SEARCH, so it carries the magnifier: the server reads every chat file on
    // disk, so this box finds conversations the loaded list does not contain. A
    // funnel would promise it only narrows what is on screen.
    kind: "search",
    label: "Search conversations",
    // The one string the page popups genuinely differ on, which is why the
    // helper takes it as a parameter: this box answers "which conversation was
    // that in", so it names the unit it returns rather than the place it looks —
    // and "Search", not "Filter", because the server reads every chat file on
    // disk and finds conversations the loaded list does not contain.
    placeholder: "Search conversations\u2026",
    note: true,
    // The scan is over up to 500 chat files, so the pause is longer than the
    // shell's default: a search is per-pause here, not per-keystroke.
    debounceMs: SEARCH_DEBOUNCE_MS,
    host: () => document.getElementById("history-view"),
    query: (q) => {
      this.query = q.trim();
      return null;
    },
    render: () => {
      void this.refresh();
    },
  });
  private query = "";

  /** Load, the body every path that opens this page shares. Separate from
   *  showView() because the tab-restore path already has the tab and must not
   *  toggle it. */
  mount(): void {
    void this.refresh();
  }

  showView(): void {
    // No callbacks: `mount` and `teardown` are what the tab factory reaches
    // through this module's own lazy-imported `loadHistoryView` /
    // `teardownHistoryView`, so every door into this page — this one, a boot
    // restore, another device's open — gets the same behaviour. Passing them here
    // as well would be two definitions of one tab.
    void toggleHistoryView();
  }

  teardown(): void {
    loadSessions.cancel();
    searchChats.cancel();
    this.abort?.abort();
    this.abort = null;
    // reset() rather than close(): the close's clear repaints the full list, and
    // that repaint is a fetch this page no longer has a reader for.
    this.query = "";
    this.search.reset();
  }

  /** Route to the list or to search, depending on the box. */
  private async refresh(): Promise<void> {
    if (this.query === "") {
      this.setNote("");
      await this.load();
      return;
    }
    await this.runSearch(this.query);
  }

  private setNote(text: string): void {
    this.search.shell?.setNote(text);
  }

  /** Render matching CHATS for the current query. */
  private async runSearch(q: string): Promise<void> {
    const container = document.getElementById("history-table");
    if (container === null) {
      return;
    }
    loadSessions.cancel();
    this.abort?.abort();
    this.abort = new AbortController();
    const { signal } = this.abort;

    const res = await searchChats.dispatch(q);
    // A newer keystroke already superseded this one, or the box was cleared.
    if (signal.aborted || this.query !== q) {
      return;
    }
    if (res === null) {
      this.setNote("Search failed. Check your connection.");
      return;
    }
    container.replaceChildren();
    if (res.matches.length === 0) {
      // Truncation must be stated: otherwise an empty result implies the text
      // is nowhere, when older chats simply were not read.
      this.setNote(
        res.truncated
          ? `No matches in the ${res.scanned} most recent conversations (older ones were not searched).`
          : `No matches in ${res.scanned} conversations.`,
      );
      container.replaceChildren(
        el("div", { className: "list-empty" }, "No matching conversations."),
      );
      return;
    }
    this.setNote(
      res.truncated
        ? `${res.matches.length} of the ${res.scanned} most recent conversations (older ones were not searched).`
        : `${res.matches.length} matching conversations.`,
    );
    for (const m of res.matches) {
      container.appendChild(buildMatchRow(m));
    }
    // One delegated listener per render, bound to this search's signal so the
    // previous one is dropped rather than stacking.
    container.addEventListener(
      "click",
      (e) => {
        const rowEl = (e.target as HTMLElement).closest<HTMLElement>("[data-search-chat]");
        const id = rowEl?.getAttribute("data-search-chat");
        if (id === null || id === undefined) {
          return;
        }
        const hit = res.matches.find((x) => x.id === id);
        void openChatTab(id, hit?.name ?? "Chat");
      },
      { signal },
    );
  }

  async load(): Promise<void> {
    const container = document.getElementById("history-table");
    if (container === null) {
      return;
    }
    loadSessions.cancel();
    this.abort?.abort();
    this.abort = new AbortController();
    const { signal } = this.abort;

    const skeleton = skeletonTiming(() => showSkeleton(container));
    const d = await loadSessions.dispatch(undefined);
    skeleton.cancel();
    if (signal.aborted) {
      return;
    }
    // Don't paint a misleading empty state on failure — offer a retry.
    if (d === null) {
      container.replaceChildren(this.buildError());
      return;
    }

    const rows = toRows(d.sessions ?? [], d.runs ?? []); // eslint-disable-line @typescript-eslint/no-unnecessary-condition
    // Drop any non-keyed sibling (skeleton / empty / error) before reconcile.
    for (const child of [...container.children]) {
      if ((child as HTMLElement).getAttribute("data-reconcile-key") === null) {
        child.remove();
      }
    }
    if (rows.length === 0) {
      container.replaceChildren(
        el("div", { className: "list-empty" }, "No previous sessions in this workspace."),
      );
      return;
    }
    reconcile(container, rows, { key: (r) => r.key, mount: (r) => buildRow(r) });

    // One delegated listener per load, signal-bound so the previous one is
    // dropped rather than stacking across re-opens.
    container.addEventListener(
      "click",
      (e) => {
        const target = e.target as HTMLElement;
        const rowEl = target.closest<HTMLElement>("[data-key]");
        if (rowEl === null) {
          return;
        }
        const row = rows.find((r) => r.key === rowEl.getAttribute("data-key"));
        if (row === undefined) {
          return;
        }
        // The delete button lives INSIDE the row, so its click reaches here too;
        // without this branch a delete would also open the row behind the confirm
        // dialog. A successful delete refreshes rather than waiting for the runs
        // bus event, which only fires for runs.
        if (target.closest("[data-history-delete]") !== null) {
          void deleteRow(row).then((gone) => {
            if (gone) {
              void this.refresh();
            }
          });
          return;
        }
        openRow(row, () => {
          void this.refresh();
        });
      },
      { signal },
    );
  }

  private buildError(): HTMLElement {
    const retry = el("button", { type: "button", className: "btn-small" }, "Retry");
    retry.addEventListener("click", () => {
      void this.load();
    });
    return el(
      "div",
      { className: "list-empty history-error" },
      el("span", {}, "Couldn't load previous sessions. Check your connection and try again."),
      retry,
    );
  }
}

/** Open a history row: a chat resumes, a run opens its read-only review.
 *
 *  `onGone` runs when the row's conversation turned out to be DELETED — the
 *  retention-off close erased it after this list was fetched — so the caller
 *  refreshes and the dead row drops out of the server-derived list. */
function openRow(row: HistoryRow, onGone: () => void): void {
  if (row.kind === "run" && row.run !== undefined) {
    // The parent chat, when there is one, nests the run's tab under it. History
    // already carries the id, so this costs nothing and is what puts a run beside
    // its own conversation rather than at the end of the strip. Whether the RUN is
    // parentless is not passed: it is the run's own fact and the composition root
    // resolves it from the run store.
    openRunView(row.run.workflow_id, row.title, row.run.parent_chat_id ?? "");
    return;
  }
  if (row.session !== undefined) {
    void openPreviousSession(row.session).then((outcome) => {
      if (outcome === "gone") {
        onGone();
      }
    });
  }
}

/** Delete a history row and the files behind it, after confirming.
 *
 *  This is the page's only destructive affordance and the only manual control
 *  over what History holds. Everything else that removes a row is automatic and
 *  invisible: the retention purge (a chat older than `chat_retention_days`), the
 *  hourly orphan sweep (KAS session state no chat references), and a tab close in
 *  the retention-off mode. None of those is addressable, so a row a user wants
 *  gone had no way to go.
 *
 *  Both kinds delete their own underlying state, and neither is recoverable:
 *
 *    - a CHAT row runs the `delete_chat` command, vibekit's single chat-deletion
 *      path, which removes the chat file AND reaps every KAS session in the
 *      chat's chain (`reapChatSession`).
 *    - a RUN row runs `_kiro/workflow/delete`, which cancels the run if it is
 *      still moving and then removes its run directory, plus vibekit's own lease,
 *      timer and recorded end reason.
 *
 *  Returns true when the row is gone, so the caller can refresh rather than wait
 *  for a poll. */
async function deleteRow(row: HistoryRow): Promise<boolean> {
  const label = row.kind === "run" ? "run" : "conversation";
  const ok = await confirm(
    `Delete this ${label}? "${row.title}" and its stored history are removed for good.`,
    "Delete",
    "destructive",
  );
  if (!ok) {
    return false;
  }
  if (row.kind === "run" && row.run !== undefined) {
    return (await deleteRun.dispatch(row.run.workflow_id)) !== null;
  }
  const chatID = row.session?.chat_id ?? "";
  if (chatID === "") {
    return false;
  }
  // Closing the tab first would run its own teardown against a chat that is
  // about to stop existing, so the tab goes only after the delete lands. A chat
  // open on ANOTHER device keeps its tab there until the chat_deleted frame
  // arrives, which is the same path an ordinary delete takes.
  // The TAB is not closed here any more, and that is the point: the membership
  // coordinator closes every tab for a deleted chat under the same lock that
  // removes the record, and emits the removal. Closing it from here would be a
  // second `close_tab` for a tab the server has already dropped.
  return (await deleteChatAction.dispatch(chatID)) !== null;
}

function buildRow(row: HistoryRow): HTMLElement {
  const isRun = row.kind === "run";
  const kindChip = el(
    "span",
    { className: `history-kind ${isRun ? "history-kind-run" : "history-kind-chat"}` },
    isRun ? "Run" : "Chat",
  );
  // The row's OPEN control, and a real <button> rather than a role on the row.
  // `role="button"` is Children-Presentational, so it flattened the delete button
  // beside it out of the accessibility tree (axe nested-interactive, serious, on
  // every row) — the same finding the git Changes tab's file row and the
  // disclosure headers already answered. It also never activated on Enter or
  // Space: a role="button" needs a key handler and this page never had one, so the
  // rows were focusable and not operable. A real button gets both from the
  // platform, and the container's delegated click listener keeps the wide mouse
  // target, so nothing about a click on the row changes.
  const openBtn = el(
    "button",
    { type: "button", className: "list-row-name", "aria-label": `Open ${row.title}` },
    row.title,
  );
  const title = el(
    "div",
    { className: "list-row-title" },
    openBtn,
    row.detail !== "" ? el("span", { className: "list-row-summary" }, row.detail) : null,
    // The facts line. Present only when there is something factual to say, so a
    // row vibekit knows nothing about keeps its old two-line height instead of
    // reserving a blank strip. Rendered as one element with separators rather
    // than a chip per fact: these are read left to right as a sentence about the
    // conversation, and six bordered chips per row would compete with the kind
    // chip that actually needs to stand out.
    row.facts.length > 0 ? el("span", { className: "history-facts" }, row.facts.join(" · ")) : null,
  );
  // A status is shown only when it says something. KAS reports `idle` for every
  // settled session, which is noise; `failed` and `waiting_on_user` are not. A
  // row with a verdict is the third case: the glyph IS its outcome channel, so
  // the word beside it would be a second rendering of one fact.
  const showStatus = row.status !== "" && row.status !== "idle" && row.outcome === null;
  const node = el(
    "div",
    {
      className: "list-row history-table-row",
      "data-key": row.key,
    },
    kindChip,
    title,
    showStatus ? el("span", { className: "history-status" }, row.status.replace(/_/g, " ")) : null,
    // The glyph the verdict is painted onto. `.tool-icon` is the DOM contract of
    // the shared outcome vocabulary (a relative box for the composited badge),
    // and the run's own icon is what carries the tint.
    row.outcome !== null ? el("span", { className: "tool-icon" }, iconEl(ICON_TAB_RUN)) : null,
    el(
      "span",
      { className: "list-row-meta" },
      row.updatedAt > 0 ? new Date(row.updatedAt).toLocaleString() : "",
    ),
    buildDeleteButton(row),
  );
  if (row.outcome !== null) {
    // ONE writer for the vocabulary: tint, shape and word all come from
    // tool-card.ts, so a run row and a tool card cannot spell the same verdict
    // differently. The subject is the row's own label, so the accessible name it
    // composes still opens with what a click does ("Open X, succeeded") — and it
    // lands on the open button, because that is the control a reader reaches.
    applyOutcome(node, row.outcome, `Open ${row.title}`, ROW_RENDER_INFO, openBtn);
  }
  return node;
}

/** The row's delete control.
 *
 *  A real `<button>`, beside the row's open button rather than inside it: both are
 *  ordinary children of a plain row, so assistive tech reads two controls. It
 *  carries `data-history-delete` so the container's one delegated listener can tell
 *  a delete click from an open click, and the open handler bails on it — otherwise
 *  every delete would also open the thing it is deleting.
 *
 *  Named for the row, not the glyph: "Delete X" is what a screen reader announces,
 *  beside the row's own "Open X". */
function buildDeleteButton(row: HistoryRow): HTMLElement {
  const btn = el("button", {
    type: "button",
    className: "history-delete",
    "data-history-delete": row.key,
    "aria-label": `Delete ${row.title}`,
    "data-tooltip": "Delete",
  });
  btn.appendChild(iconEl(ICON_TRASH));
  return btn;
}

// ---------------------------------------------------------------------------
// Cross-chat search.
//
// A SECOND mode over the same container, not a filter of the loaded list: the
// list is the newest N sessions from KAS, while search reads every chat file
// server-side. Filtering what happens to be on screen would answer a narrower
// question than the box appears to ask.
//
// The in-chat search stays scoped to its own chat (user decision); this one
// finds the conversation, and opening it hands over to that.
// ---------------------------------------------------------------------------

/** Debounce so a search is per-pause, not per-keystroke. */
const SEARCH_DEBOUNCE_MS = 250;

function buildMatchRow(m: ChatSearchMatch): HTMLElement {
  const detail =
    m.best.excerpt !== ""
      ? m.best.excerpt
      : // A title-only match has no line to quote; say why it matched instead.
        "matches the conversation name";
  const more = m.hits > 1 ? `${m.hits} matches` : m.hits === 1 ? "1 match" : "";
  return el(
    "div",
    {
      className: "list-row history-table-row",
      "data-search-chat": m.id,
    },
    el("span", { className: "history-kind history-kind-chat" }, "Chat"),
    el(
      "div",
      { className: "list-row-title" },
      // The same open control the loaded list uses, so a keyboard reaches a match
      // row exactly as it reaches a session row.
      el(
        "button",
        { type: "button", className: "list-row-name", "aria-label": `Open ${m.name}` },
        m.name,
      ),
      el("span", { className: "list-row-summary" }, detail),
    ),
    more !== "" ? el("span", { className: "history-status" }, more) : null,
    el(
      "span",
      { className: "list-row-meta" },
      m.updated_at > 0 ? new Date(m.updated_at).toLocaleString() : "",
    ),
  );
}

/** Skeleton rows while the fetch is in flight; skipped when already populated
 *  so a re-open doesn't flash placeholders. */
function showSkeleton(container: HTMLElement): () => void {
  if (container.querySelector("[data-key]") !== null) {
    return () => {
      /* already populated */
    };
  }
  const wrap = el("div", { className: "history-skeleton", "aria-hidden": "true" });
  for (let i = 0; i < 4; i++) {
    const rowEl = el("div", { className: "list-row history-table-row history-skel-row" });
    const title = el("div", { className: "list-row-title" });
    title.appendChild(skelBar("history-skel-name", "55%"));
    title.appendChild(skelBar("history-skel-summary", "38%"));
    rowEl.appendChild(title);
    rowEl.appendChild(skelBar("history-skel-date", "8rem"));
    wrap.appendChild(rowEl);
  }
  container.replaceChildren(wrap);
  return () => {
    wrap.remove();
  };
}

function skelBar(className: string, width: string): HTMLElement {
  const bar = el("div", { className: `skeleton ${className}` });
  bar.style.width = width;
  return bar;
}

const historyCtrl = new HistoryController();
registerCleanup(() => {
  historyCtrl.teardown();
});

/** This page's find, handed to the dispatcher rather than imported by it: this
 *  module is lazily loaded (it pulls chat.ts in behind it) and the dispatcher must
 *  not put it on the boot path.
 *
 *  The popup already answers the three questions `PageFind` asks, so there is
 *  nothing to adapt. It declares no `available`: the cross-chat box is reachable
 *  in every state this page renders in, so the toolbar's magnifier always has a
 *  destination here. */
const historyFind = historyCtrl.search;

export function showHistoryView(): void {
  registerFind("history", historyFind);
  historyCtrl.showView();
}

/** Load (or reload) the page's data without touching the tab.
 *
 *  The tab-restore path needs this and cannot use showHistoryView(): that one
 *  toggles, so firing it from the `onShow` of an already-open, already-active
 *  tab would hit `hasTab && active` and CLOSE the tab it was meant to fill. */
export function loadHistoryView(): void {
  registerFind("history", historyFind);
  historyCtrl.mount();
}

/** Cancel the page's in-flight work. The restore path passes this as its tab's
 *  `onClose`; the module-level cleanup above covers app teardown, not a close. */
export function teardownHistoryView(): void {
  historyCtrl.teardown();
}

// A run starting or finishing changes this list, and a workflow with twenty steps
// would otherwise leave a stale row until the user reopened the page.
//
// Gated on the page being on screen, so this never becomes a background fetch for
// a view nobody is looking at: `#history-table` only holds rows while the view is
// mounted, so its emptiness IS the closed state.
onBus(BUS_RUNS_CHANGED, () => {
  if ((document.getElementById("history-table")?.childElementCount ?? 0) > 0) {
    void historyCtrl.load();
  }
});
