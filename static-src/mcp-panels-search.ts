// ---------------------------------------------------------------------------
// MCP registry search panel — extracted from mcp-panels.ts for isolation.
// ---------------------------------------------------------------------------

import { byId } from "./dom.js";
import { searchRegistry, registryFailureOf } from "./actions/mcp.js";
import type {
  RegistryEntry,
  RegistrySearchFailure,
  RegistrySearchResult,
} from "./wire/types.gen.js";
import {
  subscribeToActions,
  bindLoadingState,
  debouncedDispatch,
  registerCleanup,
} from "./actions/index.js";
import type { DebouncedDispatch } from "./actions/index.js";
import { reconcile } from "./reconcile.js";
import { chevronEl } from "./chevron.js";
import { emptyNote, type Nouns } from "./textsearch/copy.js";
import { el } from "@cplieger/reactive";

// --- Types ---

/** The no-rows answers this surface can give, mapped from its own inputs: a
 *  502 body, the reply's `filtered` count, the query's length. `tooShort` is
 *  the shared empty-answer vocabulary's member and renders through its
 *  `emptyNote`; the other three keep this surface's own sentences until it
 *  adopts `textsearch/copy.ts` whole. An in-flight search is not an answer and
 *  has no member. */
type RegistryEmptyState =
  | { kind: "none" }
  | { kind: "withheld"; matched: number }
  | { kind: "failed"; retryAfterS?: number }
  | { kind: "tooShort"; min: number };

/** The registry's rows are servers, and it scans nothing of its own, so the
 *  one noun serves both keys. */
const NOUNS: Nouns = {
  match: { one: "server", many: "servers" },
  scanned: { one: "server", many: "servers" },
};

/** Callback to switch the modal to a different panel mode. */
export type SwitchModeFn = (
  kind: string,
  slug: string,
  identifier: string,
  fields: InstallField[],
) => void;

/** One field a registry entry declares: the env var or header the server needs,
 *  with the publisher's description and its required / secret markers. The
 *  markers used to be dropped on the way into the form, which is why a server
 *  could install cleanly and then do nothing. */
export interface InstallField {
  name: string;
  description?: string | undefined;
  required?: boolean | undefined;
  secret?: boolean | undefined;
}

// --- Module state ---

/** Quiet window before a keystroke reaches the registry. The upstream refuses
 *  connections after a burst (measured: ~16 requests in a few seconds, then
 *  `Could not connect` for about a minute), and one request per typed PREFIX is
 *  exactly that shape — each prefix is a distinct query, so neither the server's
 *  60s cache nor the action's dedupe collapses any of it. */
const DEBOUNCE_MS = 400;

/** Shortest query that reaches the registry. A single letter matches most of the
 *  index, so it costs a slow upstream round trip to answer nothing useful. */
const MIN_QUERY_LEN = 2;

let debouncedSearch: DebouncedDispatch<{ q: string }> | null = null;
let searchUnsub: (() => void) | null = null;
let retryBtnUnbind: (() => void) | null = null;
/** Re-enables a Retry button the registry asked to hold for an interval. */
let retryTimer: ReturnType<typeof setTimeout> | null = null;
let searchBtnUnbind: (() => void) | null = null;

/** The newest query the user has asked for. Dispatches are not scoped, so a
 *  slow answer for an abandoned prefix can land after a newer one; without this
 *  the box shows results for a query the user has already typed past. */
let wantedQuery = "";

registerCleanup(() => {
  debouncedSearch?.cancel();
  searchUnsub?.();
  clearRetry();
  searchBtnUnbind?.();
});

/** Drops the Retry button's binding and its hold timer together: every render
 *  that replaces the box replaces the button, and a timer left behind would
 *  re-enable a node that is no longer on screen. */
function clearRetry(): void {
  retryBtnUnbind?.();
  retryBtnUnbind = null;
  if (retryTimer !== null) {
    clearTimeout(retryTimer);
    retryTimer = null;
  }
}

// --- Public API ---

/** Wire to call when the user clicks an install button in search results. */
let switchMode: SwitchModeFn | null = null;

export function setSwitchMode(fn: SwitchModeFn): void {
  switchMode = fn;
}

/** Cancel in-flight search work and tear down subscriptions. */
export function cleanupSearch(): void {
  debouncedSearch?.cancel();
  clearRetry();
  searchBtnUnbind?.();
  searchBtnUnbind = null;
  searchUnsub?.();
  searchUnsub = null;
  wantedQuery = "";
}

