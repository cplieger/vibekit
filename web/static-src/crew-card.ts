// ---------------------------------------------------------------------------
// Crew card: collapsed container per subagent-orchestration group.
//
// Each subagent row shows a status icon (spinner for working, checkmark
// for terminated, warning for error/unknown). The card starts collapsed;
// clicking a row expands it to show the full subagent output (initial
// query + tool calls). An inline message input at the bottom of the
// expanded body lets the user (or the main agent) send instructions to
// that specific subagent without interrupting others.
//
// kiro-cli sends full-state snapshots (not deltas) via
// _kiro.dev/subagent/list_update. The server dedups identical snapshots;
// we apply whatever arrives.
// ---------------------------------------------------------------------------

import type { Crew, CrewSubagent, CrewPendingStage, CrewStatus, ToolCall } from "./types.js";
import { scroll, trimOldMessages } from "./scroll.js";
import { breakToolGroup } from "./tool-group.js";
import { buildToolCard } from "./tool-card.js";
import { isToolActive } from "./tool-schema.js";
import { getActiveId } from "./store.js";
import { sendMessage } from "./actions/crew.js";
import { bindLoadingState } from "./actions/index.js";
import { ICON_SPINNER_14, ICON_CHECK_14, ICON_ERROR_14, ICON_PENDING_14 } from "./icons.js";
import { formatToolActivity } from "./format-tool-activity.js";

const cards = new Map<string, HTMLDivElement>();
const cardState = new WeakMap<HTMLDivElement, string>();
// Per-subagent tool containers, keyed on session_id. Preserved across
// crew-card rebuilds so tool cards aren't destroyed when the snapshot
// updates (which replaces all rows).
const toolContainers = new Map<string, HTMLDivElement>();
// Track which rows are expanded so we can restore state after rebuild.
const expandedRows = new Set<string>();
// Per-tool-call elements inside crew rows, keyed on tool call id.
// Used to propagate status/output updates from the main transcript.
const crewToolEls = new Map<string, HTMLDivElement>();
// Per-subagent activity line elements, keyed on session_id.
// Updated live as tool calls arrive and complete.
const activityEls = new Map<string, HTMLSpanElement>();
// Per-subagent pending-approval state.
const pendingApprovals = new Set<string>();
// Track bindLoadingState unbind functions so they are drained on rebuild.
const loadingUnbinds: Array<() => void> = [];

/** Update the collapsed-row activity line for a subagent. Called when
 *  a tool call arrives, updates, or a permission is requested. */
export function setSubagentActivity(subSessionID: string, text: string): void {
  const el = activityEls.get(subSessionID);
  if (el !== undefined) el.textContent = text;
}

/** Mark a subagent as having a pending permission request. */
export function setSubagentPendingApproval(subSessionID: string, pending: boolean): void {
  if (pending) {
    pendingApprovals.add(subSessionID);
    setSubagentActivity(subSessionID, "\u26a0 tool approval needed");
  } else {
    pendingApprovals.delete(subSessionID);
  }
}

/** Route a tool card into the matching crew-card row's tool container.
 *  Returns true if a matching row was found, false otherwise. */
export function addToolToCrewRow(subSessionID: string, tc: ToolCall): boolean {
  let container = toolContainers.get(subSessionID);
  if (container === undefined) {
    for (const card of cards.values()) {
      const row = card.querySelector(
        `.crew-row[data-session-id="${CSS.escape(subSessionID)}"]`,
      );
      if (row !== null) {
        container = getOrCreateToolContainer(row as HTMLDivElement, subSessionID);
        break;
      }
    }
  }
  if (container === undefined) return false;
  const el = buildToolCard({
    id: tc.id, title: tc.title, kind: tc.kind,
    status: tc.status, live: false,
  });
  el.classList.add("crew-tool-card");
  container.appendChild(el);
  crewToolEls.set(tc.id, el);
  // Update the collapsed-row activity line with the tool name.
  if (isToolActive(tc.status)) {
    setSubagentActivity(subSessionID, formatToolActivity(tc.title));
  }
  return true;
}

/** Update a tool card inside a crew row. Returns the element if found. */
export function getCrewToolEl(toolCallID: string): HTMLDivElement | undefined {
  return crewToolEls.get(toolCallID);
}

