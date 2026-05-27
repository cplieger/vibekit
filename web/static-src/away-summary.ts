// ---------------------------------------------------------------------------
// Away summary: when the user returns after being away for >15 minutes
// AND the agent consumed >5K tokens, show a dismissible "Welcome back"
// banner summarizing what happened.
//
// v1 uses heuristic summaries (message/tool counts). v2 could use
// CheapestModel for AI-generated recaps.
// ---------------------------------------------------------------------------

import { getActive } from "./store.js";
import { showBanner } from "./banner-stack.js";

const AWAY_THRESHOLD_MS = 15 * 60 * 1000; // 15 minutes
const TOKEN_THRESHOLD = 5_000;

class AwaySummaryController {
  private lastHiddenAt = Date.now();
  private lastContextPct = 0;
  private lastMsgCount = 0;
  private lastChatId = "";

  init(): void {
    document.addEventListener("visibilitychange", () => {
      if (document.visibilityState === "visible") {
        this.checkAway();
      } else {
        this.snapshotState();
      }
    });
    this.snapshotState();
  }

  private snapshotState(): void {
    this.lastHiddenAt = Date.now();
    const s = getActive();
    if (s !== undefined) {
      this.lastChatId = s.id;
      this.lastContextPct = s.usage.context_pct;
      this.lastMsgCount = s.messages.length;
    }
  }

  private checkAway(): void {
    const elapsed = Date.now() - this.lastHiddenAt;
    if (elapsed < AWAY_THRESHOLD_MS) {
      return;
    }

    const s = getActive();
    if (s === undefined) {
      return;
    }

    if (s.id !== this.lastChatId) {
      this.snapshotState();
      return;
    }

    // Detect compaction: message array shrank while away.
    if (s.messages.length < this.lastMsgCount) {
      this.snapshotState();
      return;
    }

    const contextGrowth = s.usage.context_pct - this.lastContextPct;
    const contextSize = s.usage.context_size;
    const tokensConsumed = contextSize > 0 ? (contextGrowth / 100) * contextSize : 0;
    if (tokensConsumed < TOKEN_THRESHOLD) {
      return;
    }

    const newMsgs = s.messages.length - this.lastMsgCount;
    if (newMsgs <= 0) {
      return;
    }

    let assistantMsgs = 0;
    let toolCalls = 0;
    const changedPaths = new Set<string>();

    for (let i = this.lastMsgCount; i < s.messages.length; i++) {
      const m = s.messages[i]!;
      if (m.role === "assistant") {
        assistantMsgs++;
        if (m.tool_calls !== undefined) {
          for (const tc of m.tool_calls) {
            toolCalls++;
            if (tc.diffs !== undefined) {
              for (const d of tc.diffs) {
                changedPaths.add(d.path);
              }
            }
          }
        }
      }
    }

    const parts: string[] = [];
    if (assistantMsgs > 0) {
      parts.push(`${String(assistantMsgs)} response${assistantMsgs > 1 ? "s" : ""}`);
    }
    if (toolCalls > 0) {
      parts.push(`${String(toolCalls)} tool call${toolCalls > 1 ? "s" : ""}`);
    }
    if (changedPaths.size > 0) {
      parts.push(`${String(changedPaths.size)} file${changedPaths.size > 1 ? "s" : ""} changed`);
    }

    if (parts.length === 0) {
      return;
    }

    const awayMins = Math.round(elapsed / 60_000);
    const msg = `Welcome back (${String(awayMins)}m away). ${parts.join(", ")}.`;
    showBanner(s.id, "away-summary", msg, "info", true);

    this.snapshotState();
  }
}

const instance = new AwaySummaryController();

export function initAwaySummary(): void {
  instance.init();
}