export function initSearchPanel(): void {
  const input = byId<HTMLInputElement>("mcp-search-input");
  const results = byId<HTMLDivElement>("mcp-search-results");
  const btn = byId<HTMLButtonElement>("mcp-search-btn");
  input.value = "";
  wantedQuery = "";
  results.replaceChildren();
  input.focus();

  searchUnsub?.();
  debouncedSearch = debouncedDispatch(searchRegistry, { wait: DEBOUNCE_MS });

  // The panel is re-initialised every time the modal opens on this mode, so the
  // previous binding has to go or the button collects one per open. `pendingClass`
  // is what makes the button say a query is running, and it covers the typed path
  // as well as the click because both dispatch this action.
  searchBtnUnbind?.();
  searchBtnUnbind = bindLoadingState("mcp.search_registry", btn, {
    pendingClass: "is-searching",
  });

  searchUnsub = subscribeToActions((inst) => {
    if (inst.name !== "mcp.search_registry") {
      return;
    }
    const q = (inst.args as { q: string }).q;
    if (q !== wantedQuery) {
      return; // An abandoned prefix answering late.
    }
    if (inst.status === "pending") {
      renderSearching(results);
    } else if (inst.status === "success") {
      const d = inst.result as RegistrySearchResult | undefined;
      renderSearchResults(results, d, q);
    } else if (inst.status === "error") {
      renderSearchError(results, q, registryFailureOf(inst.error));
    }
  });

  /** Schedule or fire a query, or say why nothing is asked. A query under the
   *  floor renders the hint rather than clearing the box: an empty box after
   *  one typed character reads exactly like an answered query with no matches.
   *  `immediate` is the Enter / button path, which skips the quiet window. */
  const ask = (immediate: boolean): void => {
    const q = input.value.trim();
    wantedQuery = q;
    if (q.length < MIN_QUERY_LEN) {
      debouncedSearch?.cancel();
      renderEmpty(results, { kind: "tooShort", min: MIN_QUERY_LEN }, q);
      return;
    }
    if (immediate) {
      void debouncedSearch?.flush({ q });
      return;
    }
    debouncedSearch?.({ q });
  };

  input.oninput = (): void => {
    ask(false);
  };

  input.onkeydown = (e: KeyboardEvent): void => {
    if (e.key === "Enter") {
      e.preventDefault();
      ask(true);
    }
  };

  btn.onclick = (): void => {
    ask(true);
  };
}

/** The in-flight row. The registry answers in about a second when healthy and
 *  can take ten when it is not, and the box used to sit empty for the whole
 *  wait — which reads as "this does nothing" rather than "this is slow". */
function renderSearching(results: HTMLDivElement): void {
  clearRetry();
  results.replaceChildren(el("p", { className: "mcp-empty" }, "Searching the registry…"));
}

const ROW_SPEC = {
  key: (e: RegistryEntry) => e.name,
  mount: (e: RegistryEntry) => renderRegistryResult(e),
};

function renderSearchResults(
  results: HTMLDivElement,
  d: RegistrySearchResult | undefined,
  q: string,
): void {
  clearRetry();
  for (const child of [...results.children]) {
    if ((child as HTMLElement).getAttribute("data-reconcile-key") === null) {
      child.remove();
    }
  }
  if (d == null) {
    renderEmpty(results, { kind: "failed" }, q);
    return;
  }
  if (d.servers.length === 0) {
    renderEmpty(
      results,
      d.filtered > 0 ? { kind: "withheld", matched: d.filtered } : { kind: "none" },
      q,
    );
    return;
  }
  reconcile(results, d.servers, ROW_SPEC);
  const note = resultNote(d);
  if (note !== null) {
    results.appendChild(el("p", { className: "mcp-empty" }, note));
  }
}

/** One line under a non-empty list saying how it differs from what matched.
 *  Both facts are "the list you see is not the list that matched", so they
 *  share the line. Null when the list is the whole answer. */
function resultNote(d: RegistrySearchResult): string | null {
  const parts: string[] = [];
  if (d.filtered > 0) {
    parts.push(`${d.filtered} more matched but cannot be installed here.`);
  }
  if (d.truncated) {
    parts.push("More matched than shown; narrow the query to see the rest.");
  }
  return parts.length === 0 ? null : parts.join(" ");
}

/** One sentence per no-rows answer. `tooShort` is the shared sentence; the
 *  other three are this surface's own until it adopts `textsearch/copy.ts`
 *  whole, and `none` echoes the query where the shared vocabulary speaks in
 *  nouns. */
