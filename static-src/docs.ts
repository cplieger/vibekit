// ---------------------------------------------------------------------------
// The Kiro configuration browser: one page over everything in `.kiro/`.
//
// Replaces two surfaces with one — the deleted spec board (whose toolbar slot
// this took) and the "Steering docs, skills & agents" list that was buried in
// Settings → Instructions.
//
// It is SUB-TABBED, one tab per category, not one table. The categories are not
// one kind of thing: a steering doc's most useful fact is its inclusion mode, an
// agent's is its model, a spec has no front-matter at all and is identified by
// its feature directory, and a hook is JSON with a trigger. A single column spec
// would be fighting the data — and the sub-tabs also dissolve the caps question
// (per-tab, not one budget) and the scope question (each tab names its own
// source, so a file no tab claims simply gets no row).
//
// The tab bar reuses the Settings idiom verbatim (pills on desktop, a native
// select on mobile, deep-linkable, roving focus), so mobile is solved on
// arrival rather than being a second tab vocabulary.
//
// Row click opens the document in the file editor; the server row already
// carries the path, so there is no resolution step here.
// ---------------------------------------------------------------------------

import { apiGet, apiGetTyped, type Decoder } from "./api-client.js";
import { defineAction, ActionError, retryNetwork, registerCleanup } from "./actions/index.js";
import { setHookEnabled } from "./actions/hooks.js";
import { asObject, decodeArray, optStr, reqBool, reqStr } from "./validators.js";
import { onSSE } from "./bus.js";
import { $ } from "./dom.js";
import { swapViews } from "./view-swap.js";
import { el } from "@cplieger/reactive";
import { iconEl } from "./icon-el.js";
import { ICON_EDIT, ICON_TRASH } from "./icons.js";
import { confirm as confirmDialog } from "./confirm.js";
import { deleteDoc } from "./actions/docs.js";
import { initGitStatusStore, onGitStatusChange, statusFor } from "./git-status-store.js";
import { describeStatus } from "./git-types.js";
import { openFile } from "./editor-openers.js";
import { reconcile } from "./reconcile.js";
import { join as joinKey } from "@cplieger/keyenc";
import { rovingFocus } from "@cplieger/ui-primitives/roving-focus";
import { signal, subscribe } from "@cplieger/reactive";
import { skeletonTiming } from "@cplieger/ui-primitives/skeleton";
import { pushRoute } from "./router.js";
import type { DocsTab } from "./router.js";
import { renderRecipesPanel, setRecipeCountsListener } from "./recipes.js";
import { setDocsTab as setTabRoute, toggleDocsView } from "./tabs.js";
import { createSearchPopup } from "./search-popup.js";
import type { SearchPopup } from "./search-popup.js";
import { registerFind } from "./find-registry.js";

/** One document row, as the server reports it. Fields are per-category and
 *  mostly optional — see the endpoint's own note on why they are not uniform. */
interface KiroDoc {
  category: string;
  name: string;
  path: string;
  group?: string;
  description?: string;
  inclusion?: string;
  file_match?: string;
  model?: string;
  trigger?: string;
  action?: string;
  tools?: string[];
  steering_override?: boolean;
  /** The row is not writable, so it renders without the edit or delete
   *  affordance. Absent means writable — a restriction is ASSERTED by the server,
   *  never inferred from a missing field (same direction as `mcp-state.ts`'s
   *  `adaptOrigin`).
   *
   *  NOTHING SETS IT TODAY. It briefly meant "reached through a symlink, so the
   *  save would fail with ELOOP", and that premise was false: the server resolves
   *  the link and applies `O_NOFOLLOW` to the canonical target, so the save
   *  succeeds. The field stays as the provenance channel because it is the right
   *  shape for a real writability source; what went is the wrong derivation. */
  read_only?: boolean;
  /** The row's DELETE must not be offered, but its edit still may be.
   *
   *  A separate field because it is a separate question. The server sets it when
   *  the entry's own final component is a symlink: the delete route canonicalizes
   *  the path, so removing the alias removes the file it points at — which is
   *  listed as its own row on this same page. Editing through a link writes the
   *  target, which is what following a link means; deleting through it destroys a
   *  row the reader never touched. */
  delete_protected?: boolean;
  /** Which scope's hook this row is. NOT a wire field — the client sets it,
   *  because it can: `kiroRoots()` walks the workspace's `.kiro` trees and
   *  nothing else, so a SCANNED row is workspace by construction, and a
   *  synthesized row is global by construction. Absent means workspace.
   *
   *  It is the third component of the join key, and it has to be. The two path
   *  shapes normalize to the same `.kiro/...` tail, so a workspace hook and a
   *  global hook with the same relative path and name shared one key — and since
   *  GET /api/hooks lists workspace first then global, the global state
   *  overwrote the workspace one. The scanned workspace row then joined to the
   *  global state, lost open and delete through the global gate, and toggled the
   *  GLOBAL hook's id, while the real global row was suppressed as claimed. */
  hook_scope?: HookScope;
}

/** The two scopes a hook row belongs to. A workspace hook and a global hook are
 *  different files with different affordances even when their relative path and
 *  their name match, so scope is part of a hook's identity, not a label on it. */
type HookScope = "workspace" | "global";

// ---------------------------------------------------------------------------
// Hooks: the one tab that is not a pure projection of /api/workspace/kiro-docs.
//
// A hook has STATE the file scan cannot see — enabled, and the reason KAS
// disabled it — so the tab joins the scan's rows against GET /api/hooks. It also
// has rows the scan cannot see AT ALL: `kiroRoots()` enumerates the workspace's
// `.kiro` trees and nothing else, while a GLOBAL hook lives under the container
// HOME, so those rows exist only on the hooks endpoint and are synthesized here.
//
// THE JOIN KEY IS (path, name), not path. `hookRows` in kiro_docs.go expands one
// v1 envelope into one row PER HOOK, all sharing a Path, which is why the tab's
// reconcile key was already `d:${path}:${name}`. Keying the join on path alone
// would apply one hook's toggle to every hook in its file.
//
// THE TWO PATH SHAPES ARE DIFFERENT and have to be normalized. `hookInfo.FilePath`
// is workDir-relative (`.kiro/hooks/x.json`); `kiroDoc.Path` carries the work
// directory without its leading slash (`workspace/.kiro/hooks/x.json`) because
// that is the spelling the editor and the delete action take. So the join
// normalizes both to the `.kiro/...` tail rather than trusting either.
// ---------------------------------------------------------------------------

