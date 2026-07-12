// ---------------------------------------------------------------------------
// Mode / role picker: an expandable pill in the prompt bar that shows the
// active chat's current mode and lets the user switch to any other — a
// bundled workflow mode (Default / Spec / Quick Spec / Bug Fix / Plan /
// Autonomous) or a workspace custom agent from .kiro/agents/.
//
// On kiro-cli v3 (KAS) every role is a *mode* in the session's
// availableModes, switched in place via session/set_mode — no new chat, no
// teardown, even on a chat with history. Selecting a mode dispatches the
// chat.set_mode action for the active chat; the server switches the live
// session (or, for an as-yet-unstarted chat, persists the choice and applies
// it at session/new). The pill's icon + label always reflect the active
// chat's current_mode_id.
//
// The list is grouped: bundled modes first, workspace custom agents below
// (the wire tags each with _meta.kiro.source). Before a chat has a live
// session — so availableModes is still empty — the list is seeded from the
// static bundled catalog plus the workspace agents reported by
// /api/workspace/kiro-config, so a mode can be picked before the first prompt.
// ---------------------------------------------------------------------------

import { $, byId } from "./dom.js";
import { el, effect } from "@cplieger/reactive";
import { iconEl } from "./icon-el.js";
import { makeExpandable, collapseAll } from "./pill-expand.js";
import { activeSession, getActive } from "./store.js";
import { createSession } from "./chat.js";
import { setTabIcon } from "./tabs.js";
import { setMode } from "./actions/chat.js";
import { apiGet } from "./api-client.js";
import type { SessionMode } from "./types.js";
import { BUILTIN_MODES, iconForMode, labelForMode, normalizeModeID } from "./roles.js";

// Cache of workspace custom-agent names; refreshed on each expand so the
// list renders instantly from cache, then folds in any changes. Only used
// to seed the list before a live session reports availableModes.
let customAgents: string[] = [];

/** Wire the prompt-bar mode pill. Call once at startup. */
export function initRolePicker(): void {
  const pill = $.rolePill;
  const list = $.roleList;
  makeExpandable(pill, list, {
    onExpand: () => {
      renderList(list);
    },
  });
  // Keep the pill's icon + label in sync with the active chat's mode.
  effect(() => {
    const s = activeSession.value;
    refreshPill(s?.current_mode_id ?? "", s?.available_modes ?? []);
  });
}

function refreshPill(modeID: string, modes: readonly SessionMode[]): void {
  byId<HTMLElement>("role-pill-icon").replaceChildren(iconEl(iconForMode(normalizeModeID(modeID))));
  byId<HTMLSpanElement>("role-pill-label").textContent = labelForMode(modeID, modes);
}

/** The modes to offer: the live session's availableModes when present
 *  (authoritative — carries bundled modes AND workspace agents), else the
 *  bundled catalog seeded with workspace agents from kiro-config. */
function currentModes(): SessionMode[] {
  const live = getActive()?.available_modes ?? [];
  if (live.length > 0) {
    return [...live];
  }
  const custom: SessionMode[] = customAgents.map((name) => ({
    id: name,
    name,
    description: "Custom agent from your workspace .kiro/agents/ folder.",
    source: "workspace",
  }));
  return [...BUILTIN_MODES, ...custom];
}

function renderList(list: HTMLElement): void {
  renderOptions(list);
  // Refresh the workspace-agent seed in the background; only matters before a
  // live session exists (once it does, availableModes already includes them).
  void apiGet<{ items: { name: string; type: string }[] }>("/api/workspace/kiro-config").then(
    (data) => {
      const next = (data?.items ?? []).filter((i) => i.type === "agent").map((i) => i.name);
      if (next.join("\u0000") !== customAgents.join("\u0000")) {
        customAgents = next;
        if (list.classList.contains("pill-expand-visible")) {
          renderOptions(list);
        }
      }
    },
  );
}

function renderOptions(list: HTMLElement): void {
  list.setAttribute("role", "listbox");
  list.setAttribute("aria-label", "Chat mode");
  const currentMode = normalizeModeID(getActive()?.current_mode_id ?? "");
  const modes = currentModes();
  // Group is "bundled" vs everything else. The server reports source as
  // bundled | global | workspace; both global and workspace custom agents
  // belong under the "Custom agents" divider, so only "bundled" (and any
  // unset source, which the built-in catalog uses) stays in the top group.
  const isCustomAgent = (m: SessionMode): boolean =>
    m.source === "workspace" || m.source === "global";
  const bundled = modes.filter((m) => !isCustomAgent(m));
  const workspace = modes.filter(isCustomAgent);

  const items: HTMLElement[] = [
    el("span", { className: "pill-role-hint" }, "Switch mode for this chat"),
  ];
  for (const m of bundled) {
    items.push(modeOption(m, currentMode));
  }
  if (workspace.length > 0) {
    items.push(el("span", { className: "pill-role-hint" }, "Custom agents"));
    for (const m of workspace) {
      items.push(modeOption(m, currentMode));
    }
  }
  list.replaceChildren(...items);
}

function modeOption(mode: SessionMode, currentMode: string): HTMLButtonElement {
  const isCurrent = mode.id === currentMode;
  const opt = el(
    "button",
    {
      type: "button",
      className: isCurrent ? "pill-role-item active" : "pill-role-item",
      role: "option",
      "aria-selected": isCurrent ? "true" : "false",
      "data-tooltip": mode.description ?? "",
    },
    el("span", { className: "pill-role-item-icon" }, iconEl(iconForMode(mode.id))),
    el("span", { className: "pill-role-name" }, mode.name || mode.id),
  ) as HTMLButtonElement;
  opt.addEventListener("click", (e: MouseEvent) => {
    e.stopPropagation();
    collapseAll();
    selectMode(mode.id);
  });
  return opt;
}

function selectMode(modeID: string): void {
  let active = getActive();
  if (active === undefined) {
    // No active chat → create one, then switch it. createSession sets the
    // new chat active synchronously.
    createSession();
    active = getActive();
    if (active === undefined) {
      return;
    }
  }
  const chatID = active.id;
  // Reflect the choice on the tab immediately; the action's optimistic
  // update flips the pill, and the server's mode_changed broadcast confirms.
  setTabIcon(chatID, iconForMode(modeID));
  void setMode.dispatch({ chatID, modeID });
}