function registryEmptyNote(state: RegistryEmptyState, q: string): string {
  switch (state.kind) {
    case "none":
      return `No results for "${q}".`;
    case "withheld":
      return `${state.matched} matched, but none can be installed here.`;
    case "failed":
      return state.retryAfterS === undefined
        ? "Registry unreachable. Use the Remote URL or npm package forms instead."
        : `The registry asked for a pause; retry in ${state.retryAfterS}s, or use the Remote URL or npm package forms instead.`;
    case "tooShort":
      return emptyNote(state, NOUNS);
  }
}

function renderEmpty(results: HTMLDivElement, state: RegistryEmptyState, q: string): void {
  clearRetry();
  reconcile(results, [] as RegistryEntry[], ROW_SPEC);
  results.replaceChildren(el("p", { className: "mcp-empty" }, registryEmptyNote(state, q)));
  if (state.kind === "failed") {
    results.appendChild(renderRetry(q, state.retryAfterS));
  }
}

/** A failed dispatch. The 502 body's classification, when the server sent
 *  one, decides whether Retry may fire at once: a rate-limited registry that
 *  named an interval is refusing, so a click inside it is guaranteed to fail
 *  again, and the button waits it out. */
function renderSearchError(
  results: HTMLDivElement,
  q: string,
  failure: RegistrySearchFailure | undefined,
): void {
  const retryAfterS = failure?.retry_after;
  renderEmpty(
    results,
    retryAfterS === undefined ? { kind: "failed" } : { kind: "failed", retryAfterS },
    q,
  );
}

function renderRetry(q: string, holdS: number | undefined): HTMLButtonElement {
  const retryBtn = el(
    "button",
    { type: "button", className: "btn-small" },
    "Retry",
  ) as HTMLButtonElement;
  if (holdS !== undefined) {
    retryBtn.disabled = true;
    retryTimer = setTimeout(() => {
      retryTimer = null;
      retryBtn.disabled = false;
    }, holdS * 1000);
  }
  // `disabledFn` is what the binding restores on a pending-to-idle transition;
  // without it an abandoned prefix's dispatch settling would re-enable the
  // button inside the hold.
  retryBtnUnbind = bindLoadingState("mcp.search_registry", retryBtn, {
    disabledFn: () => retryTimer !== null,
  });
  retryBtn.addEventListener("click", () => {
    // Re-declare the intent: a failed query is not cached server-side, so this
    // is a real re-fetch, and the subscription only renders the wanted query.
    wantedQuery = q;
    void searchRegistry.dispatch({ q });
  });
  return retryBtn;
}

/** One search result: a compact row that expands. Exported for its test — the
 *  deprecated badge and the requirements preview are the two things a reader
 *  relies on before installing, and both are decided here.
 *
 *  The install buttons are SIBLINGS of the `<details>` rather than children of its
 *  `<summary>`, which is what keeps installing reachable without opening the row
 *  and what keeps a button out of a `role="button"` (axe's `nested-interactive`). */
export function renderRegistryResult(entry: RegistryEntry): HTMLDivElement {
  // The registry still LISTS a deprecated entry (only deleted ones are filtered
  // upstream), so without this badge a dead server reads exactly like a live one.
  const status = entry.status ?? "";

  const summary = el(
    "summary",
    { className: "mcp-result-summary" },
    chevronEl(),
    el("span", { className: "mcp-result-name" }, entry.title ?? entry.name),
  );
  const version = (entry.version ?? "").trim();
  if (version !== "") {
    summary.appendChild(el("span", { className: "mcp-result-version" }, version));
  }
  if (status !== "") {
    summary.appendChild(el("span", { className: "mcp-result-status" }, status));
  }
  summary.appendChild(
    el("span", { className: "mcp-result-desc" }, entry.description ?? entry.name),
  );

  const body = el("div", { className: "mcp-result-body" });
  if (status !== "") {
    const why = (entry.status_message ?? "").trim();
    body.appendChild(
      el(
        "p",
        { className: "mcp-result-status-note" },
        why !== "" ? why : `The registry marks this entry ${status}.`,
      ),
    );
  }

  const actions = el("div", { className: "mcp-result-actions" });
  for (const option of installOptions(entry)) {
    actions.appendChild(option.btn);
    body.appendChild(option.detail);
  }

  const row = el(
    "div",
    { className: "mcp-result" },
    el("details", { className: "mcp-result-disc" }, summary, body),
    actions,
  ) as HTMLDivElement;
  if (status !== "") {
    row.classList.add("mcp-result-deprecated");
  }
  return row;
}

