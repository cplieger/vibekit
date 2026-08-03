// ---------------------------------------------------------------------------
// Fundamental: SubagentBlock — a collapsible container for a subagent's work.
//
// A subagent's blocks (text / thinking / tool_use) carry its agent_subtask_id.
// The composition groups them and renders them into THIS block's `.body` using
// the SAME block dispatcher as the main transcript — so a subagent shows real
// tool cards, diffs, reasoning, and nested subagents, not a text preview.
//
// Pure view: header (spinner/agent-glyph + name + status + collapse) + a
// body the caller fills. Mirrors the IDE, which shows a subagent as a
// collapsible section that stays open while active and collapses when it
// completes. The glyph defaults to the shared agent hexagon; the caller
// swaps in a per-known-subagent icon via setIcon (roles.ts iconForSubagent),
// so pre-built subagents (introspect, context-gatherer, …) get distinct
// icons like the mode picker's builtins do.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { createDisclosure } from "@cplieger/ui-primitives/disclosure";
import type { ToolStatus } from "../types.js";
import { isToolActive } from "../tool-schema.js";
import { iconEl } from "../icon-el.js";
import { ICON_TAB_AGENT } from "../icons.js";

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
  /** Swap the identity glyph (SVG string; roles.ts iconForSubagent). The
   *  spinner still owns the slot while the subagent is active. */
  setIcon(svg: string): void;
}

/** Build a subagent block. Open while active; auto-collapses once settled
 *  (unless the user has toggled it). */
export function buildSubagentBlock(name: string, status: ToolStatus): SubagentView {
  const root = el("div", { className: "subagent-block" }) as HTMLDivElement;

  const icon = el("span", { className: "subagent-icon" });
  const nameEl = el("span", { className: "subagent-name" }, name);
  // No status WORD here either — same vocabulary as a tool card: the icon
  // carries the outcome by tint plus a composited check/cross, and the header's
  // accessible name carries the word for a screen reader. This block was the
  // fourth writer of that vocabulary and it moves with the other three; leaving
  // it behind would have split the vocabulary down the middle of one transcript.
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

  let iconSvg = ICON_TAB_AGENT;
  let lastStatus = status;
  const applyIcon = (s: ToolStatus): void => {
    const failed = s === "failed";
    icon.classList.toggle("is-fail", failed);
    icon.classList.toggle("is-ok", !failed && !isToolActive(s));
    icon.classList.toggle("is-running", isToolActive(s));
    if (isToolActive(s)) {
      icon.classList.add("subagent-spinner");
      icon.replaceChildren();
    } else {
      icon.classList.remove("subagent-spinner");
      icon.replaceChildren(
        iconEl(iconSvg),
        el(
          "span",
          { className: "tool-outcome-badge", "aria-hidden": "true" },
          failed ? "\u2717" : "\u2713",
        ),
      );
    }
    header.setAttribute(
      "aria-label",
      `${nameEl.textContent}, ${failed ? "failed" : isToolActive(s) ? "running" : "succeeded"}`,
    );
  };
  applyIcon(status);

  return {
    root,
    body,
    setStatus(s: ToolStatus): void {
      lastStatus = s;
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
    setIcon(svg: string): void {
      if (svg === iconSvg) {
        return;
      }
      iconSvg = svg;
      applyIcon(lastStatus);
    },
  };
}
