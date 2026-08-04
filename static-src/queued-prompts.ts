// ---------------------------------------------------------------------------
// Queued-prompts affordance: the visible pending-send indicator that sits in
// the prompt chrome (a row of chips between the textarea and the pill row).
//
// It shows the prompts that were posted while a turn is in flight and are
// waiting to drain on the next turn end — distinct from a sent user bubble
// (invariant vibekit.md #6: a queued prompt is a pending-send, never a
// message). Each chip previews the queued text and carries a cancel button;
// when more than one is queued a count chip shows the depth/order.
//
// Two controls per chip, one rule each. CANCEL discards — it never restores
// the text to the input, because the restore path was a data-loss trap in both
// directions: with a draft in the box it silently dropped the entry's
// attachments and text, and with an empty box it could clobber what a
// paste/undo was about to put there. Discard is what the × says. SEND-NOW
// jumps the wait for the one message that has become urgent: promote to the
// queue front, interrupt the streaming turn (the drain delivers on turn end)
// — mechanically an interrupt, so it ships with the interrupt's terminal
// teardown (§5.6 R3).
//
// State is a pure projection of `activeSession.prompt_queue`; the queue
// lifecycle itself is owned by prompt-queue.ts.
// ---------------------------------------------------------------------------

import { el, computed, effect } from "@cplieger/reactive";
import { announce } from "@cplieger/ui-primitives/announce";
import { $ } from "./dom.js";
import { activeSession, getActiveId } from "./store.js";
import { cancelQueuedPrompt, sendQueuedNow } from "./prompt-queue.js";
import { ICON_HOURGLASS, ICON_SEND } from "./icons.js";
import { iconEl } from "./icon-el.js";
import type { QueuedPrompt } from "./types.js";

const PREVIEW_MAX = 60;

let bound = false;
let prevCount = 0;
let prevId = "";

/** Wire the reactive render. Idempotent. Called once from app.ts. */
export function initQueuedPrompts(): void {
  if (bound) {
    return;
  }
  bound = true;
  const row = $.queuedRow;
  // Re-render only when the active chat or its queued prompt texts change.
  // The computed returns a string, so it dedups by value — an unrelated
  // active-session field write (usage, thinking) does not re-render the row.
  const sig = computed(() => {
    const s = activeSession.value;
    const q = s?.prompt_queue ?? [];
    return (s?.id ?? "") + "\u0001" + q.map((e) => e.text).join("\u0000");
  });
  effect(() => {
    void sig.value;
    render(row);
  });
}

function render(row: HTMLUListElement): void {
  const s = activeSession.peek();
  const queue = s?.prompt_queue ?? [];
  const count = queue.length;
  const id = s?.id ?? "";

  row.replaceChildren();
  if (count === 0) {
    row.classList.add("hidden");
    // Reset the announce baseline so switching to a chat that already has a
    // queue announces it fresh, but silently absorb the empty case.
    prevCount = 0;
    prevId = id;
    return;
  }
  row.classList.remove("hidden");

  if (count > 1) {
    row.appendChild(
      el("li", { className: "queued-count", "aria-hidden": "true" }, `${String(count)} queued`),
    );
  }
  queue.forEach((entry, i) => {
    row.appendChild(buildChip(entry, i));
  });

  // Announce count changes on the same chat only (skip pure chat switches so
  // returning to a chat with a standing queue isn't read out as "new").
  if (id === prevId && count !== prevCount) {
    announce(count === 1 ? "1 prompt queued to send" : `${String(count)} prompts queued to send`);
  }
  prevCount = count;
  prevId = id;
}

function buildChip(entry: QueuedPrompt, index: number): HTMLElement {
  const preview = truncate(entry.text);
  const icon = el(
    "span",
    { className: "queued-icon", "aria-hidden": "true" },
    iconEl(ICON_HOURGLASS),
  );
  const label = el("span", { className: "queued-text" }, preview);
  const sendNow = el(
    "button",
    {
      type: "button",
      className: "queued-send-now",
      "aria-label": `Send now: ${preview}`,
      "data-tooltip": "Send now — interrupts the current turn",
    },
    iconEl(ICON_SEND),
  );
  sendNow.addEventListener("click", () => {
    onSendNow(index);
  });
  const cancel = el(
    "button",
    {
      type: "button",
      className: "queued-cancel",
      "aria-label": `Discard queued prompt: ${preview}`,
      "data-tooltip": "Discard this queued prompt",
    },
    "\u00d7",
  );
  cancel.addEventListener("click", () => {
    onCancel(index);
  });
  return el("li", { className: "queued-prompt", title: entry.text }, icon, label, sendNow, cancel);
}

function onSendNow(index: number): void {
  const chatID = getActiveId();
  if (chatID === "") {
    return;
  }
  sendQueuedNow(chatID, index);
  announce("Sending this queued prompt next");
}

function onCancel(index: number): void {
  const chatID = getActiveId();
  if (chatID === "") {
    return;
  }
  if (cancelQueuedPrompt(chatID, index) !== undefined) {
    // Discard, never restore-to-input: the restore was a data-loss trap (it
    // dropped attachments+text when a draft was present, and clobbered the
    // box otherwise). The row re-render + the announcement report the change.
    announce("Queued prompt discarded");
  }
}

function truncate(text: string): string {
  const oneLine = text.replace(/\s+/g, " ").trim();
  return oneLine.length > PREVIEW_MAX ? oneLine.slice(0, PREVIEW_MAX - 1) + "\u2026" : oneLine;
}
