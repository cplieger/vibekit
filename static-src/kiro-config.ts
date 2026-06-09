// ---------------------------------------------------------------------------
// Kiro config viewer: lists steering docs, skills, and custom agents from
// the workspace .kiro/ directory. Clicking an entry opens it in the editor.
// ---------------------------------------------------------------------------

import { ICON_EDIT } from "./icons.js";
import { openFile } from "./editor-openers.js";
import { defineAction, ActionError, retryNetwork } from "./actions/index.js";
import { apiGet } from "./api-client.js";
import { $ } from "./dom.js";
import { reconcile } from "./reconcile.js";
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
  void loadKiroConfigAction.dispatch(undefined, {
    onSuccess: (d) => {
      render($.kiroConfigList, d.items);
    },
    onError: () => {
      $.kiroConfigList.replaceChildren(
        el("div", { className: "list-empty" }, "Failed to load config"),
      );
    },
  });
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

  // Drop any prior non-keyed empty-state placeholder before reconcile.
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

  const edit = el("span", { className: "list-row-btn" });
  edit.innerHTML = ICON_EDIT;

  const row = el("div", { className: "list-row" }, name, meta, edit);
  row.style.cursor = "pointer";
  row.addEventListener("click", () => {
    openFile(item.path);
  });
  return row;
}
