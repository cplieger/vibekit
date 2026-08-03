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

import { apiGet } from "./api-client.js";
import { defineAction, ActionError, retryNetwork, registerCleanup } from "./actions/index.js";
import { onSSE } from "./bus.js";
import { $, maybeViewTransition } from "./dom.js";
import { el } from "@cplieger/reactive";
import { iconEl } from "./icon-el.js";
import { ICON_EDIT } from "./icons.js";
import { initGitStatusStore, onGitStatusChange, statusFor } from "./git-status-store.js";
import { describeStatus } from "./git-types.js";
import { openFile } from "./editor-openers.js";
import { reconcile } from "./reconcile.js";
import { rovingFocus } from "@cplieger/ui-primitives/roving-focus";
import { signal, subscribe } from "@cplieger/reactive";
import { skeletonTiming } from "@cplieger/ui-primitives/skeleton";
import { pushRoute } from "./router.js";
import type { DocsTab } from "./router.js";
import { renderRecipesPanel } from "./recipes.js";
import { setDocsTab as setTabRoute, toggleDocsView } from "./tabs.js";

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
let inited = false;

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
 *  and the router both come through here. */
export function showDocsView(tab: DocsTab = "steering"): void {
  toggleDocsView(tab, () => {
    forceDocsTab(tab);
    initDocsView();
    loadDocs();
  });
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
  maybeViewTransition(() => {
    for (const panel of document.querySelectorAll<HTMLDivElement>("[data-docs-panel]")) {
      const panelTab = panel.dataset["docsPanel"] ?? "";
      const isActive = panelTab === tab;
      panel.classList.toggle("hidden", !isActive);
      panel.setAttribute("role", "tabpanel");
      panel.id = `docs-panel-${panelTab}`;
      panel.setAttribute("aria-labelledby", `docs-tab-${panelTab}`);
    }
  });
  const title = document.getElementById("docs-page-title");
  if (title !== null) {
    title.textContent = TAB_LABELS[tab];
  }
}

function panelFor(tab: DocsTab): HTMLDivElement | null {
  return document.querySelector<HTMLDivElement>(`[data-docs-panel="${tab}"]`);
}

// --- Rendering ---

/** A rendered entry: either a group separator or a document row. Specs and
 *  hooks nest under a group; the flat categories emit rows only. */
type Entry = { kind: "group"; label: string } | { kind: "doc"; doc: KiroDoc };

function renderActive(): void {
  const tab = activeTab.peek();
  const container = panelFor(tab);
  if (container === null) {
    return;
  }
  if (tab === "workflows") {
    renderRecipesPanel(container);
    return;
  }
  const rows = docs.filter((d) => d.category === TAB_CATEGORY[tab]);
  if (rows.length === 0) {
    container.replaceChildren(el("div", { className: "list-empty" }, EMPTY_TEXT[tab]));
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
  });
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

function docRow(doc: KiroDoc): HTMLElement {
  const name = el("span", { className: "list-row-name" }, doc.name);
  const children: HTMLElement[] = [name];

  const { repo, rel } = splitRepoPath(doc.path);
  const letter = repo === "" ? "" : statusFor(repo, rel);
  if (letter !== "") {
    const badge = el("span", { className: "docs-git-letter" }, letter);
    badge.setAttribute("data-tooltip", describeStatus(letter));
    badge.setAttribute("aria-label", `Git status: ${describeStatus(letter)}`);
    children.push(badge);
  }

  const meta = el("span", { className: "list-row-meta docs-row-meta" }, ...metaFor(doc));
  children.push(meta);

  const sub = subtitleFor(doc);
  const row = el(
    "div",
    {
      className: "list-row docs-row",
      role: "button",
      tabindex: "0",
      "aria-label": `Open ${doc.name}`,
      "data-path": doc.path,
    },
    el("div", { className: "docs-row-top" }, ...children),
    // Decorative: the whole row is the button.
    el("span", { className: "list-row-btn", "aria-hidden": "true" }, iconEl(ICON_EDIT)),
  );
  if (sub !== "") {
    row.insertBefore(
      el("div", { className: "docs-row-sub" }, sub),
      row.querySelector(".list-row-btn"),
    );
  }

  const open = (): void => {
    openFile(doc.path);
  };
  row.addEventListener("click", open);
  row.addEventListener("keydown", (e: KeyboardEvent) => {
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      open();
    }
  });
  return row;
}

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

/** @internal Test seam for the repo/path split — the piece that decides whether
 *  a git letter resolves at all. */
export function _splitRepoPathForTest(path: string): { repo: string; rel: string } {
  return splitRepoPath(path);
}

/** @internal Test seam for one rendered row. */
export function _renderRowForTest(doc: KiroDoc): HTMLElement {
  return docRow(doc);
}