/** A hook's live state, from GET /api/hooks. Narrowed to what the tab needs:
 *  `enabled` + `disabled_reason` are the state no file scan can know, `scope`
 *  decides which affordances the row may offer, `file_path` is the join key and
 *  `id` is what setEnabled addresses.
 *
 *  `trigger` / `command` / `prompt` are display-only and exist for ONE case: a
 *  global hook has no docs row, so this is the only source for its whole row. */
interface HookState {
  id: string;
  name: string;
  scope?: string;
  enabled: boolean;
  disabled_reason?: string;
  file_path?: string;
  trigger?: string;
  command?: string;
  prompt?: string;
  /** The regex KAS tests this hook's trigger subject against. Display-only. */
  matcher?: string;
  /** What is wrong with the trigger-and-matcher pairing, computed SERVER-side
   *  (internal/vibekit's ClassifyHookMatcher) so the trigger-to-subject table
   *  exists once. `missing_tool_matcher` = a tool trigger with no matcher, so the
   *  hook runs on every tool call; `ineffective` = a matcher on a trigger with
   *  nothing to match on, so it governs nothing. Absent = nothing to say.
   *
   *  Never derived here. A TypeScript copy of that table could disagree with the
   *  Go one about a trigger's subject, and the subject is the whole judgement. */
  matcher_warning?: string;
}

const HP = "$.hook";

const decodeHook: Decoder<HookState> = (v) => {
  const o = asObject(v, HP);
  const out: HookState = {
    id: reqStr(o, "id", HP),
    name: reqStr(o, "name", HP),
    enabled: reqBool(o, "enabled", HP),
  };
  for (const key of [
    "scope",
    "disabled_reason",
    "file_path",
    "trigger",
    "command",
    "prompt",
    "matcher",
    "matcher_warning",
  ] as const) {
    const val = optStr(o, key, HP);
    if (val !== undefined) {
      out[key] = val;
    }
  }
  return out;
};

const decodeHookList: Decoder<{ hooks: HookState[] }> = (v) => {
  const o = asObject(v, "$.hooks");
  return { hooks: decodeArray(o["hooks"], decodeHook, "$.hooks.hooks") };
};

/** Global hooks live in ~/.kiro/hooks (kiro-cli 2.13+) and apply in every
 *  workspace. Scope is DERIVED server-side from the hook's file path, because the
 *  wire carries no scope field; an absent value (an older server) counts as
 *  workspace, which is the safe direction — it grants the file affordances a
 *  workspace file legitimately has. */
function isGlobalHook(h: HookState): boolean {
  return h.scope === "global";
}

/** A hook's scope as the join key needs it: the endpoint's own explicit value,
 *  with the same safe default `isGlobalHook` applies. */
function hookScopeOf(h: HookState): HookScope {
  return isGlobalHook(h) ? "global" : "workspace";
}

/** A ROW's scope. Absent means workspace, which is what every scanned row is. */
function rowScope(doc: KiroDoc): HookScope {
  return doc.hook_scope ?? "workspace";
}

/** Normalize either path shape to its `.kiro/...` tail, which is the only part
 *  the two endpoints agree on. Returns "" for a path with no `.kiro` segment
 *  (a global hook's `~/...` display path has one; an absolute fallback may not),
 *  and a row that cannot produce a key simply does not join. */
function hookPathKey(path: string): string {
  const idx = path.indexOf(".kiro/");
  return idx < 0 ? "" : path.slice(idx);
}

/** The join key: (scope, normalized path, name).
 *
 *  keyenc rather than a template literal: a hook NAME is arbitrary text from a
 *  JSON file, so a separator inside one could otherwise forge another hook's key
 *  and hand it the wrong toggle.
 *
 *  SCOPE leads, because the path normalization above deliberately discards the one
 *  thing that told the two scopes apart — `~/.kiro/hooks/x.json` and
 *  `workspace/.kiro/hooks/x.json` share a tail. Without it the two collide, and
 *  the collision is not symmetric: the endpoint loads workspace then global, so
 *  the global hook wins the key and the workspace row inherits its id and its
 *  gates. */
function hookKey(scope: HookScope, path: string, name: string): string {
  return joinKey(scope, hookPathKey(path), name);
}

/** Tab order is fixed: the page always reads Steering · Skills · Agents ·
 *  Specs · Hooks. Workflows is deliberately absent — a recipe is not a `.kiro`
 *  file (the bundled ones are compiled into KAS and agent-authored ones land
 *  under the sessions path), so it is sourced from an RPC and arrives with the
 *  run-launch work rather than here. */
export const DOCS_TABS: readonly DocsTab[] = [
  "steering",
  "skills",
  "agents",
  "specs",
  "hooks",
  "workflows",
] as const;

const TAB_LABELS: Readonly<Record<DocsTab, string>> = {
  steering: "Steering",
  skills: "Skills",
  agents: "Agents",
  specs: "Specs",
  hooks: "Hooks",
  workflows: "Workflows",
};

/** Wire category (the server's value) per tab. Workflows has NO category: it
 *  is RPC-sourced (a recipe is not a `.kiro` file — the bundled ones are
 *  compiled into KAS and agent-authored ones live under KAS's sessions tree),
 *  so renderActive hands its panel to recipes.ts instead of filtering docs. */
const TAB_CATEGORY: Readonly<Record<Exclude<DocsTab, "workflows">, string>> = {
  steering: "steering",
  skills: "skill",
  agents: "agent",
  specs: "spec",
  hooks: "hook",
};

const EMPTY_TEXT: Readonly<Record<Exclude<DocsTab, "workflows">, string>> = {
  steering: "No steering docs in .kiro/steering/.",
  skills: "No skills in .kiro/skills/.",
  agents: "No custom agents in .kiro/agents/.",
  specs: "No specs in .kiro/specs/.",
  hooks: "No hooks in .kiro/hooks/.",
};

// --- State ---

const activeTab = signal<DocsTab>("steering");
/** Last fetched inventory. Kept so a tab switch repaints from memory rather
 *  than refetching ~200 parsed documents. */
let docs: KiroDoc[] = [];
/** Last fetched hook state, keyed by (normalized path, name). Kept for the same
 *  reason `docs` is, and separately from it because the two have different
 *  invalidation triggers: the inventory refetches on `settings_updated`, hook
 *  state on `hooks_changed`. */
