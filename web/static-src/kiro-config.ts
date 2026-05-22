// ---------------------------------------------------------------------------
// Kiro config viewer: lists steering docs, skills, and custom agents from
// the workspace .kiro/ directory. Clicking an entry opens it in the editor.
// ---------------------------------------------------------------------------

import { ICON_EDIT } from "./icons.js";
import { openFile } from "./editor-openers.js";
import { apiGet, CancellableSlot } from "./api-client.js";
import { $ } from "./dom.js";

interface KiroConfigItem {
  name: string;
  path: string;
  type: string;
  inclusion?: string;
}

const TYPE_LABELS: Record<string, string> = {
  steering: "Steering docs",
  skill: "Skills",
  agent: "Custom agents",
};

const loadSlot = new CancellableSlot();

export function loadKiroConfig(): void {
  const signal = loadSlot.start();
  const container = $.kiroConfigList;
  void apiGet<{ items: KiroConfigItem[] }>("/api/workspace/kiro-config", signal).then((d) => {
    if (signal.aborted) return;
    if (d === null) {
      const empty = document.createElement("div");
      empty.className = "list-empty";
      empty.textContent = "Failed to load config";
      container.replaceChildren(empty);
      return;
    }
    render(container, d.items);
  });
}

function render(container: HTMLDivElement, items: KiroConfigItem[]): void {
  container.replaceChildren();

  const groups = new Map<string, KiroConfigItem[]>();
  for (const item of items) {
    const g = groups.get(item.type) ?? [];
    g.push(item);
    groups.set(item.type, g);
  }

  for (const [type, group] of groups) {
    const label = document.createElement("div");
    label.className = "list-group-label";
    label.textContent = TYPE_LABELS[type] ?? type;
    container.appendChild(label);
    for (const item of group) container.appendChild(itemRow(item));
  }

  if (container.children.length === 0) {
    const empty = document.createElement("div");
    empty.className = "list-empty";
    empty.textContent = "No .kiro/ configuration found";
    container.replaceChildren(empty);
  }
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
