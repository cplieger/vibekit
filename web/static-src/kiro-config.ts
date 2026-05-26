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

interface KiroConfigItem {
  name: string;
  path: string;
  type: string;
  inclusion?: string;
}

type ConfigEntry =
  | { kind: "label"; type: string }
  | { kind: "item"; item: KiroConfigItem };

const TYPE_LABELS: Record<string, string> = {
  steering: "Steering docs",
  skill: "Skills",
  agent: "Custom agents",
};

const loadKiroConfigAction = defineAction<void, { items: KiroConfigItem[] }>({
  name: "settings.load_kiro_config",
  retryable: retryNetwork,
  retry: { count: 2, delay: 300 },
  run: async (_args, signal) => {
    const data = await apiGet<{ items: KiroConfigItem[] }>("/api/workspace/kiro-config", signal);
    if (signal.aborted) throw new DOMException("aborted", "AbortError");
    if (!data) throw new ActionError("Failed to load config", { code: "network" });
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
      const empty = document.createElement("div");
      empty.className = "list-empty";
      empty.textContent = "Failed to load config";
      $.kiroConfigList.replaceChildren(empty);
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
    for (const item of group) flat.push({ kind: "item", item });
  }

  // Drop any prior non-keyed empty-state placeholder before reconcile.
  for (const child of [...container.children]) {
    if ((child as HTMLElement).getAttribute("data-reconcile-key") === null) child.remove();
  }

  if (flat.length === 0) {
    container.replaceChildren();
    const empty = document.createElement("div");
    empty.className = "list-empty";
    empty.textContent = "No .kiro/ configuration found";
    container.appendChild(empty);
    return;
  }

  reconcile(container, flat, {
    key: (e: ConfigEntry) => e.kind === "label" ? `label:${e.type}` : `item:${e.item.path}`,
    mount: (e: ConfigEntry) => e.kind === "label" ? labelRow(e.type) : itemRow(e.item),
  });
}

function labelRow(type: string): HTMLDivElement {
  const label = document.createElement("div");
  label.className = "list-group-label";
  label.textContent = TYPE_LABELS[type] ?? type;
  return label;
}

function itemRow(item: KiroConfigItem): HTMLDivElement {
  const row = document.createElement("div");
  row.className = "list-row";
  row.style.cursor = "pointer";

  const name = document.createElement("span");
  name.className = "list-row-name";
  name.textContent = item.name;

  const meta = document.createElement("span");
  meta.className = "list-row-meta";
  meta.textContent = item.inclusion ?? "";

  const edit = document.createElement("span");
  edit.className = "list-row-btn";
  edit.innerHTML = ICON_EDIT;

  row.append(name, meta, edit);
  row.addEventListener("click", () => openFile(item.path));
  return row;
}