let hooks = new Map<string, HookState>();
let inited = false;
/** The folded query the metadata filter is applying.
 *
 *  A FILTER, not a search: everything it matches on is already in memory, so
 *  there is no request and no truncation to report. It carries no match-case
 *  toggle because every filter in this app folds the query AND the row it matches
 *  it against, so a toggle would be wired to nothing.
 *
 *  Metadata only, and stated because it bounds the answer: the inventory carries
 *  a name, a description, a path, front-matter and a hook's trigger, never a
 *  document's BODY. Searching bodies is what the file browser's recursive grep
 *  is for, one view away. */
let filterText = "";

const loadDocsAction = defineAction<undefined, { docs: KiroDoc[] }>({
  name: "docs.load",
  retryable: retryNetwork,
  retry: { count: 2, delay: 300 },
  run: async (_args, sig) => {
    const data = await apiGet<{ docs: KiroDoc[] }>("/api/workspace/kiro-docs", sig);
    if (sig.aborted) {
      throw new DOMException("aborted", "AbortError");
    }
    if (data === null) {
      throw new ActionError("Failed to load .kiro documents", { code: "network" });
    }
    return data;
  },
  error: false,
});

// --- Public API ---

/** Open (or toggle) the docs page, landing on `tab`. The toolbar's book button
 *  and the router both come through here.
 *
 *  No onShow callback: the tab factory reaches this module's own `loadDocsView`
 *  through a lazy import, so every door into the page gets the same behaviour and
 *  the sub-tab is applied by `toggleDocsView`'s own `setDocsTab` afterwards. */
export function showDocsView(tab: DocsTab = "steering"): void {
  void toggleDocsView(tab);
}

/** Load (or reload) the page without touching the tab, the way
 *  `loadHistoryView` does and for the same reason: the tab-restore path cannot
 *  call `showDocsView`, which TOGGLES, so firing it from the `onShow` of an
 *  already-open tab would close the tab it was meant to fill.
 *
 *  It must run `initDocsView` too. The restore path used to call
 *  `forceDocsTab` + `loadDocs` only, so a docs tab restored at boot loaded its
 *  rows and never registered its find — leaving the toolbar's magnifier absent
 *  for the whole session on the one path where the user had the page open last
 *  time. `initDocsView` is one-shot, so calling it from both doors is free. */
export function loadDocsView(tab: DocsTab = "steering"): void {
  forceDocsTab(tab);
  initDocsView();
  loadDocs();
}

/** Set the active tab without pushing a URL — the router's entry point when
 *  back/forward lands on /docs/<tab>. */
export function forceDocsTab(tab: DocsTab): void {
  setTabRoute(tab);
  activeTab.value = tab;
}

/** Fetch (or refetch) the inventory and repaint. */
export function loadDocs(): void {
  loadDocsAction.cancel();
  const skeleton = skeletonTiming(() => showSkeleton());
  void loadDocsAction.dispatch(undefined, {
    onSuccess: (d) => {
      skeleton.cancel();
      docs = d.docs;
      renderActive();
    },
    onError: () => {
      skeleton.cancel();
      panelFor(activeTab.peek())?.replaceChildren(
        el("div", { className: "list-empty" }, "Failed to load .kiro documents"),
      );
    },
  });
  loadHookState();
}

/** Fetch the hook state and repaint.
 *
 *  Best-effort and deliberately NOT folded into loadDocs's action: a hooks
 *  endpoint failure must not blank the Steering tab, and the four tabs that need
 *  nothing from it must not wait on it. When it fails, a workspace hook still
 *  renders from its docs row minus the toggle; a global hook has no row to fall
 *  back to and simply does not appear, which is the honest degradation — inventing
 *  a row for a file nothing reported would be worse. */
function loadHookState(): void {
  void apiGetTyped("/api/hooks", decodeHookList).then((d) => {
    if (d === null) {
      return;
    }
    const next = new Map<string, HookState>();
    for (const h of d.hooks) {
      next.set(hookKey(hookScopeOf(h), h.file_path ?? "", h.name), h);
    }
    hooks = next;
    if (activeTab.peek() === "hooks") {
      renderActive();
    }
  });
}

// --- Tab bar wiring (the Settings idiom) ---

function initDocsView(): void {
  if (inited) {
    return;
  }
  inited = true;
  const bar = $.docsTabBar;
  const select = $.docsTabSelect;

  bar.setAttribute("role", "tablist");
  bar.setAttribute("aria-label", "Kiro document categories");

  for (const tab of DOCS_TABS) {
    const btn = bar.querySelector<HTMLButtonElement>(`[data-docs-tab="${tab}"]`);
    if (btn === null) {
      continue;
    }
    btn.setAttribute("role", "tab");
    btn.id = `docs-tab-${tab}`;
    btn.setAttribute("aria-label", TAB_LABELS[tab]);
    btn.setAttribute("aria-controls", `docs-panel-${tab}`);
    btn.addEventListener("click", () => {
      selectTab(tab);
    });
  }
  select.addEventListener("change", () => {
    const v = select.value;
    if (isDocsTab(v)) {
      selectTab(v);
    }
  });
  rovingFocus(bar, "[data-docs-tab]", { orientation: "horizontal" });
  // Hand Ctrl-F this page's entry point. Through the LEAF registry, not the
  // dispatcher: importing find-dispatch here would drag find-in-chat and
  // scroll.ts's self-initialising singleton into this lazily-loaded page.
  //
  // No `available` predicate: every one of the six tabs is filterable now that
  // Workflows narrows its recipes too, so the toolbar's magnifier always has a
  // destination here. It used to answer `activeTab.value !== "workflows"`.
  registerFind("docs", docsFilter);
  // The Workflows panel owns its own rows, so it reports what the filter is
  // showing rather than the page inferring it. Wired once, here, because the
  // panel repaints on its own schedule (its run poll, its schedules fetch).
  setRecipeCountsListener(({ total, shown }) => {
    docsFilter.shell?.setNote(filterNote(total, shown));
  });

  subscribe(activeTab, (tab) => {
    syncTabChrome(tab);
    renderActive();
  });

  // Git letters ride the shared status poll: no new server call and no second
  // timer, which is the whole reason the store exists. Started here as well as
  // from the badge, because the page must not depend on which surface the user
  // opened first (both calls are idempotent).
  initGitStatusStore();
  registerCleanup(
    onGitStatusChange(() => {
      renderActive();
    }),
  );
  // An edit — by the user or the agent — should reflect without a reopen.
  registerCleanup(
    onSSE("settings_updated", () => {
      if ($.docsView.offsetParent !== null) {
        loadDocs();
      }
    }),
  );
  // Hook state has its OWN broadcast and needs it: `settings_updated` does not
  // fire for a hook file, and the docs scan is memoized behind a signature of each
  // category directory's mtime AND its entry names — so an in-place edit to a hook
  // file changes neither and that endpoint would serve the old trigger forever.
  // KAS watches the tree and emits `_kiro/hooks/didChange`, which the server turns
  // into this event, so a hook hand-edited AS A FILE reaches the tab. Both halves
  // refetch: the body edit is the inventory's, the enabled flag is the endpoint's.
  registerCleanup(
    onSSE("hooks_changed", () => {
      if ($.docsView.offsetParent !== null) {
        loadDocs();
      }
    }),
  );
  // The toggle is delegated on the container, like the delete button: rows are
  // reconciled, so a per-row listener would be rebound on every repaint.
  $.docsView.addEventListener("change", (e) => {
    const target = e.target as HTMLElement;
    if (!target.classList.contains("hook-toggle")) {
      return;
    }
    const id = target.closest<HTMLElement>("[data-hook-id]")?.getAttribute("data-hook-id") ?? "";
    if (id !== "") {
      // Server-canonical: dispatch, then refetch so the checkbox reconciles from
      // authoritative state — which also re-syncs it when the write failed.
      void setHookEnabled
        .dispatch({ id, enabled: (target as HTMLInputElement).checked })
        .then(() => {
          loadHookState();
        });
    }
  });
}