/** Resolve a subagent's display name from the current crew state. */
export function getSubagentName(subSessionID: string): string {
  for (const card of cards.values()) {
    const row = card.querySelector(
      `.crew-row[data-session-id="${CSS.escape(subSessionID)}"]`,
    );
    if (row !== null) {
      const name = row.querySelector(".crew-name");
      return name?.textContent ?? subSessionID;
    }
  }
  return subSessionID;
}

export function addCrew(
  messageID: string, crew: Crew,
  append: (el: HTMLElement) => void,
): void {
  const existing = cards.get(messageID);
  if (existing !== undefined) {
    applyState(existing, crew);
    return;
  }
  breakToolGroup();
  const el = build(messageID, crew);
  cards.set(messageID, el);
  append(el);
  trimOldMessages();
  scroll();
}

export function updateCrew(
  messageID: string, crew: Crew,
  append: (el: HTMLElement) => void,
): void {
  const el = cards.get(messageID);
  if (el === undefined) {
    addCrew(messageID, crew, append);
    return;
  }
  applyState(el, crew);
}

export function buildCrewCardForReplay(
  messageID: string, crew: Crew,
): HTMLDivElement {
  const el = build(messageID, crew);
  cards.set(messageID, el);
  return el;
}

export function clearCrews(): void {
  cards.clear();
  toolContainers.clear();
  expandedRows.clear();
  crewToolEls.clear();
  activityEls.clear();
  pendingApprovals.clear();
}

// --- DOM builders ---

function build(messageID: string, crew: Crew): HTMLDivElement {
  const el = document.createElement("div");
  el.className = "crew-card";
  el.dataset["messageId"] = messageID;

  const header = document.createElement("div");
  header.className = "crew-header";
  const icon = document.createElement("span");
  icon.className = "crew-icon";
  icon.textContent = "\u2699";
  const title = document.createElement("span");
  title.className = "crew-title";
  title.textContent = titleFor(crew);
  const count = document.createElement("span");
  count.className = "crew-count";
  header.append(icon, title, count);

  const body = document.createElement("div");
  body.className = "crew-body";

  el.append(header, body);
  applyState(el, crew);
  return el;
}

function applyState(el: HTMLDivElement, crew: Crew): void {
  const sig = signature(crew);
  if (cardState.get(el) === sig) return;
  cardState.set(el, sig);
  // Drain previous bindLoadingState unbinds before rebuilding.
  for (const fn of loadingUnbinds) fn();
  loadingUnbinds.length = 0;
  const body = el.querySelector(".crew-body") as HTMLDivElement;
  const count = el.querySelector(".crew-count") as HTMLSpanElement;

  // Capture draft input values before destroying rows.
  const drafts = new Map<string, string>();
  for (const input of body.querySelectorAll<HTMLInputElement>(".crew-msg-field")) {
    const row = input.closest<HTMLDivElement>(".crew-row");
    const sid = row?.dataset["sessionId"];
    if (sid !== undefined && input.value !== "") drafts.set(sid, input.value);
  }

  body.replaceChildren();

  const active = crew.subagents.filter((s) => s.status === "working").length;
  const done = crew.subagents.filter((s) => s.status === "terminated").length;
  const pending = crew.pending_stages?.length ?? 0;
  let countText = active > 0
    ? `${String(active)} running \u00b7 ${String(done)} done`
    : `${String(crew.subagents.length)} done`;
  if (pending > 0) countText += ` \u00b7 ${String(pending)} pending`;
  count.textContent = countText;
  el.classList.toggle("crew-done", active === 0 && pending === 0);

  for (const sub of crew.subagents) {
    const r = buildRow(sub);
    body.appendChild(r);
    const tc = toolContainers.get(sub.session_id);
    if (tc !== undefined) {
      const expandBody = r.querySelector(".crew-row-expand") as HTMLDivElement;
      expandBody.appendChild(tc);
    }
  }
  for (const ps of crew.pending_stages ?? []) {
    body.appendChild(buildPendingRow(ps));
  }

  // Restore draft input values.
  for (const [sid, val] of drafts) {
    const row = body.querySelector<HTMLDivElement>(`.crew-row[data-session-id="${CSS.escape(sid)}"]`);
    const input = row?.querySelector<HTMLInputElement>(".crew-msg-field");
    if (input !== undefined && input !== null) input.value = val;
  }
}

