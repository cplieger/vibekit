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

import { toggleHistoryView } from "./tabs.js";
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
import type { ResumableSessionRow, WorkflowRunRow } from "./types.js";

/** A chat row and a run row share the list, so they share a shape. */
interface HistoryRow {
  /** Reconcile key. Prefixed per kind so a session and a run can never collide. */
  key: string;
  kind: "chat" | "run";
  title: string;
  updatedAt: number;
  /** Secondary line: the agent's focus for a chat, the status for a run. */
  detail: string;
  status: string;
  session?: ResumableSessionRow;
  run?: WorkflowRunRow;
}

function toRows(sessions: ResumableSessionRow[], runs: WorkflowRunRow[]): HistoryRow[] {
  const rows: HistoryRow[] = [];
  for (const s of sessions) {
    rows.push({
      key: `s:${s.session_id}`,
      kind: "chat",
      title: s.title === "" ? "Untitled session" : s.title,
      updatedAt: s.updated_at,
      detail: s.description ?? "",
      status: s.status ?? "",
      session: s,
    });
  }
  for (const r of runs) {
    rows.push({
      key: `r:${r.workflow_id}`,
      kind: "run",
      title: r.name === "" ? "Untitled run" : r.name,
      updatedAt: r.updated_at,
      detail: "",
      status: r.status ?? "",
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
  private searchTimer: ReturnType<typeof setTimeout> | null = null;
  private searchWired = false;
  private query = "";

  showView(): void {
    toggleHistoryView(
      () => {
        this.wireSearch();
        void this.refresh();
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
    if (this.searchTimer !== null) {
      clearTimeout(this.searchTimer);
      this.searchTimer = null;
    }
  }

  /** Wire the search box once; the element outlives a view close. */
  private wireSearch(): void {
    if (this.searchWired) {
      return;
    }
    const input = document.getElementById("hist-search-input");
    if (!(input instanceof HTMLInputElement)) {
      return;
    }
    this.searchWired = true;
    input.addEventListener("input", () => {
      this.query = input.value.trim();
      if (this.searchTimer !== null) {
        clearTimeout(this.searchTimer);
      }
      this.searchTimer = setTimeout(() => {
        this.searchTimer = null;
        void this.refresh();
      }, SEARCH_DEBOUNCE_MS);
    });
    // Escape clears back to the full list, the convention every search box has.
    input.addEventListener("keydown", (e: KeyboardEvent) => {
      if (e.key === "Escape" && input.value !== "") {
        e.stopPropagation();
        input.value = "";
        this.query = "";
        void this.refresh();
      }
    });
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
    const note = document.getElementById("hist-search-note");
    if (note !== null) {
      note.textContent = text;
    }
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
    openRunView(row.run.workflow_id, row.title);
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
  // settled session, which is noise; `failed` and `waiting_on_user` are not.
  const showStatus = row.status !== "" && row.status !== "idle";
  return el(
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
    el(
      "span",
      { className: "list-row-meta" },
      row.updatedAt > 0 ? new Date(row.updatedAt).toLocaleString() : "",
    ),
  );
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

export function showHistoryView(): void {
  historyCtrl.showView();
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