function selectTab(tab: DocsTab): void {
  if (tab === activeTab.peek()) {
    return;
  }
  setTabRoute(tab);
  pushRoute({ kind: "docs", tab });
  activeTab.value = tab;
}

function isDocsTab(v: string): v is DocsTab {
  return DOCS_TABS.includes(v as DocsTab);
}

function syncTabChrome(tab: DocsTab): void {
  const bar = $.docsTabBar;
  for (const t of DOCS_TABS) {
    const btn = bar.querySelector<HTMLButtonElement>(`[data-docs-tab="${t}"]`);
    btn?.classList.toggle("active", t === tab);
    btn?.setAttribute("aria-selected", t === tab ? "true" : "false");
    btn?.setAttribute("tabindex", t === tab ? "0" : "-1");
  }
  $.docsTabSelect.value = tab;
  swapViews(() => {
    let active: HTMLElement | null = null;
    for (const panel of document.querySelectorAll<HTMLDivElement>("[data-docs-panel]")) {
      const panelTab = panel.dataset["docsPanel"] ?? "";
      const isActive = panelTab === tab;
      panel.classList.toggle("hidden", !isActive);
      panel.setAttribute("role", "tabpanel");
      panel.id = `docs-panel-${panelTab}`;
      panel.setAttribute("aria-labelledby", `docs-tab-${panelTab}`);
      if (isActive) {
        active = panel;
      }
    }
    return active;
  });
  const title = document.getElementById("docs-page-title");
  if (title !== null) {
    title.textContent = TAB_LABELS[tab];
  }
}

function panelFor(tab: DocsTab): HTMLDivElement | null {
  return document.querySelector<HTMLDivElement>(`[data-docs-panel="${tab}"]`);
}

/** How much of the tab the filter is showing. Silent with no filter: the list is
 *  the whole answer then, and a count restating it is noise. */
function filterNote(total: number, shown: number): string {
  if (filterText === "") {
    return "";
  }
  return `${String(shown)} of ${String(total)} shown.`;
}

/** Build and mount the metadata filter.
 *
 *  A POPUP since the search-box audit, not the permanent in-flow field it was:
 *  the page's box now looks and behaves like the transcript's, opens from the
 *  toolbar's magnifier or Ctrl-F, and closes on Escape — and the close clears the
 *  query, because a hidden box holding `redis` would leave this page showing
 *  three of forty rows with nothing on screen saying why. */
const docsFilter: SearchPopup = createSearchPopup<null>({
  id: "docs-filter",
  // A FILTER, so it carries the funnel: everything it matches on is already in
  // memory, and it can only hide rows that are here.
  kind: "filter",
  label: "Filter documents",
  // Names what it reaches, and it reaches metadata only: never a document's
  // body, which is the file browser's recursive grep one view away.
  placeholder: "Filter by name, description or trigger\u2026",
  note: true,
  host: () => document.getElementById("docs-view"),
  // Synchronous by nature: the inventory is already here. The filter is
  // applied in renderActive, which is the ONE place that decides which records
  // a tab shows, so this hands the work there rather than keeping a second
  // copy of the decision.
  query: (query) => {
    filterText = query.trim().toLowerCase();
    return null;
  },
  render: () => {
    renderActive();
  },
});

// --- Rendering ---

/** A rendered entry: either a group separator or a document row. Specs and
 *  hooks nest under a group; the flat categories emit rows only. */
type Entry = { kind: "group"; label: string } | { kind: "doc"; doc: KiroDoc };

/** Every field of a row a reader could plausibly type at, folded once.
 *
 *  Built per call rather than cached on the record: the inventory is refetched
 *  whole on `settings_updated`, so a cache would need the same invalidation
 *  `rowSig` already has and would buy nothing over ~200 rows. */
function filterHaystack(doc: KiroDoc): string {
  return [
    doc.name,
    doc.description ?? "",
    doc.path,
    doc.group ?? "",
    doc.inclusion ?? "",
    doc.file_match ?? "",
    doc.model ?? "",
    doc.trigger ?? "",
    doc.action ?? "",
    ...(doc.tools ?? []),
  ]
    .join("\n")
    .toLowerCase();
}

