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
import { createDisclosure } from "@cplieger/ui-primitives/disclosure";
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
  // The chevron is purely decorative now: the HEADER is the disclosure
  // trigger (it carries aria-expanded + activation), so a nested focusable
  // button would be a redundant tab stop announcing a second control.
  const chevron = el("button", {
    className: "subagent-toggle",
    type: "button",
    "aria-hidden": "true",
    tabindex: "-1",
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

  // The disclosure primitive owns the header wiring (click, Enter/Space,
  // aria-expanded/aria-controls) and the body collapse (animated height,
  // aria-hidden + inert). vibekit keeps the root .collapsed skin class (the
  // chevron arrow keys off it) and the user-took-over latch, both via
  // onToggle's source.
  let userToggled = false;
  const ctl = createDisclosure(header, body, {
    open: true,
    onToggle: (open, source) => {
      root.classList.toggle("collapsed", !open);
      if (source === "user") {
        userToggled = true;
      }
    },
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
        ctl.close();
      }
    },
    setName(n: string): void {
      nameEl.textContent = n;
    },
  };
}
