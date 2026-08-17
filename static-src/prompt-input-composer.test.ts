// @vitest-environment happy-dom
// The composer has ONE textarea and three modules writing to it, and exactly one
// of them owns what the draft IS.
//
// prompt-input owns the box's BEHAVIOUR, composer-state owns the per-chat DRAFT,
// share-target hands the box a prompt from the PWA share sheet. They meet on the
// element and never import each other (send-state imports prompt-input and
// transport imports send-state, so a static import from there to the draft
// action would close a cycle), so the contract is the `input` event: a write that
// means "this is the draft now" announces itself, and the draft layer keeps the
// value in its own map rather than reading the element back.
//
// Both halves of that contract lost text before this file existed:
//
//   - The draft layer read `$.promptInput.value` when it saved. ArrowUp displays
//     a previously submitted prompt through a SILENT write (history is
//     navigation, not editing), so a blur or a chat switch while one was on
//     screen sent that old prompt to the server AND replaced the real draft with
//     it — gone locally and remotely at once.
//   - share-target assigned `.value` with no event, so the map never learned the
//     shared text and nothing scheduled a save. A user who opened a shared prompt
//     and reloaded before typing lost what they had just been handed.
//
// Whole-module wiring rather than mocks on either side: the bug was in the seam,
// so a test that stubbed one half could not have seen it. Only the debounce
// (whose timing is the library's) and the two leaves that own unrelated DOM are
// faked.
import { describe, it, expect, beforeEach, vi } from "vitest";

import type * as Store from "./store.js";
import type * as ComposerState from "./composer-state.js";
import type * as PromptInput from "./prompt-input.js";
import type * as ShareTarget from "./share-target.js";
import type { Session } from "./types.js";

const { mockDispatch, mockFlush, mockPending, mockSubmit } = vi.hoisted(() => ({
  mockDispatch: vi.fn(),
  mockFlush: vi.fn(),
  mockPending: vi.fn(() => false),
  mockSubmit: vi.fn(),
}));

// What reaches the draft action, and when a flush is forced. The 600ms itself is
// the library's business.
vi.mock("./actions/index.js", () => ({
  debouncedDispatch: () =>
    Object.assign(mockDispatch, { isPending: mockPending, flush: mockFlush, cancel: vi.fn() }),
  registerCleanup: vi.fn(),
}));
vi.mock("./actions/chat.js", () => ({ setDraft: { name: "chat.set_draft" } }));
vi.mock("./platform.js", () => ({ fixIOSViewport: vi.fn() }));
vi.mock("./pill-expand.js", () => ({ collapseAll: vi.fn() }));
// share-target's other job is the ?agent=planner shortcut, whose import graph is
// the whole chat lifecycle.
vi.mock("./chat.js", () => ({ createPlannerSession: vi.fn() }));

/** The one prompt the chat has already sent, so ArrowUp has somewhere to go. */
const PRIOR_PROMPT = "the prompt I sent an hour ago";

function makeSession(id: string, prompts: string[]): Session {
  return {
    id,
    name: id,
    model: "",
    acp_session_id: "",
    current_mode_id: "",
    available_modes: [],
    available_models: [],
    usage: {
      context_pct: 0,
      context_size: 0,
      credits: 0,
      turn_count: 0,
      last_turn_ms: 0,
      has_real_data: false,
    },
    message_count: prompts.length,
    messages: prompts.map((content, i) => ({
      id: `m${String(i)}`,
      role: "user" as const,
      content,
      ts: i + 1,
    })),
    has_more: false,
    thinking: false,
    working_label: "Thinking",
  };
}

interface Mounted {
  store: typeof Store;
  composerState: typeof ComposerState;
  promptInput: typeof PromptInput;
  shareTarget: typeof ShareTarget;
  input: HTMLTextAreaElement;
}

/** Fresh module graph per case: prompt-input latches its own init and both
 *  modules wire listeners once, so a shared instance would carry the previous
 *  case's element and history position. */
async function mount(): Promise<Mounted> {
  vi.resetModules();
  document.body.innerHTML = `
    <div id="chat-area">
      <form id="prompt-form">
        <div id="prompt-box">
          <textarea id="prompt-input"></textarea>
          <ul id="attachment-row" class="hidden"></ul>
          <button id="send-btn" type="submit"></button>
        </div>
      </form>
    </div>`;
  const store = await import("./store.js");
  const composerState = await import("./composer-state.js");
  const promptInput = await import("./prompt-input.js");
  const shareTarget = await import("./share-target.js");
  store.setSessions([makeSession("c1", [PRIOR_PROMPT]), makeSession("c2", [])]);
  store.setActive("c1");
  // The order app.ts uses: the draft layer is listening before anything writes.
  composerState.initComposerState();
  promptInput.initPromptInput(mockSubmit, () => undefined);
  composerState.restoreComposerState("c1");
  return {
    store,
    composerState,
    promptInput,
    shareTarget,
    input: document.getElementById("prompt-input") as HTMLTextAreaElement,
  };
}