function renderActive(): void {
  const tab = activeTab.peek();
  const container = panelFor(tab);
  if (container === null) {
    return;
  }
  // Workflows filters too, and it reaches recipes.ts to do it. That tab is
  // RPC-sourced and escapes before any docs logic runs, so the filter cannot be
  // applied HERE — but "no inventory of its own" was never the same claim as
  // "nothing to filter", and hiding the box was answering the second with the
  // first. The panel reports its own counts through the listener wired in
  // initDocsView, so the note reads the same on all six tabs.
  if (tab === "workflows") {
    renderRecipesPanel(container, filterText);
    return;
  }
  const all = tab === "hooks" ? hookRows() : docs.filter((d) => d.category === TAB_CATEGORY[tab]);
  const rows = filterText === "" ? all : all.filter((d) => filterHaystack(d).includes(filterText));
  docsFilter.shell?.setNote(filterNote(all.length, rows.length));
  if (rows.length === 0) {
    // The category's empty text is a LIE under an active filter — "No steering
    // docs in .kiro/steering/." when 47 of them are one keystroke away. The
    // git changes tab already makes this distinction and for the same reason.
    container.replaceChildren(
      el(
        "div",
        { className: "list-empty" },
        filterText === "" ? EMPTY_TEXT[tab] : "No documents match the filter.",
      ),
    );
    return;
  }
  const flat = groupEntries(rows);

  // Drop any non-keyed placeholder (skeleton / empty state) before reconcile.
  for (const child of [...container.children]) {
    if (child.getAttribute("data-reconcile-key") === null) {
      child.remove();
    }
  }
  reconcile(container, flat, {
    key: (e: Entry) => (e.kind === "group" ? `g:${e.label}` : `d:${e.doc.path}:${e.doc.name}`),
    mount: (e: Entry) => (e.kind === "group" ? groupRow(e.label) : docRow(e.doc)),
    update: updateRow,
  });
}

/** Repaint a kept row when what it renders changed.
 *
 *  Required by the hook toggle, and it was already missing for everything else.
 *  `reconcile` LEAVES a keyed row alone without this, and a row's key is its path
 *  plus its name — neither of which moves when a hook is enabled, a steering doc's
 *  inclusion mode is edited, or the git poll changes a letter. So every repaint
 *  after the first was a no-op for content, and the toggle would have shown the
 *  state it was mounted with forever however many times the server was refetched.
 *
 *  Rebuilds the row's CHILDREN rather than replacing the row, so reconcile keeps
 *  tracking the node it placed. */
function updateRow(row: HTMLElement, e: Entry): void {
  if (e.kind === "group") {
    return;
  }
  const sig = rowSig(e.doc);
  if (row.getAttribute("data-sig") === sig) {
    return;
  }
  row.setAttribute("data-sig", sig);
  row.replaceChildren(...rowParts(e.doc));
}

/** Everything a row renders, folded into one comparable string.
 *
 *  keyenc `join` rather than a template literal, and the nested arrays get their
 *  own `join` so their contents cannot reach the outer field boundaries: every
 *  component here is arbitrary text from a file on disk (a name, a description, a
 *  hook command), so a separator inside one could otherwise make two different
 *  rows compare equal. The consequence of a collision is a STALE row rather than a
 *  wrong one, since identity is the reconcile key — but a stale toggle is exactly
 *  the bug this function exists to prevent. */
function rowSig(doc: KiroDoc): string {
  const hook = hookFor(doc);
  const { repo, rel } = splitRepoPath(doc.path);
  return joinKey(
    doc.name,
    doc.description ?? "",
    doc.inclusion ?? "",
    doc.file_match ?? "",
    doc.model ?? "",
    doc.trigger ?? "",
    doc.action ?? "",
    joinKey(...(doc.tools ?? [])),
    doc.steering_override === true ? "1" : "0",
    doc.read_only === true ? "1" : "0",
    doc.delete_protected === true ? "1" : "0",
    repo === "" ? "" : statusFor(repo, rel),
    hook === undefined
      ? ""
      : joinKey(
          hook.id,
          hook.enabled ? "1" : "0",
          hook.scope ?? "",
          hook.disabled_reason ?? "",
          // Both, because both are RENDERED. Without them a hook whose matcher
          // was edited on disk keeps the badge it mounted with, which is the
          // exact class of staleness this signature exists to prevent.
          hook.matcher ?? "",
          hook.matcher_warning ?? "",
        ),
  );
}

/** The Hooks tab's rows: the scanned workspace hooks, then the global ones the
 *  scan cannot see.
 *
 *  Global rows come LAST rather than interleaved, matching the server's own order
 *  on GET /api/hooks (workspace before global, `hookScopeRank`), so the two
 *  surfaces present the same list in the same sequence. */
function hookRows(): KiroDoc[] {
  const scanned = docs.filter((d) => d.category === TAB_CATEGORY.hooks);
  const globals: KiroDoc[] = [];
  for (const h of hooks.values()) {
    // GLOBAL only, and that is the WHOLE test. A workspace hook the scan did not
    // report means the two surfaces disagree about the workspace, and synthesizing
    // it would build a row carrying the hooks endpoint's workDir-RELATIVE path —
    // which neither openFile nor the delete action accepts — while rowGates,
    // seeing a workspace hook, would hand it both. A missing row is a better
    // answer than a row with two controls that fail.
    //
    // There is no already-claimed check beside it, and adding one back is the bug:
    // the scan's only reach is the workspace (`kiroRoots()`), so no scanned row can
    // ever BE this global hook, and the check that used to sit here compared
    // scope-blind keys — so a workspace hook with the same relative path and name
    // suppressed the global row entirely.
    if (!isGlobalHook(h)) {
      continue;
    }
    globals.push(synthesizedHookDoc(h));
  }
  return [...scanned, ...globals];
}

/** A row for a hook the docs scan never saw.
 *
 *  Its `path` is the hook's DISPLAY path (`~/.kiro/hooks/x.json`) and is not a
 *  path any endpoint accepts — which is exactly right, because the row it builds
 *  offers neither open nor delete. It is here to key the reconcile and to carry
 *  the file name into the group label.
 *
 *  Non-global hooks are never synthesized: one outside the scan's reach that is
 *  NOT global means the two surfaces disagree about the workspace, and inventing a
 *  row would paper over that with a row whose affordances would then be wrong. */
function synthesizedHookDoc(h: HookState): KiroDoc {
  const path = h.file_path ?? "";
  const out: KiroDoc = {
    category: TAB_CATEGORY.hooks,
    name: h.name,
    path,
    group: path.slice(path.lastIndexOf("/") + 1),
    // Global by construction: this function is only reached for a hook the scan
    // cannot see, and the scan sees the whole workspace. Stamped so the row keys
    // back to the state it was built from rather than to a same-named workspace
    // hook's.
    hook_scope: "global",
  };
  if (h.trigger !== undefined && h.trigger !== "") {
    out.trigger = h.trigger;
  }
  const action = h.command ?? h.prompt ?? "";
  if (action !== "") {
    out.action = action;
  }
  return out;
}

