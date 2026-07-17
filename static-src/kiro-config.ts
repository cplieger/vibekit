// ---------------------------------------------------------------------------
// Kiro config viewer: lists steering docs, skills, and custom agents from
// the workspace .kiro/ directory. Clicking (or Enter/Space on) an entry opens
// it in the editor.
//
// Rendered into the Settings → Instructions tab. The list is server-canonical
// (GET /api/workspace/kiro-config); it refetches live when the server signals a
// settings change (settings_updated SSE) so an edit — via the editor or the
// agent — reflects without a close+reopen. initKiroConfig() wires that (called
// once from settings.ts initUI); loadKiroConfig() is the on-open fetch.
// ---------------------------------------------------------------------------

import { ICON_EDIT } from "./icons.js";
import { iconEl } from "./icon-el.js";
import { openFile } from "./editor-openers.js";
import { defineAction, ActionError, retryNetwork, registerCleanup } from "./actions/index.js";
import { onSSE } from "./bus.js";
import { apiGet } from "./api-client.js";
import { $ } from "./dom.js";
import { reconcile } from "./reconcile.js";
import { skeletonTiming } from "@cplieger/ui-primitives/skeleton";
import { el } from "@cplieger/reactive";

interface KiroConfigItem {
  name: string;
  path: string;
  type: string;
  inclusion?: string;
}

type ConfigEntry = { kind: "label"; type: string } | { kind: "item"; item: KiroConfigItem };

const TYPE_LABELS: Record<string, string> = {
  steering: "Steering docs",
  skill: "Skills",
  agent: "Custom agents",
};

const loadKiroConfigAction = defineAction<undefined, { items: KiroConfigItem[] }>({
  name: "settings.load_kiro_config",
  retryable: retryNetwork,
  retry: { count: 2, delay: 300 },
  run: async (_args, signal) => {
    const data = await apiGet<{ items: KiroConfigItem[] }>("/api/workspace/kiro-config", signal);
    if (signal.aborted) {
      throw new DOMException("aborted", "AbortError");
    }
    if (!data) {
      throw new ActionError("Failed to load config", { code: "network" });
    }
    return data;
  },
  error: false,
});

export function loadKiroConfig(): void {
  loadKiroConfigAction.cancel();
  // 150ms show-delay so a fast load never flashes the skeleton; the skeleton
  // teardown is cancelled once the fetch settles (success or error).
  const skeleton = skeletonTiming(() => showSkeleton($.kiroConfigList));
  void loadKiroConfigAction.dispatch(undefined, {
    onSuccess: (d) => {
      skeleton.cancel();
      render($.kiroConfigList, d.items);
    },
    onError: () => {
      skeleton.cancel();
      $.kiroConfigList.replaceChildren(
        el("div", { className: "list-empty" }, "Failed to load config"),
      );
    },
  });
}

/** Wire the live-refresh subscription. Called once at settings init. When the
 *  server broadcasts a settings change, refetch the list — but only while it's
 *  visible (the Instructions tab is open), so a hidden panel doesn't fetch. */
export function initKiroConfig(): void {
  registerCleanup(
    onSSE("settings_updated", () => {
      // offsetParent is null when any ancestor is display:none (settings closed
      // or a different tab active), so this fetches only when on-screen.
      if ($.kiroConfigList.offsetParent !== null) {
        loadKiroConfig();
      }
    }),
  );
}

/** Render a stable skeleton matching the final row dimensions. Returns a
 *  teardown that removes it. Skipped when the list already holds real rows
 *  (a live refetch) so an in-place refresh never flashes placeholders. */
function showSkeleton(container: HTMLDivElement): () => void {
  if (container.querySelector("[data-reconcile-key]") !== null) {
    return () => {
      /* already populated — nothing shown, nothing to tear down */
    };
  }
  const skel = buildSkeleton();
  container.replaceChildren(skel);
  return () => {
    skel.remove();
  };
}

function buildSkeleton(): HTMLElement {
  const wrap = el("div", { className: "kiro-config-skeleton", "aria-hidden": "true" });
  // A label + a few rows, mirroring one group of the real list.
  wrap.appendChild(skelBar("kiro-config-skel-label", "6rem"));
  for (const w of ["70%", "55%", "62%"]) {
    const row = el("div", { className: "list-row kiro-config-skel-row" });
    row.appendChild(skelBar("kiro-config-skel-name", w));
    wrap.appendChild(row);
  }
  return wrap;
}

function skelBar(className: string, width: string): HTMLElement {
  const bar = el("div", { className: `skeleton ${className}` });
  bar.style.width = width;
  return bar;
}

function render(container: HTMLDivElement, items: KiroConfigItem[]): void {
  // Flatten groups + items into a single keyed sequence so reconcile
  // can patch in place without rebuilding the whole list on every refresh.
  const groups = new Map<string, KiroConfigItem[]>();
  for (const item of items) {
    const g = groups.get(item.type) ?? [];
    g.push(item);
    groups.set(item.type, g);
  }

  const flat: ConfigEntry[] = [];
  for (const [type, group] of groups) {
    flat.push({ kind: "label", type });
    for (const item of group) {
      flat.push({ kind: "item", item });
    }
  }

  // Drop any prior non-keyed placeholder (skeleton / empty state) before reconcile.
  for (const child of [...container.children]) {
    if ((child as HTMLElement).getAttribute("data-reconcile-key") === null) {
      child.remove();
    }
  }

  if (flat.length === 0) {
    container.replaceChildren();
    container.appendChild(el("div", { className: "list-empty" }, "No .kiro/ configuration found"));
    return;
  }

  reconcile(container, flat, {
    key: (e: ConfigEntry) => (e.kind === "label" ? `label:${e.type}` : `item:${e.item.path}`),
    mount: (e: ConfigEntry) => (e.kind === "label" ? labelRow(e.type) : itemRow(e.item)),
  });
}

function labelRow(type: string): HTMLElement {
  return el("div", { className: "list-group-label" }, TYPE_LABELS[type] ?? type);
}

function itemRow(item: KiroConfigItem): HTMLElement {
  const name = el("span", { className: "list-row-name" }, item.name);
  const meta = el("span", { className: "list-row-meta" }, item.inclusion ?? "");
  // Decorative — the whole row is the button, so hide the icon from AT.
  const edit = el("span", { className: "list-row-btn", "aria-hidden": "true" }, iconEl(ICON_EDIT));

  // The row itself is the activation target (open the file). Make it a real
  // button semantically: role + tabindex + Enter/Space, so keyboard users get
  // the same affordance as a mouse click, with a visible focus ring (CSS).
  const row = el(
    "div",
    {
      className: "list-row kiro-config-row",
      role: "button",
      tabindex: "0",
      "aria-label": `Open ${item.name}`,
      "data-path": item.path,
    },
    name,
    meta,
    edit,
  );
  const open = (): void => {
    openFile(item.path);
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
