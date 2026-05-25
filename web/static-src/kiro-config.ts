// ---------------------------------------------------------------------------
// Kiro config viewer: lists steering docs, skills, and custom agents from
// the workspace .kiro/ directory. Clicking an entry opens it in the editor.
// ---------------------------------------------------------------------------

import { ICON_EDIT } from "./icons.js";
import { openFile } from "./editor-openers.js";
import { apiAction } from "./actions/index.js";
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

const loadKiroConfigAction = apiAction<void, { items: KiroConfigItem[] }>({
  name: "settings.load_kiro_config",
  dedupe: true,
  retryable: "network",
  retry: { count: 2, delay: 300 },
  request: () => ({ method: "GET", path: "/api/workspace/kiro-config" }),
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