/** The hook state a row joins to, or undefined for a row the endpoint did not
 *  report (its own fetch failed, or the file changed under the inventory). */
function hookFor(doc: KiroDoc): HookState | undefined {
  if (doc.category !== TAB_CATEGORY.hooks) {
    return undefined;
  }
  return hooks.get(hookKey(rowScope(doc), doc.path, doc.name));
}

/** Insert a separator whenever the group changes. The server already returns
 *  each category in its intended order (specs sorted requirements → design →
 *  tasks → lexical), so this only has to notice the boundaries. */
function groupEntries(rows: KiroDoc[]): Entry[] {
  const flat: Entry[] = [];
  let current: string | null = null;
  for (const doc of rows) {
    const group = doc.group ?? "";
    // "." is the server's marker for "directly in the category root".
    const label = group === "." ? "" : group;
    if (label !== "" && label !== current) {
      flat.push({ kind: "group", label });
      current = label;
    }
    flat.push({ kind: "doc", doc });
  }
  return flat;
}

function groupRow(label: string): HTMLElement {
  return el("div", { className: "list-group-label" }, label);
}

/** The per-category metadata cell. Each category shows the fact that answers
 *  the question its page gets asked. */
function metaFor(doc: KiroDoc): HTMLElement[] {
  const out: HTMLElement[] = [];
  switch (doc.category) {
    case "steering":
    case "skill": {
      // Inclusion is the most useful fact here: it answers "is this doc costing
      // me tokens on every session, or only when I touch its files".
      const mode = doc.inclusion ?? "";
      if (mode !== "") {
        // The class is lowercased ("fileMatch" → filematch): CSS class names are
        // kebab/lower by convention and the label keeps the camelCase spelling.
        const badge = el(
          "span",
          { className: `docs-badge docs-badge-${mode.toLowerCase()}` },
          mode,
        );
        if (doc.file_match !== undefined && doc.file_match !== "") {
          badge.setAttribute("data-tooltip", doc.file_match);
        }
        out.push(badge);
      }
      if (doc.steering_override === true) {
        const marker = el("span", { className: "docs-badge docs-badge-override" }, "override");
        marker.setAttribute("data-tooltip", "Replaces the steering set while this skill runs");
        out.push(marker);
      }
      break;
    }
    case "agent": {
      if (doc.model !== undefined && doc.model !== "") {
        out.push(el("span", { className: "docs-badge docs-badge-model" }, doc.model));
      }
      const tools = doc.tools ?? [];
      if (tools.length > 0) {
        const chip = el(
          "span",
          { className: "docs-badge" },
          `${String(tools.length)} tool${tools.length === 1 ? "" : "s"}`,
        );
        chip.setAttribute("data-tooltip", tools.join(", "));
        out.push(chip);
      }
      break;
    }
    case "hook": {
      if (doc.trigger !== undefined && doc.trigger !== "") {
        out.push(el("span", { className: "docs-badge docs-badge-trigger" }, doc.trigger));
      }
      // The matcher beside its trigger, because the pair is one fact: a trigger
      // says WHEN and its matcher says WHICH, and the row was showing only half.
      // Rendered as code, since it is a regex the reader may need to compare
      // against a tool name or a path character for character.
      const matcher = hookFor(doc)?.matcher ?? "";
      if (matcher !== "") {
        const badge = el("code", { className: "docs-badge docs-badge-matcher" }, matcher);
        badge.setAttribute("data-tooltip", `Matcher: ${matcher}`);
        out.push(badge);
      }
      break;
    }
    default:
      break;
  }
  return out;
}

/** The secondary line: a description, or a hook's action preview. */
function subtitleFor(doc: KiroDoc): string {
  if (doc.category === "hook") {
    return doc.action ?? "";
  }
  return doc.description ?? "";
}

/** Split a document path into (repo, repo-relative path) for the git lookup.
 *
 *  Server paths look like `<workdir>/<repo>/.kiro/...` or `<workdir>/.kiro/...`,
 *  and git status reports a repo NAME plus a repo-relative path. The first
 *  segment after the work directory is the repo — for the workspace-root tree
 *  that is `.kiro` itself, which is its own repo. */
function splitRepoPath(path: string): { repo: string; rel: string } {
  const idx = path.indexOf("/.kiro");
  if (idx < 0) {
    return { repo: "", rel: "" };
  }
  // Everything before /.kiro, last segment = the containing directory.
  const before = path.slice(0, idx);
  const afterKiro = path.slice(idx + 1); // ".kiro/..."
  const parent = before.slice(before.lastIndexOf("/") + 1);
  // A per-repo tree: repo is the directory holding .kiro, and .kiro is part of
  // the repo-relative path. The workspace-root tree has no such directory
  // inside the workspace, so .kiro is itself the repo.
  if (parent === "" || before.endsWith("/workspace") || !before.includes("/")) {
    return { repo: ".kiro", rel: afterKiro.slice(".kiro/".length) };
  }
  return { repo: parent, rel: afterKiro };
}

/** What a row may do. THREE provenance gates, resolved into one answer here so
 *  they cannot disagree at the two places that render.
 *
 *  They are three questions, not three levels of one:
 *
 *  - `reachable` — can the file surface reach this path at all? A GLOBAL hook's
 *    file lives under the container HOME, which `internal/filebrowse` deny-lists
 *    as a sensitive path (the whole `/config/home` tree is blocked, and its
 *    `~`-prefixed display path would not even resolve first). Nothing there is
 *    openable, editable or deletable.
 *  - `read_only` — the file is not WRITABLE. Reading it is still legitimate, so
 *    this withholds the controls and keeps the activation surface.
 *  - `delete_protected` — the row is a symlink, so deleting it would unlink the
 *    file it points at, which is listed under its own name on this same page.
 *    Editing through a link is what following one means, so the pencil stays.
 *
 *  THE RESOLUTION THAT MATTERS is `openable`. The first two gates used to touch
 *  only the control slot, while `docRow` built the activation surface
 *  unconditionally — so a row could hide its pencil and its delete and then open
 *  an editable file on click, which is the same incoherence the read_only /
 *  delete_protected split was made to end. An unreachable row's surface is INERT:
 *  no role, no tabindex, no listeners. Not a disabled button, because a button
 *  that cannot act still announces itself as one.
 *
 *  The enable toggle is orthogonal to all three and survives every one of them: it
 *  goes through POST /api/hooks/{id}/enabled and KAS writes the file, so it never
 *  touches vibekit's file surface. A global hook is exactly the row that proves
 *  this — untouchable through the editor, and still switchable. */
