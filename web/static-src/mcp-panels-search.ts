// ---------------------------------------------------------------------------
// MCP registry search panel — extracted from mcp-panels.ts for isolation.
// ---------------------------------------------------------------------------

import { el } from "./dom.js";
import { searchRegistry, type RegistrySearchResult } from "./actions/mcp.js";
import {
  subscribeToActions,
  bindLoadingState,
  debouncedDispatch,
  registerCleanup,
} from "./actions/index.js";
import type { DebouncedDispatch } from "./actions/index.js";
import { reconcile } from "./reconcile.js";

// --- Types ---

/** Derived from the action's wire type — single source of truth. */
export type RegistryEntry = RegistrySearchResult["servers"][number];

/** Callback to switch the modal to a different panel mode. */
export type SwitchModeFn = (
  kind: string,
  slug: string,
  identifier: string,
  fields: InstallField[],
) => void;

export interface InstallField {
  name: string;
  description?: string | undefined;
  required?: boolean | undefined;
  secret?: boolean | undefined;
}

// --- Module state ---

let debouncedSearch: DebouncedDispatch<{ q: string }> | null = null;
let searchUnsub: (() => void) | null = null;
let retryBtnUnbind: (() => void) | null = null;

registerCleanup(() => {
  debouncedSearch?.cancel();
  searchUnsub?.();
  retryBtnUnbind?.();
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
  searchUnsub?.();
  searchUnsub = null;
}

export function initSearchPanel(): void {
  const input = el<HTMLInputElement>("mcp-search-input");
  const results = el<HTMLDivElement>("mcp-search-results");
  const btn = el<HTMLButtonElement>("mcp-search-btn");
  input.value = "";
  results.replaceChildren();
  input.focus();

  searchUnsub?.();
  debouncedSearch = debouncedDispatch(searchRegistry, { wait: 200 });

  searchUnsub = subscribeToActions((inst) => {
    if (inst.name !== "mcp.search_registry") {
      return;
    }
    if (inst.status === "success") {
      const d = inst.result as RegistrySearchResult | undefined;
      const q = (inst.args as { q: string }).q;
      renderSearchResults(results, d, q);
    } else if (inst.status === "error") {
      renderSearchError(results, (inst.args as { q: string }).q);
    }
  });

  input.oninput = (): void => {
    const q = input.value.trim();
    if (q === "") {
      retryBtnUnbind?.();
      retryBtnUnbind = null;
      results.replaceChildren();
      debouncedSearch!.cancel(); // eslint-disable-line @typescript-eslint/no-non-null-assertion
      return;
    }
    debouncedSearch!({ q }); // eslint-disable-line @typescript-eslint/no-non-null-assertion
  };

  input.onkeydown = (e: KeyboardEvent): void => {
    if (e.key === "Enter") {
      e.preventDefault();
      const q = input.value.trim();
      if (q === "") {
        return;
      }
      void debouncedSearch!.flush({ q }); // eslint-disable-line @typescript-eslint/no-non-null-assertion
    }
  };

  btn.onclick = (): void => {
    const q = input.value.trim();
    if (q === "") {
      return;
    }
    void debouncedSearch!.flush({ q }); // eslint-disable-line @typescript-eslint/no-non-null-assertion
  };
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
      mount: () => document.createElement("div"),
    });
    const empty = document.createElement("p");
    empty.className = "mcp-empty";
    empty.textContent = `No results for "${q}".`;
    results.appendChild(empty);
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
  const err = document.createElement("p");
  err.className = "mcp-empty";
  err.textContent = "Registry unreachable. Use the Remote URL or npm package forms instead.";
  results.appendChild(err);
  const retryBtn = document.createElement("button");
  retryBtn.type = "button";
  retryBtn.className = "btn-small";
  retryBtn.textContent = "Retry";
  retryBtnUnbind = bindLoadingState("mcp.search_registry", retryBtn);
  retryBtn.addEventListener("click", () => {
    void searchRegistry.dispatch({ q });
  });
  results.appendChild(retryBtn);
}

function renderRegistryResult(entry: RegistryEntry): HTMLDivElement {
  const row = document.createElement("div");
  row.className = "mcp-result";

  const head = document.createElement("div");
  head.className = "mcp-result-head";
  const name = document.createElement("span");
  name.className = "mcp-result-name";
  name.textContent = entry.title ?? entry.name;
  const version = document.createElement("span");
  version.className = "mcp-result-version";
  version.textContent = entry.version ?? "";
  head.append(name, version);

  const desc = document.createElement("p");
  desc.className = "mcp-result-desc";
  desc.textContent = entry.description ?? entry.name;

  row.append(head, desc);

  for (const pkg of entry.packages ?? []) {
    row.appendChild(renderInstallBtn(entry, "npm", pkg.identifier, pkg.env_vars ?? []));
  }
  for (const rem of entry.remotes ?? []) {
    row.appendChild(
      renderInstallBtn(
        entry,
        rem.type,
        rem.url,
        (rem.headers ?? []).map((h) => ({
          name: h.name,
          description: h.description,
          required: h.required,
          secret: h.secret,
        })),
      ),
    );
  }

  return row;
}

function renderInstallBtn(
  entry: RegistryEntry,
  kind: string,
  identifier: string,
  fields: InstallField[],
): HTMLButtonElement {
  const btn = document.createElement("button");
  btn.type = "button";
  btn.className = "btn-small mcp-install-btn";
  btn.textContent = `Use ${kind}: ${identifier}`;
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
