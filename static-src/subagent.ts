// ---------------------------------------------------------------------------
// Sub-agent UI for the Claude-Code-style `invokeSubAgent` tool. Custom
// agents (user-authored, not kiro-cli native) can define a recursive
// tool that invokes another agent; we render it as an inline card
// whose body aggregates whatever the sub-agent streams back. Clicking
// opens the full transcript in a modal.
//
// kiro-cli's native subagent orchestration flows through a richer
// channel (_kiro.dev/subagent/list_update → crew card); see
// crew-card.ts. This module is the fallback for custom-agent
// conventions that don't emit that notification.
//
// Nested tool calls during an active sub-agent turn are folded into
// the card's preview instead of rendering at top level, keeping the
// transcript stable.
// ---------------------------------------------------------------------------

import type { ToolStatus } from "./types.js";
import { isToolDone } from "./tool-schema.js";
import { humanName } from "./strings.js";
import { scroll } from "./scroll.js";
import { $ } from "./dom.js";
import { openModal } from "./modals.js";

/** Number of trailing sentences shown in the sub-agent preview card. */
const PREVIEW_SENTENCES = 4;

const els = new Map<string, HTMLDivElement>();
const transcripts = new Map<string, string>();
let activeId = "";

export function isSubAgent(title: string): boolean {
  // Claude-Code-style custom-agent convention only. kiro-cli's
  // native subagent orchestration is filtered server-side and
  // rendered through the crew-monitor card instead.
  return title === "invokeSubAgent";
}

/** True iff a sub-agent is currently active. `addToolCall` uses this
 *  to decide whether an incoming tool_call should be folded into the
 *  sub-agent preview or rendered at top level. There is no per-id
 *  tracking: any tool call that fires while `activeId !== ""` is
 *  considered nested, which matches how Claude-Code-style custom
 *  agents fan out (every sub-tool of the sub-agent runs during its
 *  active turn). When the sub-agent completes, activeId clears and
 *  subsequent tool calls render at top level again. */
export function isSubAgentActive(): boolean {
  return activeId !== "";
}

/** Append free-form text to the active sub-agent preview + open modal (if open). */
export function appendToSubAgent(text: string): void {
  if (activeId === "") {
    return;
  }
  const el = els.get(activeId);
  if (el === undefined) {
    return;
  }
  let acc = transcripts.get(activeId) ?? "";
  acc += text;
  transcripts.set(activeId, acc);
  const preview = el.querySelector(".subagent-preview");
  if (preview !== null) {
    preview.textContent = lastSentences(acc, PREVIEW_SENTENCES);
  }
  if (!$.subagentModal.classList.contains("hidden")) {
    const body = $.subagentModalBody;
    if (body.textContent !== acc) {
      body.textContent = acc;
      body.scrollTop = body.scrollHeight;
    }
  }
  scroll();
}

/** Build a spinner element for the sub-agent card header. */
function buildSpinner(): HTMLDivElement {
  const spinner = document.createElement("div");
  spinner.className = "subagent-spinner";
  return spinner;
}

/** Render a sub-agent card into a container (the caller picks placement). */
export function createSubAgentCard(
  id: string,
  status: ToolStatus,
  rawInput: Record<string, unknown> | undefined,
  storedOutput?: string,
): HTMLDivElement {
  const label = labelFromInput(rawInput);
  transcripts.set(id, storedOutput ?? "");
  const active = status === "pending" || status === "in_progress";
  if (active) {
    activeId = id;
  }

  const el = document.createElement("div");
  el.className = "subagent-call";

  const header = document.createElement("div");
  header.className = "subagent-header";

  if (active) {
    header.appendChild(buildSpinner());
  } else {
    const iconSpan = document.createElement("span");
    iconSpan.className = "subagent-icon";
    iconSpan.textContent = "🤖";
    header.appendChild(iconSpan);
  }

  const nameSpan = document.createElement("span");
  nameSpan.className = "subagent-name";
  nameSpan.textContent = label;
  header.appendChild(nameSpan);

  const statusSpan = document.createElement("span");
  statusSpan.className = `tool-status ${status}`;
  statusSpan.textContent = status;
  header.appendChild(statusSpan);

  el.appendChild(header);

  const preview = document.createElement("div");
  preview.className = "subagent-preview";
  if (storedOutput !== undefined && storedOutput !== "") {
    preview.textContent = lastSentences(storedOutput, PREVIEW_SENTENCES);
  }
  el.appendChild(preview);

  el.addEventListener("click", () => {
    openPopup(id, label);
  });
  els.set(id, el);
  return el;
}

/** Update an existing sub-agent card (status + appended output). */
export function updateSubAgentCard(
  id: string,
  status: ToolStatus | undefined,
  output: string | undefined,
): boolean {
  const el = els.get(id);
  if (el === undefined) {
    return false;
  }
  if (status !== undefined) {
    const s = el.querySelector(".tool-status");
    if (s !== null) {
      s.textContent = status;
      s.className = `tool-status ${status}`;
    }
    const done = isToolDone(status);
    if (done && activeId === id) {
      activeId = "";
    }
    if (done) {
      const spinner = el.querySelector(".subagent-header .subagent-spinner");
      if (spinner !== null) {
        const icon = document.createElement("span");
        icon.className = "subagent-icon";
        icon.textContent = "🤖";
        spinner.replaceWith(icon);
      }
    }
  }
  if (output !== undefined) {
    let acc = transcripts.get(id) ?? "";
    acc += (acc !== "" ? "\n" : "") + output;
    transcripts.set(id, acc);
    const preview = el.querySelector(".subagent-preview");
    if (preview !== null) {
      preview.textContent = lastSentences(acc, PREVIEW_SENTENCES);
    }
  }
  if (!$.subagentModal.classList.contains("hidden")) {
    const body = $.subagentModalBody;
    const cur = transcripts.get(id) ?? "";
    if (body.textContent !== cur) {
      body.textContent = cur;
      body.scrollTop = body.scrollHeight;
    }
  }
  scroll();
  return true;
}

export function resetSubAgents(): void {
  $.subagentModal.classList.add("hidden");
  els.clear();
  transcripts.clear();
  activeId = "";
}

// --- internal helpers ---

function labelFromInput(rawInput: Record<string, unknown> | undefined): string {
  return humanName((rawInput?.["name"] as string | undefined) ?? "subagent");
}

function lastSentences(text: string, n: number): string {
  return text
    .split(/(?<=[.!?\n])\s+/)
    .filter((s) => s.trim() !== "")
    .slice(-n)
    .join(" ");
}

function openPopup(id: string, label: string): void {
  $.subagentModalTitle.textContent = label;
  $.subagentModalBody.textContent = transcripts.get(id) ?? "";
  openModal($.subagentModal);
}