interface RowGates {
  openable: boolean;
  editable: boolean;
  deletable: boolean;
  /** Say why the delete is withheld. Only for the symlink case: an unreachable
   *  row's own scope badge already carries its reason, and a second explanation
   *  beside it would state one fact twice. */
  explainDelete: boolean;
}

function rowGates(doc: KiroDoc, hook: HookState | undefined): RowGates {
  if (hook !== undefined && isGlobalHook(hook)) {
    return { openable: false, editable: false, deletable: false, explainDelete: false };
  }
  if (doc.read_only === true) {
    // Neither control, and no claim either. The badge that used to sit here said
    // "read-only" while the activation surface opened a file the editor could
    // save; a row states what it can back up or it states nothing.
    return { openable: true, editable: false, deletable: false, explainDelete: false };
  }
  if (doc.delete_protected === true) {
    return { openable: true, editable: true, deletable: false, explainDelete: true };
  }
  return { openable: true, editable: true, deletable: true, explainDelete: false };
}

/** Delete the document behind a row, after a confirm.
 *
 *  The confirm is not ceremony: there is no undo path for a `.kiro` file — no
 *  trash, no snapshot — so the dialog IS the guard. */
async function deleteRow(doc: KiroDoc): Promise<void> {
  const ok = await confirmDialog(
    `Delete ${doc.name}? This cannot be undone.`,
    "Delete",
    "destructive",
  );
  if (!ok) {
    return;
  }
  const res = await deleteDoc.dispatch({ path: doc.path, name: doc.name });
  if (res === null) {
    return; // the action reported it; the row stays until the refetch says otherwise
  }
  loadDocs();
}

/** The row's trailing control slot.
 *
 *  The whole row used to BE the button, with a decorative pencil inside it. A
 *  delete control cannot live in that shape: it would nest an interactive
 *  element inside a `<button>`, which is invalid HTML and gets flattened by
 *  assistive tech — the same defect `pill-expand.ts` documents. So the
 *  activation surface and the controls are SIBLINGS, which is the shape the pill
 *  work already established here. */
function rowControls(doc: KiroDoc, hook: HookState | undefined, gates: RowGates): HTMLElement {
  const slot = el("span", { className: "docs-row-controls" });
  // The toggle comes FIRST in the DOM and reads last in the row, and it is added
  // before every gate below because none of them govern it.
  if (hook !== undefined) {
    slot.appendChild(hookToggle(hook));
  }
  if (gates.editable) {
    slot.appendChild(
      // Decorative: the activation surface beside it is the open control.
      el("span", { className: "list-row-btn", "aria-hidden": "true" }, iconEl(ICON_EDIT)),
    );
  }
  if (gates.explainDelete) {
    // Editable, so the pencil above stays. The delete is withheld and SAID,
    // because an absent control with no reason reads as a bug: this row is an
    // alias, and deleting it would remove the file it points at — which is listed
    // under its own name on this same page.
    const badge = el("span", { className: "docs-badge docs-badge-link" }, "link");
    badge.setAttribute(
      "data-tooltip",
      "A symlink. Editing it writes the file it points to; deleting it would remove that file, so delete is disabled here",
    );
    slot.appendChild(badge);
    return slot;
  }
  if (!gates.deletable) {
    return slot;
  }
  const del = el("button", {
    type: "button",
    className: "icon-btn docs-row-delete",
    "aria-label": `Delete ${doc.name}`,
  }) as HTMLButtonElement;
  del.setAttribute("data-tooltip", "Delete");
  del.appendChild(iconEl(ICON_TRASH));
  del.addEventListener("click", (e: MouseEvent) => {
    // The activation surface is a sibling, not an ancestor, so this cannot
    // bubble into an open — but the row is a flex container users click on, and
    // stopping here keeps the two controls independent whatever the layout does.
    e.stopPropagation();
    void deleteRow(doc);
  });
  slot.appendChild(del);
  return slot;
}

/** The enable switch. Its checked state is the SERVER's — the row repaints from a
 *  refetch after every write — so there is no optimistic flip to reconcile. */
function hookToggle(h: HookState): HTMLElement {
  const input = el("input", {
    type: "checkbox",
    className: "hook-toggle",
    "aria-label": `${h.enabled ? "Disable" : "Enable"} hook ${h.name}`,
  }) as HTMLInputElement;
  input.checked = h.enabled;
  const label = el(
    "label",
    { className: "toggle toggle-inline" },
    input,
    el("span", { className: "toggle-slider" }),
  );
  // The id rides the wrapper, not the input, because the delegated handler walks
  // up from whatever the event hit.
  label.setAttribute("data-hook-id", h.id);
  return label;
}

function docRow(doc: KiroDoc): HTMLElement {
  const row = el("div", { className: "list-row docs-row", "data-path": doc.path });
  row.setAttribute("data-sig", rowSig(doc));
  row.append(...rowParts(doc));
  return row;
}

/** A row's two children: the activation surface and the control slot. Shared by
 *  the mount and the repaint so the two cannot build different rows. */
