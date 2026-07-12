// ---------------------------------------------------------------------------
// Fundamental: SubagentBlock — a collapsible container for a subagent's work.
//
// A subagent's blocks (text / thinking / tool_use) carry its agent_subtask_id.
// The composition groups them and renders them into THIS block's `.body` using
// the SAME block dispatcher as the main transcript — so a subagent shows real
// tool cards, diffs, reasoning, and nested subagents, not a text preview.
//
// Pure view: header (spinner/🤖 + name + status + collapse) + a body the
// caller fills. Mirrors the IDE, which shows a subagent as a collapsible
// section that stays open while active and collapses when it completes.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import type { ToolStatus } from "../types.js";
import { isToolActive } from "../tool-schema.js";

/** A mounted subagent block plus its imperative handle. */
export interface SubagentView {
  /** The `.subagent-block` root to insert into the DOM. */
  readonly root: HTMLDivElement;
  /** The container the composition renders the subagent's child blocks into. */
  readonly body: HTMLElement;
  /** Update the header status chip + swap spinner↔icon; auto-collapse on done. */
  setStatus(status: ToolStatus): void;
  /** Update the subagent's display name. */
  setName(name: string): void;
}

/** Build a subagent block. Open while active; auto-collapses once settled
 *  (unless the user has toggled it). */
export function buildSubagentBlock(name: string, status: ToolStatus): SubagentView {
  const root = el("div", { className: "subagent-block" }) as HTMLDivElement;

  const icon = el("span", { className: "subagent-icon" });
  const nameEl = el("span", { className: "subagent-name" }, name);
  const statusEl = el("span", { className: `tool-status ${status}` }, status);
  const chevron = el("button", {
    className: "subagent-toggle",
    type: "button",
    "aria-label": "Toggle subagent details",
    "aria-expanded": "true",
  });
  const header = el(
    "div",
    { className: "subagent-header", role: "button", tabindex: "0" },
    icon,
    nameEl,
    statusEl,
    chevron,
  ) as HTMLDivElement;
  const body = el("div", { className: "subagent-body" });
  root.append(header, body);

  let userToggled = false;
  const setCollapsed = (collapsed: boolean): void => {
    root.classList.toggle("collapsed", collapsed);
    chevron.setAttribute("aria-expanded", collapsed ? "false" : "true");
  };
  const toggle = (): void => {
    userToggled = true;
    setCollapsed(!root.classList.contains("collapsed"));
  };
  header.addEventListener("click", toggle);
  header.addEventListener("keydown", (e: KeyboardEvent) => {
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      toggle();
    }
  });

  const applyIcon = (s: ToolStatus): void => {
    if (isToolActive(s)) {
      icon.classList.add("subagent-spinner");
      icon.textContent = "";
    } else {
      icon.classList.remove("subagent-spinner");
      icon.textContent = "\u{1F916}"; // 🤖
    }
  };
  applyIcon(status);

  return {
    root,
    body,
    setStatus(s: ToolStatus): void {
      statusEl.textContent = s;
      statusEl.className = `tool-status ${s}`;
      applyIcon(s);
      // Auto-collapse a settled subagent so a long nested transcript doesn't
      // dominate the view — unless the user has taken control of the toggle.
      if (!userToggled && !isToolActive(s)) {
        setCollapsed(true);
      }
    },
    setName(n: string): void {
      nameEl.textContent = n;
    },
  };
}