/** One install path, split across the row's two halves. */
interface InstallOption {
  /** Starts the install. Sits on the row, so it needs no expansion. */
  btn: HTMLButtonElement;
  /** The identifier and what installing it will ask for. Sits in the body. */
  detail: HTMLDivElement;
}

/** Every path the publisher declared, in registry order: a package runs locally
 *  under `npx`, a remote is a hosted URL. */
function installOptions(entry: RegistryEntry): InstallOption[] {
  const out: InstallOption[] = [];
  for (const pkg of entry.packages ?? []) {
    out.push(
      renderInstallOption(entry, pkg.registry_type, pkg.identifier, pkg.env_vars ?? [], "env"),
    );
  }
  for (const rem of entry.remotes ?? []) {
    out.push(
      renderInstallOption(
        entry,
        rem.type,
        rem.url,
        (rem.headers ?? []).map((h) => ({
          name: h.name,
          description: h.description,
          required: h.required,
          secret: h.secret,
        })),
        "header",
      ),
    );
  }
  return out;
}

/** One install path: the button, plus what installing it will ask for.
 *
 *  The preview is DISCLOSURE, not consent — it names the credentials the server
 *  needs before the user commits, which is the gap that made a clean install
 *  fail silently. It gates nothing; the form behind it saves either way. */
function renderInstallOption(
  entry: RegistryEntry,
  kind: string,
  identifier: string,
  fields: InstallField[],
  fieldKind: "env" | "header",
): InstallOption {
  const detail = el(
    "div",
    { className: "mcp-install-option" },
    el("code", { className: "mcp-install-id" }, `${kind}: ${identifier}`),
  ) as HTMLDivElement;
  const preview = renderRequirements(fields, fieldKind);
  if (preview !== null) {
    detail.appendChild(preview);
  }
  return { btn: renderInstallBtn(entry, kind, identifier, fields), detail };
}

/** The declared env vars / headers of one install path. Null when the publisher
 *  declared none, which is the honest reading of "needs nothing configured". */
function renderRequirements(
  fields: InstallField[],
  fieldKind: "env" | "header",
): HTMLElement | null {
  if (fields.length === 0) {
    return null;
  }
  const required = fields.filter((f) => f.required === true).length;
  const label =
    required > 0
      ? `Needs ${required} of ${fields.length} ${fieldKind === "env" ? "environment variables" : "headers"}`
      : `Optional ${fieldKind === "env" ? "environment variables" : "headers"} (${fields.length})`;

  const list = el("ul", { className: "mcp-requires-list" });
  for (const f of fields) {
    const item = el("li", {}, el("code", { className: "mcp-requires-name" }, f.name));
    if (f.required === true) {
      item.appendChild(
        el("span", { className: "mcp-pair-mark mcp-pair-mark-required" }, "Required"),
      );
    }
    if (f.secret === true) {
      item.appendChild(el("span", { className: "mcp-pair-mark" }, "Secret"));
    }
    const desc = (f.description ?? "").trim();
    if (desc !== "") {
      item.appendChild(el("span", { className: "mcp-requires-desc" }, desc));
    }
    list.appendChild(item);
  }
  return el(
    "div",
    { className: "mcp-requires" },
    el("p", { className: "mcp-requires-label" }, label),
    list,
  );
}

function renderInstallBtn(
  entry: RegistryEntry,
  kind: string,
  identifier: string,
  fields: InstallField[],
): HTMLButtonElement {
  // The label names the TRANSPORT only, because the row is one line and an npm
  // package or a hosted URL is longer than the rest of it. The identifier travels
  // in the accessible name and the tooltip, so two remotes of one kind are not two
  // buttons reading the same two words.
  const btn = el(
    "button",
    {
      type: "button",
      className: "btn-small mcp-install-btn",
      "aria-label": `Use ${kind}: ${identifier}`,
      "data-tooltip": identifier,
    },
    `Use ${kind}`,
  ) as HTMLButtonElement;
  btn.addEventListener("click", () => {
    const slug = simplifyName(entry.name);
    switchMode?.(kind, slug, identifier, fields);
  });
  return btn;
}

export function simplifyName(full: string): string {
  const slash = full.lastIndexOf("/");
  const raw = slash >= 0 ? full.slice(slash + 1) : full;
  return (
    raw
      .replace(/[^A-Za-z0-9_-]/g, "-")
      .replace(/^-+|-+$/g, "")
      .slice(0, 48) || "server"
  );
}
