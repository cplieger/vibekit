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
import { applyOutcome } from "./tool-card.js";
import type { ToolRenderInfo } from "./tool-schema.js";
import { ICON_TAB_RUN } from "./icons.js";
import { iconEl } from "./icon-el.js";
import { createSearchShell, searchGlyph } from "./search-shell.js";
import type { SearchShell } from "./search-shell.js";
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
  session?: ResumableSessionRow;
  run?: WorkflowRunRow;
}

/** The run statuses that carry a verdict. Two are `ToolStatus` members and the
 *  third is the member `applyOutcome` was widened by, so these pass straight
 *  into the shared outcome vocabulary with no translation table here. */
type RunVerdict = "completed" | "failed" | "aborted";

/** How a bounded termination reads. The keys are the server's vocabulary
 *  (api.WorkflowRun.EndReason); the sentences are the reader's.
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
  return chatID !== "" && hasTab(chatID);
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
  /** The shared search box. Built once and left mounted: unlike the transcript's
   *  and the browser's, this box is PERMANENT page furniture — it is the page's
   *  second mode selector, so there is nothing to reveal and nothing to dismiss.
   *
   *  It carries NO match-case toggle, and that is the endpoint's decision rather
   *  than a gap. `chat.searchOneChat` states it: "Case-INSENSITIVE, always. The
   *  match-case toggle belongs to the in-chat search, which is a different
   *  question on a different endpoint; a cross-chat 'which conversation was that
   *  in' is asked from memory, and memory does not remember capitalisation." So
   *  `GET /api/chats/search` reads no `case` parameter and `titleHits` folds
   *  unconditionally. A toggle here would be wired to nothing. */
  private shell: SearchShell | null = null;
  private query = "";

  /** Wire the search box and load, the body every path that opens this page
   *  shares. Separate from showView() because the tab-restore path already has
   *  the tab and must not toggle it. */
  mount(): void {
    this.wireSearch();
    void this.refresh();
  }

  /** Put the caret in the box. What Ctrl-F means on this page. */
  focusSearch(): boolean {
    this.wireSearch();
    if (this.shell === null) {
      return false;
    }
    this.shell.focus();
    return true;
  }

  showView(): void {
    toggleHistoryView(
      () => {
        this.mount();
      },
      () => {
        this.teardown();
      },
    );
  }

  teardown(): void {
    loadSessions.cancel();
    searchChats.cancel();
    this.abort?.abort();
    this.abort = null;
    this.shell?.cancel();
  }

  /** Build the search box once into its host; the element outlives a view
   *  close. */
  private wireSearch(): void {
    if (this.shell !== null) {
      return;
    }
    const host = document.getElementById("hist-search-host");
    if (host === null) {
      return;
    }
    this.shell = createSearchShell<null>({
      id: "hist-search",
      regionClass: "hist-search",
      inputClass: "hist-search-input",
      buttonClass: "hist-search-btn",
      noteClass: "hist-search-note",
      label: "Search conversations",
      // The one string the four boxes genuinely differ on, which is why the
      // shell takes it as a parameter: this box answers "which conversation was
      // that in", so it names the unit it returns rather than the place it looks.
      placeholder: "Search conversations\u2026",
      inputType: "search",
      note: true,
      // The scan is over up to 500 chat files, so the pause is longer than the
      // shell's default: a search is per-pause here, not per-keystroke.
      debounceMs: SEARCH_DEBOUNCE_MS,
      compose: ({ input, note }) => {
        // The magnifier stays: it is a SEARCH, not a filter — the server reads
        // every chat file on disk, so it finds conversations the loaded list does
        // not contain, and a funnel would promise it only narrows what is on
        // screen. 18-pages.css records that reasoning.
        const field = el("span", { className: "hist-search-field" });
        field.appendChild(searchGlyph("hist-search-icon"));
        field.appendChild(input);
        return [field, note];
      },
      query: (q) => {
        this.query = q.trim();
        return null;
      },
      render: () => {
        void this.refresh();
      },
      onDismiss: () => {
        // Escape CLEARS rather than closes: a permanent box has nothing to
        // dismiss, so the useful meaning is "back to the full list".
        if (this.shell === null || this.shell.input.value === "") {
          return;
        }
        this.shell.input.value = "";
        this.shell.run();
      },
      onSubmit: () => {
        this.shell?.run();
      },
    });
    host.appendChild(this.shell.region);
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
    this.shell?.setNote(text);
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
        openChatTab(id, hit?.name ?? "Chat");
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
        const rowEl = (e.target as HTMLElement).closest<HTMLElement>("[data-key]");
        if (rowEl === null) {
          return;
        }
        const row = rows.find((r) => r.key === rowEl.getAttribute("data-key"));
        if (row !== undefined) {
          openRow(row);
        }
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

/** Open a history row: a chat resumes, a run opens its read-only review. */
function openRow(row: HistoryRow): void {
  if (row.kind === "run" && row.run !== undefined) {
    // Only a parentless run offers Retry on its page (user decision).
    openRunView(row.run.workflow_id, row.title, (row.run.parent_chat_id ?? "") === "");
    return;
  }
  if (row.session !== undefined) {
    openPreviousSession(row.session);
  }
}

function buildRow(row: HistoryRow): HTMLElement {
  const isRun = row.kind === "run";
  const kindChip = el(
    "span",
    { className: `history-kind ${isRun ? "history-kind-run" : "history-kind-chat"}` },
    isRun ? "Run" : "Chat",
  );
  const title = el(
    "div",
    { className: "list-row-title" },
    el("span", { className: "list-row-name" }, row.title),
    row.detail !== "" ? el("span", { className: "list-row-summary" }, row.detail) : null,
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
      role: "button",
      tabindex: "0",
      "data-key": row.key,
      "aria-label": `Open ${row.title}`,
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
  );
  if (row.outcome !== null) {
    // ONE writer for the vocabulary: tint, shape and word all come from
    // tool-card.ts, so a run row and a tool card cannot spell the same verdict
    // differently. The subject is the row's own label, so the accessible name it
    // composes still opens with what a click does ("Open X, succeeded").
    applyOutcome(node, row.outcome, `Open ${row.title}`, ROW_RENDER_INFO);
  }
  return node;
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
      role: "button",
      tabindex: "0",
      "data-search-chat": m.id,
      "aria-label": `Open ${m.name}`,
    },
    el("span", { className: "history-kind history-kind-chat" }, "Chat"),
    el(
      "div",
      { className: "list-row-title" },
      el("span", { className: "list-row-name" }, m.name),
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

/** Put the caret in the cross-chat search box. What Ctrl-F and the toolbar's
 *  search button mean on this page.
 *
 *  Registered with find-dispatch rather than imported by it, because this module
 *  is lazily loaded (it pulls chat.ts in behind it) and the dispatcher must not
 *  put it on the boot path. */
function focusHistorySearch(): boolean {
  return historyCtrl.focusSearch();
}

export function showHistoryView(): void {
  registerFind("history", focusHistorySearch);
  historyCtrl.showView();
}

/** Load (or reload) the page's data without touching the tab.
 *
 *  The tab-restore path needs this and cannot use showHistoryView(): that one
 *  toggles, so firing it from the `onShow` of an already-open, already-active
 *  tab would hit `hasTab && active` and CLOSE the tab it was meant to fill. */
export function loadHistoryView(): void {
  registerFind("history", focusHistorySearch);
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