/** Type, the way a keystroke does: the value and the event that announces it. */
function type(input: HTMLTextAreaElement, text: string): void {
  input.value = text;
  input.dispatchEvent(new Event("input", { bubbles: true }));
}

function pressKey(input: HTMLTextAreaElement, key: string): KeyboardEvent {
  const e = new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true });
  input.dispatchEvent(e);
  return e;
}

beforeEach(() => {
  mockDispatch.mockClear();
  mockFlush.mockClear();
  mockSubmit.mockClear();
  mockPending.mockReturnValue(false);
  history.replaceState(null, "", "/");
});

describe("history cycling versus the per-chat draft", () => {
  it("shows the previous prompt without telling the draft layer about it", async () => {
    const { input } = await mount();
    type(input, "the draft I am still writing");
    expect(mockDispatch).toHaveBeenLastCalledWith({
      chatID: "c1",
      text: "the draft I am still writing",
    });

    expect(pressKey(input, "ArrowUp").defaultPrevented).toBe(true);
    expect(input.value).toBe(PRIOR_PROMPT);
    // The display is not an edit: nothing new reached the action.
    expect(mockDispatch).toHaveBeenCalledTimes(1);
  });

  it("flushes the typed draft on blur, not the history item on screen", async () => {
    const { input } = await mount();
    type(input, "the draft I am still writing");
    pressKey(input, "ArrowUp");
    expect(input.value).toBe(PRIOR_PROMPT);

    mockPending.mockReturnValue(true);
    input.dispatchEvent(new FocusEvent("blur"));
    // The old read of `.value` sent PRIOR_PROMPT here, which is the server-side
    // half of the loss.
    expect(mockFlush).toHaveBeenCalledWith({
      chatID: "c1",
      text: "the draft I am still writing",
    });
  });

  it("brings the typed draft back after a chat switch made while history showed", async () => {
    const { input, composerState } = await mount();
    type(input, "the draft I am still writing");
    pressKey(input, "ArrowUp");
    mockPending.mockReturnValue(true);

    composerState.saveComposerState();
    composerState.restoreComposerState("c2");
    expect(input.value).toBe("");
    composerState.saveComposerState();
    composerState.restoreComposerState("c1");

    // The local half: the map entry survived rather than being replaced by the
    // prompt the box was showing.
    expect(input.value).toBe("the draft I am still writing");
    expect(mockFlush).toHaveBeenCalledWith({
      chatID: "c1",
      text: "the draft I am still writing",
    });
  });

  it("adopts a history item the user actually edits", async () => {
    // The other direction: once a keystroke lands on the displayed prompt it IS
    // the draft, because the edit announces itself like any other.
    const { input, composerState } = await mount();
    type(input, "scratch");
    pressKey(input, "ArrowUp");
    type(input, `${PRIOR_PROMPT}, but shorter`);

    composerState.saveComposerState();
    composerState.restoreComposerState("c1");
    expect(input.value).toBe(`${PRIOR_PROMPT}, but shorter`);
  });

  it("records the submit's clear, so a sent message is not left on the record", async () => {
    const { input } = await mount();
    type(input, "send me");
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    expect(mockSubmit).toHaveBeenCalledWith("send me");
    expect(input.value).toBe("");
    expect(mockDispatch).toHaveBeenLastCalledWith({ chatID: "c1", text: "" });
  });
});

describe("a prompt arriving from the share sheet", () => {
  it("is recorded for the active chat exactly once", async () => {
    const { input, shareTarget } = await mount();
    history.replaceState(null, "", "/?prompt=fix%20the%20flaky%20test");
    shareTarget.applyShareTarget();

    expect(input.value).toBe("fix the flaky test");
    // The silent assignment left this at zero, so nothing scheduled a save and a
    // reload before the first keystroke lost the shared text.
    expect(mockDispatch).toHaveBeenCalledTimes(1);
    expect(mockDispatch).toHaveBeenCalledWith({ chatID: "c1", text: "fix the flaky test" });
  });

  it("is persisted by the flush a reload runs", async () => {
    const { shareTarget } = await mount();
    history.replaceState(null, "", "/?prompt=fix%20the%20flaky%20test");
    shareTarget.applyShareTarget();

    mockPending.mockReturnValue(true);
    window.dispatchEvent(new Event("pagehide"));
    expect(mockFlush).toHaveBeenCalledWith({ chatID: "c1", text: "fix the flaky test" });
  });

  it("survives a switch away and back like any other draft", async () => {
    const { input, composerState, shareTarget } = await mount();
    history.replaceState(null, "", "/?prompt=fix%20the%20flaky%20test");
    shareTarget.applyShareTarget();

    composerState.saveComposerState();
    composerState.restoreComposerState("c2");
    composerState.saveComposerState();
    composerState.restoreComposerState("c1");
    expect(input.value).toBe("fix the flaky test");
  });
});