export function signature(crew: Crew): string {
  let s = crew.group + "|";
  for (const sub of crew.subagents) {
    s += `${sub.session_id}:${sub.status}:${sub.status_msg ?? ""};`;
  }
  for (const ps of crew.pending_stages ?? []) {
    s += `p:${ps.name};`;
  }
  return s;
}

// --- Row builders ---

// --- Data-driven status icon map ---

interface StatusIconDef {
  svg: string;
  label: string;
  className: string;
}

const CREW_STATUS_ICONS: Readonly<Record<CrewStatus, StatusIconDef>> = {
  working: {
    svg: ICON_SPINNER_14,
    label: "Running",
    className: "crew-icon-status crew-spinning",
  },
  terminated: {
    svg: ICON_CHECK_14,
    label: "Done",
    className: "crew-icon-status crew-done-icon",
  },
  error: {
    svg: ICON_ERROR_14,
    label: "Error",
    className: "crew-icon-status crew-error-icon",
  },
  pending: {
    svg: ICON_PENDING_14,
    label: "Pending",
    className: "crew-icon-status crew-pending-icon",
  },
};

function createStatusIcon(status: CrewStatus): HTMLSpanElement {
  const def = CREW_STATUS_ICONS[status];
  const span = document.createElement("span");
  span.className = def.className;
  span.setAttribute("aria-label", def.label);
  span.innerHTML = def.svg;
  return span;
}

function buildRow(sub: CrewSubagent): HTMLDivElement {
  const r = document.createElement("div");
  r.className = `crew-row crew-status-${sub.status}`;
  r.dataset["sessionId"] = sub.session_id;

  const head = document.createElement("button");
  head.type = "button";
  head.className = "crew-row-head";
  head.appendChild(createStatusIcon(sub.status));
  const nameSpan = document.createElement("span");
  nameSpan.className = "crew-name";
  nameSpan.textContent = sub.session_name || sub.session_id;
  const agentSpan = document.createElement("span");
  agentSpan.className = "crew-agent";
  agentSpan.textContent = sub.agent_name;
  head.append(nameSpan, agentSpan);
  if (sub.depends_on !== undefined && sub.depends_on.length > 0) {
    head.appendChild(createDepsIndicator(sub.depends_on));
  }
  const actEl = document.createElement("span");
  actEl.className = "crew-row-activity";
  head.appendChild(actEl);
  const chevron = document.createElement("span");
  chevron.className = "crew-chevron";
  chevron.textContent = "\u25b8";
  head.appendChild(chevron);
  r.appendChild(head);

  // Register the activity element for live updates.
  activityEls.set(sub.session_id, actEl);

  // Set initial activity text based on status.
  if (sub.status === "terminated") {
    actEl.textContent = "Done";
    actEl.classList.add("crew-activity-done");
  } else if (pendingApprovals.has(sub.session_id)) {
    actEl.textContent = "\u26a0 tool approval needed";
  } else if (sub.status_msg !== undefined && sub.status_msg !== "") {
    actEl.textContent = sub.status_msg;
  } else if (sub.status === "working") {
    actEl.textContent = "Working\u2026";
  }

  // Expandable body: query + tools + message input.
  const expand = document.createElement("div");
  expand.className = "crew-row-expand";
  const isExpanded = expandedRows.has(sub.session_id);
  if (!isExpanded) expand.classList.add("hidden");

  if (sub.initial_query !== "") {
    const query = document.createElement("div");
    query.className = "crew-row-query";
    query.textContent = sub.initial_query;
    expand.appendChild(query);
  }

  // Message input at the bottom of the expanded body.
  const inputRow = document.createElement("div");
  inputRow.className = "crew-msg-input";
  const input = document.createElement("input");
  input.type = "text";
  input.placeholder = "Message this subagent\u2026";
  input.className = "crew-msg-field";
  const sendBtn = document.createElement("button");
  sendBtn.type = "button";
  sendBtn.className = "crew-msg-send";
  sendBtn.textContent = "\u2191";
  sendBtn.setAttribute("data-tooltip", "Send");
  loadingUnbinds.push(bindLoadingState("crew.send_message", sendBtn));
  const doSend = (): void => {
    const text = input.value.trim();
    if (text === "") return;
    const chatID = getActiveId();
    if (chatID === "") return;
    const saved = input.value;
    input.value = "";
    void sendMessage.dispatch({ chatID, subSessionID: sub.session_id, text }, {
      onError: () => { input.value = saved; },
    });
  };
  sendBtn.addEventListener("click", doSend);
  input.addEventListener("keydown", (e) => {
    if (e.key === "Enter") { e.preventDefault(); doSend(); }
  });
  inputRow.append(input, sendBtn);
  expand.appendChild(inputRow);

  r.appendChild(expand);

  head.addEventListener("click", () => {
    const nowHidden = expand.classList.toggle("hidden");
    if (nowHidden) {
      expandedRows.delete(sub.session_id);
    } else {
      expandedRows.add(sub.session_id);
    }
    r.querySelector(".crew-chevron")?.classList.toggle("crew-chevron-open", !nowHidden);
  });
  if (isExpanded) {
    r.querySelector(".crew-chevron")?.classList.add("crew-chevron-open");
  }

  return r;
}

