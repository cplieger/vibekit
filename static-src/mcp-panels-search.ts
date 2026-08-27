// ---------------------------------------------------------------------------
// MCP registry search panel — extracted from mcp-panels.ts for isolation.
// ---------------------------------------------------------------------------

import { byId } from "./dom.js";
import { searchRegistry, type RegistrySearchResult } from "./actions/mcp.js";
import {
  subscribeToActions,
  bindLoadingState,
  debouncedDispatch,
  registerCleanup,
} from "./actions/index.js";
import type { DebouncedDispatch } from "./actions/index.js";
import { reconcile } from "./reconcile.js";
import { el } from "@cplieger/reactive";

// --- Types ---

/** Derived from the action's wire type — single source of truth. */
type RegistryEntry = RegistrySearchResult["servers"][number];

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
let searchBtnUnbind: (() => void) | null = null;

/** The newest query the user has asked for. Dispatches are not scoped, so a
 *  slow answer for an abandoned prefix can land after a newer one; without this
 *  the box shows results for a query the user has already typed past. */
let wantedQuery = "";

registerCleanup(() => {
  debouncedSearch?.cancel();
  searchUnsub?.();
  retryBtnUnbind?.();
  searchBtnUnbind?.();
});

// --- Public API ---

/** Wire to call when the user clicks an install button in search results. */
let switchMode: SwitchModeFn | null = null;

export function setSwitchMode(fn: SwitchModeFn): void {
  switchMode = fn;
}

/** Cancel in-flight search work and tear down subscriptions. */
export function cleanupSearch(): void {
  debouncedSearch?.cancel();
  retryBtnUnbind?.();
  retryBtnUnbind = null;
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
  // previous binding has to go or the button collects one per open.
  searchBtnUnbind?.();
  searchBtnUnbind = bindLoadingState("mcp.search_registry", btn);

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
      renderSearchError(results, q);
    }
  });

  /** Schedule or fire a query, or clear the box when there is nothing to ask.
   *  `immediate` is the Enter / button path, which skips the quiet window. */
  const ask = (immediate: boolean): void => {
    const q = input.value.trim();
    wantedQuery = q;
    if (q.length < MIN_QUERY_LEN) {
      retryBtnUnbind?.();
      retryBtnUnbind = null;
      results.replaceChildren();
      debouncedSearch?.cancel();
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
  retryBtnUnbind?.();
  retryBtnUnbind = null;
  results.replaceChildren(el("p", { className: "mcp-empty" }, "Searching the registry…"));
}

function renderSearchResults(
  results: HTMLDivElement,
  d: RegistrySearchResult | undefined,
  q: string,
): void {
  retryBtnUnbind?.();
  retryBtnUnbind = null;
  for (const child of [...results.children]) {
    if ((child as HTMLElement).getAttribute("data-reconcile-key") === null) {
      child.remove();
    }
  }
  if (d == null) {
    renderSearchError(results, q);
    return;
  }
  if (d.servers.length === 0) {
    reconcile(results, [] as RegistryEntry[], {
      key: (e) => e.name,
      mount: () => el("div"),
    });
    results.appendChild(el("p", { className: "mcp-empty" }, `No results for "${q}".`));
    return;
  }
  reconcile(results, d.servers, {
    key: (e: RegistryEntry) => e.name,
    mount: (e: RegistryEntry) => renderRegistryResult(e),
  });
}

function renderSearchError(results: HTMLDivElement, q: string): void {
  retryBtnUnbind?.();
  retryBtnUnbind = null;
  results.replaceChildren();
  results.appendChild(
    el(
      "p",
      { className: "mcp-empty" },
      "Registry unreachable. Use the Remote URL or npm package forms instead.",
    ),
  );
  const retryBtn = el(
    "button",
    { type: "button", className: "btn-small" },
    "Retry",
  ) as HTMLButtonElement;
  retryBtnUnbind = bindLoadingState("mcp.search_registry", retryBtn);
  retryBtn.addEventListener("click", () => {
    // Re-declare the intent: a failed query is not cached server-side, so this
    // is a real re-fetch, and the subscription only renders the wanted query.
    wantedQuery = q;
    void searchRegistry.dispatch({ q });
  });
  results.appendChild(retryBtn);
}

/** One search result row. Exported for its test: the deprecated badge and the
 *  requirements preview are the two things a reader relies on before installing,
 *  and both are decided here. */
export function renderRegistryResult(entry: RegistryEntry): HTMLDivElement {
  const head = el(
    "div",
    { className: "mcp-result-head" },
    el("span", { className: "mcp-result-name" }, entry.title ?? entry.name),
    el("span", { className: "mcp-result-version" }, entry.version ?? ""),
  );
  // The registry still LISTS a deprecated entry (only deleted ones are filtered
  // upstream), so without this badge a dead server reads exactly like a live one.
  const status = entry.status ?? "";
  if (status !== "") {
    head.appendChild(el("span", { className: "mcp-result-status" }, status));
  }

  const row = el(
    "div",
    { className: "mcp-result" },
    head,
    el("p", { className: "mcp-result-desc" }, entry.description ?? entry.name),
  ) as HTMLDivElement;
  if (status !== "") {
    row.classList.add("mcp-result-deprecated");
    const why = (entry.status_message ?? "").trim();
    row.appendChild(
      el(
        "p",
        { className: "mcp-result-status-note" },
        why !== "" ? why : `The registry marks this entry ${status}.`,
      ),
    );
  }

  for (const pkg of entry.packages ?? []) {
    row.appendChild(renderInstallOption(entry, "npm", pkg.identifier, pkg.env_vars ?? [], "env"));
  }
  for (const rem of entry.remotes ?? []) {
    row.appendChild(
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

  return row;
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
): HTMLDivElement {
  const option = el(
    "div",
    { className: "mcp-install-option" },
    renderInstallBtn(entry, kind, identifier, fields),
  ) as HTMLDivElement;
  const preview = renderRequirements(fields, fieldKind);
  if (preview !== null) {
    option.appendChild(preview);
  }
  return option;
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
  // Open when something is required: the case this exists for is a user who did
  // not know a token was needed, and a closed disclosure does not tell them.
  const wrap = el("details", { className: "mcp-requires" }) as HTMLDetailsElement;
  wrap.open = required > 0;
  wrap.append(el("summary", {}, label), list);
  return wrap;
}

function renderInstallBtn(
  entry: RegistryEntry,
  kind: string,
  identifier: string,
  fields: InstallField[],
): HTMLButtonElement {
  const btn = el(
    "button",
    { type: "button", className: "btn-small mcp-install-btn" },
    `Use ${kind}: ${identifier}`,
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
