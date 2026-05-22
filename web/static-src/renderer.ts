// ---------------------------------------------------------------------------
// Chat renderer: translates store changes into DOM mutations.
//
// Two entry points:
//   renderSwitch(session)  — full clear + walk, called on chat-switch
//   renderUpdates(session) — incremental, called on every store emit
//
// Incremental rules:
//   - New messages (index >= renderedCount) go through renderOne.
//   - Already-drawn crew messages get re-applied via updateCrew. Cheap
//     because the server dedups snapshots — we only actually see
//     refreshed payloads when kiro-cli's list_update changes state.
//   - The last message, if assistant, is live-streamed by refreshStreaming.
// ---------------------------------------------------------------------------

import type { Session, Message } from "./types.js";
import {
  clearMessages, addUserMessage, startStreamingMessage,
  appendToAssistant, finalizeAssistantEl,
  addToolCall, updateToolCall, addPlan, addSystemMessage,
  addBoundaryDivider, addCrew, updateCrew, addReasoningBlock,
  EVENT_BOUNDARY_META,
} from "./messages.js";
import { onSSE } from "./bus.js";

type StreamState =
  | { active: false }
  | { active: true; el: HTMLDivElement; messageID: string };

class ChatRenderer {
  private activeChatID = "";
  private renderedCount = 0;
  private userTurnCount = 0;
  private oldestTag = "";
  private streaming: StreamState = { active: false };

  constructor() {
    onSSE("turn_ended", (chatID) => {
      if (chatID !== this.activeChatID) return;
      if (this.streaming.active) {
        finalizeAssistantEl(this.streaming.el);
        this.streaming = { active: false };
      }
    });
  }

  renderSwitch(session: Session): void {
    clearMessages();
    this.activeChatID = session.id;
    this.renderedCount = 0;
    this.userTurnCount = 0;
    this.oldestTag = session.oldest_checkpoint_tag ?? "";
    this.streaming = { active: false };
    const chatView = document.getElementById("chat-view");
    if (chatView !== null) chatView.setAttribute("data-chat-id", session.id);
    this.renderUpdates(session);
  }

  renderUpdates(session: Session): void {
    if (session.id !== this.activeChatID) {
      this.renderSwitch(session);
      return;
    }
    this.oldestTag = session.oldest_checkpoint_tag ?? "";
    const batchSize = session.messages.length - this.renderedCount;
    let staggerIdx = 0;
    for (let i = this.renderedCount; i < session.messages.length; i++) {
      this.renderOne(session.messages[i]!, batchSize > 1 ? staggerIdx : -1);
      staggerIdx++;
    }
    this.renderedCount = session.messages.length;

    for (const m of session.messages) {
      if (m.role === "event" && m.event_kind === "crew" && m.crew !== undefined) {
        updateCrew(m.id, m.crew);
      }
    }

    const last = session.messages[session.messages.length - 1];
    if (last !== undefined && last.role === "assistant") this.refreshStreaming(last);
  }

  private renderOne(m: Message, staggerIdx: number): void {
    switch (m.role) {
      case "user":
        addUserMessage(m.content ?? "", checkpointTagForTurn(this.userTurnCount, this.oldestTag));
        this.userTurnCount++;
        this.streaming = { active: false };
        break;
      case "assistant":
        this.renderAssistant(m);
        break;
      case "event":
        renderEvent(m);
        break;
    }
    if (staggerIdx >= 0 && staggerIdx < 8) {
      const last = document.getElementById("messages")?.lastElementChild as HTMLElement | null;
      if (last !== null) last.style.setProperty("--stagger-index", String(staggerIdx));
    }
  }

  private renderAssistant(m: Message): void {
    if (m.operation_type === "Reasoning") {
      addReasoningBlock(m.content ?? "");
      return;
    }
    const el = startStreamingMessage();
    const text = m.content ?? "";
    if (text !== "") {
      appendToAssistant(el, text);
      finalizeAssistantEl(el);
    }
    this.streaming = { active: true, el, messageID: m.id };

    if (m.plan !== undefined && m.plan.length > 0) addPlan(m.plan);

    if (m.tool_calls !== undefined) {
      for (const tc of m.tool_calls) {
        addToolCall(tc);
      }
    }
  }

  private refreshStreaming(m: Message): void {
    if (!this.streaming.active || m.id !== this.streaming.messageID) {
      const el = startStreamingMessage();
      this.streaming = { active: true, el, messageID: m.id };
    }
    const existing = this.streaming.el.dataset["raw"] ?? "";
    const desired = m.content ?? "";
    if (desired.length > existing.length) {
      appendToAssistant(this.streaming.el, desired.slice(existing.length));
    }
    if (m.tool_calls !== undefined) {
      for (const tc of m.tool_calls) {
        updateToolCall(tc);
      }
    }
  }
}

// ---------------------------------------------------------------------------
// Pure helpers (exported for testing)
// ---------------------------------------------------------------------------

/** Return the checkpoint tag the server stored for the turn about to
 *  be rendered, or "" when no backing snapshot exists. */
export function checkpointTagForTurn(turnIndex: number, oldestTag: string): string {
  if (oldestTag === "") return "";
  const candidate = String(turnIndex);
  const oldestTurn = parseInt(oldestTag.split(".")[0] ?? "0", 10);
  if (!Number.isFinite(oldestTurn) || turnIndex < oldestTurn) return "";
  return candidate;
}

function renderEvent(m: Message): void {
  switch (m.event_kind) {
    case "compacted": {
      addBoundaryDivider("compacted", "Conversation compacted");
      if (m.content !== undefined && m.content !== "") {
        addSystemMessage(m.content);
      }
      return;
    }
    case "crew":
      if (m.crew !== undefined) addCrew(m.id, m.crew);
      return;
    case "cancelled":
      addSystemMessage("Cancelled");
      return;
    case "interrupted":
      addSystemMessage("Turn interrupted");
      return;
    default:
      break;
  }

  if (m.event_kind !== undefined) {
    const meta = EVENT_BOUNDARY_META[m.event_kind];
    if (meta !== undefined) {
      const content = m.content ?? "";
      const label = meta.labelFn ? meta.labelFn(content) : meta.defaultLabel;
      addBoundaryDivider(meta.boundary, label);
      return;
    }
  }

  addSystemMessage(m.content ?? "");
}

// ---------------------------------------------------------------------------
// Singleton instance + function exports that form the module's public API.
// ---------------------------------------------------------------------------

const renderer = new ChatRenderer();

export function renderSwitch(session: Session): void { renderer.renderSwitch(session); }
export function renderUpdates(session: Session): void { renderer.renderUpdates(session); }