function buildPendingRow(ps: CrewPendingStage): HTMLDivElement {
  const r = document.createElement("div");
  r.className = "crew-row crew-status-pending";
  const head = document.createElement("div");
  head.className = "crew-row-head";
  head.appendChild(createStatusIcon("pending"));
  const nameSpan = document.createElement("span");
  nameSpan.className = "crew-name";
  nameSpan.textContent = ps.name;
  const agentSpan = document.createElement("span");
  agentSpan.className = "crew-agent";
  agentSpan.textContent = ps.agent_name;
  head.append(nameSpan, agentSpan);
  if (ps.depends_on !== undefined && ps.depends_on.length > 0) {
    head.appendChild(createDepsIndicator(ps.depends_on));
  }
  r.appendChild(head);
  return r;
}

function createDepsIndicator(deps: string[]): HTMLSpanElement {
  const span = document.createElement("span");
  span.className = "crew-deps";
  span.title = `Depends on: ${deps.join(", ")}`;
  span.textContent = `\u2193 ${String(deps.length)}`;
  return span;
}

export function titleFor(crew: Crew): string {
  const g = crew.group.startsWith("crew-") ? crew.group.slice(5) : crew.group;
  return g.length === 0 ? "Crew" : `Crew: ${g}`;
}

function getOrCreateToolContainer(
  row: HTMLDivElement, sessionID: string,
): HTMLDivElement {
  let container = toolContainers.get(sessionID);
  if (container !== undefined) {
    const expand = row.querySelector(".crew-row-expand");
    if (expand !== null && container.parentElement !== expand) {
      // Insert before the message input (last child of expand).
      const msgInput = expand.querySelector(".crew-msg-input");
      expand.insertBefore(container, msgInput);
    }
    return container;
  }
  container = document.createElement("div");
  container.className = "crew-row-tools";
  const expand = row.querySelector(".crew-row-expand");
  if (expand !== null) {
    const msgInput = expand.querySelector(".crew-msg-input");
    expand.insertBefore(container, msgInput);
  }
  toolContainers.set(sessionID, container);
  return container;
}


export { formatToolActivity } from "./format-tool-activity.js";

/** Update the activity line when a tool call completes. Called from
 *  messages.ts updateToolCall after propagating to the crew-row clone. */
export function onCrewToolCompleted(subSessionID: string): void {
  // If there's no pending approval, show "Done" or clear the activity.
  if (pendingApprovals.has(subSessionID)) return;
  const el = activityEls.get(subSessionID);
  if (el === undefined) return;
  // Check if there are still active tools for this subagent.
  const container = toolContainers.get(subSessionID);
  if (container !== undefined) {
    const activeTool = container.querySelector(".tool-status.pending, .tool-status.in_progress");
    if (activeTool !== null) return; // Still has active tools.
  }
  el.textContent = "Done";
  el.classList.add("crew-activity-done");
}