function rowParts(doc: KiroDoc): HTMLElement[] {
  const hook = hookFor(doc);
  const gates = rowGates(doc, hook);
  const name = el("span", { className: "list-row-name" }, doc.name);
  const children: HTMLElement[] = [name];

  // Skipped for an unreachable row: a global hook's `~/...` display path is not in
  // any repo this poll walks, and splitRepoPath would resolve it to a plausible
  // repo name and look up a file that does not exist there.
  const { repo, rel } = gates.openable ? splitRepoPath(doc.path) : { repo: "", rel: "" };
  const letter = repo === "" ? "" : statusFor(repo, rel);
  if (letter !== "") {
    const badge = el("span", { className: "docs-git-letter" }, letter);
    badge.setAttribute("data-tooltip", describeStatus(letter));
    badge.setAttribute("aria-label", `Git status: ${describeStatus(letter)}`);
    children.push(badge);
  }

  const meta = el("span", { className: "list-row-meta docs-row-meta" }, ...metaFor(doc));
  if (hook !== undefined) {
    for (const badge of hookBadges(hook)) {
      meta.appendChild(badge);
    }
  }

  // The badges are their own line UNDER the title, not the tail of the title
  // line. On the title line they competed with the name for the same row of
  // pixels and had to be pushed to the far edge to stay legible, which is what
  // made them read as floating; a line of their own puts them where a reader
  // scans DOWN a column of pills instead of across.
  //
  // Appended only when a document HAS one — an empty span still consumes the
  // surface's row gap, and the Skills tab is mostly documents with no inclusion
  // mode declared, so it would show a blank line after every title.
  const sub = subtitleFor(doc);
  const surfaceChildren: HTMLElement[] = [el("div", { className: "docs-row-top" }, ...children)];
  if (meta.children.length > 0) {
    surfaceChildren.push(meta);
  }
  if (sub !== "") {
    surfaceChildren.push(el("div", { className: "docs-row-sub" }, sub));
  }
  // The activation surface: everything that identifies the document.
  //
  // INERT when the row is not openable, rather than styled-as-disabled: without
  // the role, the tabindex and the listeners it is a div, so assistive tech does
  // not announce a control and a keyboard user does not land on one. A row whose
  // controls are withheld and whose surface still opened a file was the whole
  // defect the gate resolution above exists to prevent.
  const surface = el("div", { className: "docs-row-surface" }, ...surfaceChildren);
  if (gates.openable) {
    surface.setAttribute("role", "button");
    surface.setAttribute("tabindex", "0");
    surface.setAttribute("aria-label", `Open ${doc.name}`);
    const open = (): void => {
      openFile(doc.path);
    };
    surface.addEventListener("click", open);
    surface.addEventListener("keydown", (e: KeyboardEvent) => {
      if (e.key === "Enter" || e.key === " ") {
        e.preventDefault();
        open();
      }
    });
  }

  return [surface, rowControls(doc, hook, gates)];
}

/** The badges only a joined hook can carry: its scope, and the reason KAS
 *  disabled it.
 *
 *  The Global badge does double duty and that is deliberate — it names the scope
 *  AND carries the file path its row cannot open, so an unreachable row still
 *  tells the reader where the file is. */
function hookBadges(h: HookState): HTMLElement[] {
  const out: HTMLElement[] = [];
  if (isGlobalHook(h)) {
    const badge = el("span", { className: "docs-badge docs-badge-global" }, "global");
    badge.setAttribute(
      "data-tooltip",
      `${h.file_path ?? "~/.kiro/hooks"} — applies in every workspace. Outside the workspace, so it cannot be opened or deleted here`,
    );
    out.push(badge);
  }
  const reason = h.disabled_reason ?? "";
  if (reason !== "") {
    const badge = el("span", { className: "docs-badge docs-badge-disabled" }, "disabled");
    badge.setAttribute("data-tooltip", reason);
    out.push(badge);
  }
  const warn = MATCHER_WARNINGS[h.matcher_warning ?? ""];
  if (warn !== undefined) {
    const badge = el("span", { className: "docs-badge docs-badge-warn" }, warn.label);
    badge.setAttribute("data-tooltip", warn.detail);
    out.push(badge);
  }
  return out;
}

/** The two matcher defects the server reports, and the copy for each.
 *
 *  A LOOKUP rather than a branch on the string, so an unrecognised value renders
 *  NOTHING instead of an empty badge: the field is a server-side enum, and a
 *  vibekit build older than the server that added a third value should stay quiet
 *  rather than paint a blank chip.
 *
 *  Both are warnings and neither is an error, which is why one badge style covers
 *  them. `every tool` is a legitimate choice the reader may have made on purpose —
 *  the badge exists because upstream keeps that finding in its own log, so without
 *  it a hook that fires on every tool call looks identical to one that is scoped.
 *  `no effect` cannot be created through vibekit at all (the create form refuses
 *  it), so a row carrying it is a hand-written or copied-in file. */
const MATCHER_WARNINGS: Record<string, { label: string; detail: string }> = {
  missing_tool_matcher: {
    label: "every tool",
    detail:
      "This hook has no matcher, so it runs on EVERY tool call. Add a matcher to scope it to the tools you meant.",
  },
  ineffective: {
    label: "no effect",
    detail:
      "This trigger has nothing to match against, so its matcher is ignored and the hook fires every time. Remove the matcher, or pick a trigger whose matcher is tested against a tool name or a file path.",
  },
};

/** A skeleton matching the real row shape. Skipped when the panel already holds
 *  rows, so a live refetch never flashes placeholders. */
function showSkeleton(): () => void {
  const container = panelFor(activeTab.peek());
  if (container?.querySelector("[data-reconcile-key]") !== null) {
    // Either no panel, or one that already holds real rows (a live refetch).
    return () => {
      /* nothing shown, nothing to tear down */
    };
  }
  const wrap = el("div", { className: "docs-skeleton", "aria-hidden": "true" });
  for (const w of ["62%", "48%", "70%", "55%"]) {
    const row = el("div", { className: "list-row docs-skel-row" });
    const bar = el("div", { className: "skeleton docs-skel-name" });
    bar.style.width = w;
    row.appendChild(bar);
    wrap.appendChild(row);
  }
  container.replaceChildren(wrap);
  return () => {
    wrap.remove();
  };
}

/** @internal Test seam: inject rows without a fetch. */
export function _setDocsForTest(list: KiroDoc[]): void {
  docs = list;
}

/** @internal Test seam: inject hook state without a fetch, keyed the way the
 *  join keys it. */
export function _setHooksForTest(list: HookState[]): void {
  hooks = new Map(list.map((h) => [hookKey(hookScopeOf(h), h.file_path ?? "", h.name), h]));
}

/** @internal Test seam for the Hooks tab's row set — the one tab that is not a
 *  pure filter of the inventory. */
export function _hookRowsForTest(): KiroDoc[] {
  return hookRows();
}

/** @internal Test seam for the repo/path split — the piece that decides whether
 *  a git letter resolves at all. */
export function _splitRepoPathForTest(path: string): { repo: string; rel: string } {
  return splitRepoPath(path);
}

/** @internal Test seam for one rendered row. */
export function _renderRowForTest(doc: KiroDoc): HTMLElement {
  return docRow(doc);
}

/** @internal Test seam: repaint the active panel from the seeded state, without
 *  a fetch. */
export function _renderActiveForTest(): void {
  renderActive();
}
