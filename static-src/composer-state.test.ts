// Per-chat composer state: the draft text and the staged attachments.
//
// The bug these cover is a bleed: one textarea and one pill row served every
// chat, so switching tabs carried the previous conversation's half-written
// message over and threw its attachments away. Every case here is about the
// save-then-restore pair holding across a switch, a reload and a failed send.
import { describe, it, expect, beforeEach, vi } from "vitest";

const { mockDispatch, mockFlush, mockPending } = vi.hoisted(() => ({
  mockDispatch: vi.fn(),
  mockFlush: vi.fn(),
  mockPending: vi.fn(() => false),
}));

// The debounce is the library's; what this file tests is WHICH chat id and text
// reach it and WHEN a flush is forced. The fake records both and reports its own
// pending state so the flush rule can be driven either way.
vi.mock("./actions/index.js", () => ({
  debouncedDispatch: () =>
    Object.assign(mockDispatch, { isPending: mockPending, flush: mockFlush, cancel: vi.fn() }),
  registerCleanup: vi.fn(),
}));
// Both composer writers, because attachments.ts dispatches through the same
// debounced-action layer as the draft: a mock naming only one of them fails the
// module's IMPORT, not an assertion, so the whole file goes red with no clue why.
vi.mock("./actions/chat.js", () => ({
  setDraft: { name: "chat.set_draft" },
  setAttachments: { name: "chat.set_attachments" },
}));

import {
  initComposerState,
  noteComposerText,
  flushComposerDraft,
  saveComposerState,
  restoreComposerState,
  retargetComposer,
  restoreFailedSend,
  seedComposerState,
  adoptRemoteComposerState,
  dropComposerState,
  _resetComposerStateForTest,
} from "./composer-state.js";
import {
  addAttachment,
  takeAttachments,
  addAttachmentTo,
  attachmentGeneration,
} from "./attachments.js";
import { setSessions } from "./store.js";
import type { Session } from "./types.js";

function makeSession(id: string, draft?: string): Session {
  const s: Session = {
    id,
    name: id,
    model: "",
    acp_session_id: "",
    current_mode_id: "",
    usage: {
      context_pct: 0,
      context_size: 0,
      credits: 0,
      turn_count: 0,
      last_turn_ms: 0,
      has_real_data: false,
    },
    message_count: 0,
    messages: [],
    has_more: false,
    thinking: false,
    working_label: "Thinking",
  };
  if (draft !== undefined) {
    s.draft = draft;
  }
  return s;
}

function input(): HTMLTextAreaElement {
  return document.getElementById("prompt-input") as HTMLTextAreaElement;
}

/** Type into the composer the way a keystroke does: the value AND the `input`
 *  event that tells this module the value is now the draft. A bare `.value` write
 *  is deliberately NOT this — see the history case below. */
function type(text: string): void {
  input().value = text;
  input().dispatchEvent(new Event("input"));
}

beforeEach(() => {
  document.body.innerHTML = `
    <div id="prompt-box">
      <textarea id="prompt-input"></textarea>
      <ul id="attachment-row" class="hidden"></ul>
    </div>`;
  _resetComposerStateForTest();
  // Every chat referenced below is dropped so the attachment stash cannot carry
  // between cases through the module singleton.
  for (const id of ["c1", "c2", "c3"]) {
    dropComposerState(id);
  }
  mockDispatch.mockClear();
  mockFlush.mockClear();
  mockPending.mockReturnValue(false);
  setSessions([]);
  initComposerState();
});

describe("draft text across a chat switch", () => {
  it("keeps each chat's typed text and shows the right one", () => {
    restoreComposerState("c1");
    input().value = "half a question about auth";
    noteComposerText(input().value);

    // Switching: save the outgoing chat, then restore the incoming one.
    saveComposerState();
    restoreComposerState("c2");
    expect(input().value).toBe("");

    input().value = "unrelated thought";
    noteComposerText(input().value);
    saveComposerState();
    restoreComposerState("c1");
    expect(input().value).toBe("half a question about auth");

    saveComposerState();
    restoreComposerState("c2");
    expect(input().value).toBe("unrelated thought");
  });

  // The reported bug, and the one case the save-then-restore pair could not
  // reach: THREE call sites move the store's active chat without an activation
  // behind them, and two of them then await a tab round trip. For the length of
  // that await the box still belonged to the previous chat, so a keystroke went
  // into ITS entry and was flushed to the server as ITS draft.
  //
  // Both halves of what the user sees are in this one case: the text vanishes
  // out of the box when the activation finally repaints it, and it is sitting in
  // the previous chat the next time that chat is opened.
  describe("a chat that becomes active before its tab does", () => {
    it("files a keystroke in the window under the NEW chat", () => {
      restoreComposerState("c1");
      type("half a question about auth");

      // createSession: setActive(new) → retargetComposer(new) → await the tab.
      retargetComposer("c2");
      type("the new chat's first line");

      // The activation lands and repaints. It must find the text still there.
      saveComposerState();
      restoreComposerState("c2");
      expect(input().value).toBe("the new chat's first line");

      // And the chat the user left must be untouched by any of it.
      saveComposerState();
      restoreComposerState("c1");
      expect(input().value).toBe("half a question about auth");
    });

    it("persists the window's text under the new chat, not the old one", () => {
      restoreComposerState("c1");
      type("half a question about auth");
      mockDispatch.mockClear();

      retargetComposer("c2");
      type("the new chat's first line");

      expect(mockDispatch).toHaveBeenLastCalledWith({
        chatID: "c2",
        text: "the new chat's first line",
      });
    });
  });

  it("empties the box for a chat with no draft rather than leaving it alone", () => {
    // Leaving it alone IS the bleed. A chat that has never been typed into must
    // not inherit the previous conversation's text.
    restoreComposerState("c1");
    input().value = "text belonging to c1";
    noteComposerText(input().value);
    saveComposerState();
    restoreComposerState("c3");
    expect(input().value).toBe("");
  });

  it("persists the outgoing chat's text under the outgoing chat's id", () => {
    restoreComposerState("c1");
    noteComposerText("for c1");
    expect(mockDispatch).toHaveBeenLastCalledWith({ chatID: "c1", text: "for c1" });

    input().value = "for c1";
    mockPending.mockReturnValue(true);
    saveComposerState();
    // The flush carries c1, not the chat being switched TO: the outgoing id is
    // unrecoverable once the store has moved on, which is why the save runs first.
    expect(mockFlush).toHaveBeenCalledWith({ chatID: "c1", text: "for c1" });
  });

  it("records nothing when no chat owns the composer", () => {
    noteComposerText("orphan keystroke");
    expect(mockDispatch).not.toHaveBeenCalled();
  });

  it("does not re-dispatch text the server already holds", () => {
    restoreComposerState("c1");
    noteComposerText("same");
    noteComposerText("same");
    expect(mockDispatch).toHaveBeenCalledTimes(1);
  });
});

describe("the listeners this module owns on the composer element", () => {
  it("records typing through the input event", () => {
    restoreComposerState("c1");
    input().value = "typed";
    input().dispatchEvent(new Event("input"));
    expect(mockDispatch).toHaveBeenLastCalledWith({ chatID: "c1", text: "typed" });
  });

  it("records a programmatic write, which prompt-input announces the same way", () => {
    // The submit clear and the failed-send restore both write .value and then
    // dispatch input; without that the autosave would keep a sent message on the
    // chat record and a reload would put it back.
    restoreComposerState("c1");
    input().value = "about to be sent";
    input().dispatchEvent(new Event("input"));
    input().value = "";
    input().dispatchEvent(new Event("input"));
    expect(mockDispatch).toHaveBeenLastCalledWith({ chatID: "c1", text: "" });
  });

  it("flushes on blur", () => {
    restoreComposerState("c1");
    type("half typed");
    mockPending.mockReturnValue(true);
    input().dispatchEvent(new FocusEvent("blur"));
    expect(mockFlush).toHaveBeenCalledWith({ chatID: "c1", text: "half typed" });
  });
});

describe("flushing a pending save", () => {
  it("flushes the recorded draft when a save is pending (blur, close, unload)", () => {
    restoreComposerState("c1");
    type("typed but not yet saved");
    mockPending.mockReturnValue(true);
    flushComposerDraft();
    expect(mockFlush).toHaveBeenCalledWith({ chatID: "c1", text: "typed but not yet saved" });
  });

  it("sends nothing when the debounce already fired", () => {
    restoreComposerState("c1");
    mockPending.mockReturnValue(false);
    flushComposerDraft();
    expect(mockFlush).not.toHaveBeenCalled();
  });

  it("flushes on pagehide, which is the unload event iOS actually delivers", () => {
    restoreComposerState("c1");
    type("unsent");
    mockPending.mockReturnValue(true);
    window.dispatchEvent(new Event("pagehide"));
    expect(mockFlush).toHaveBeenCalledWith({ chatID: "c1", text: "unsent" });
  });

  it("flushes the recorded draft, not whatever the box happens to be showing", () => {
    // The textarea is a DISPLAY surface: ArrowUp puts a submitted prompt in it
    // through a write that deliberately emits no `input` event, so the map is what
    // the draft IS. Reading the element here sent that old prompt to the server and
    // replaced the draft with it, losing the text in both places at once.
    // prompt-input-composer.test.ts drives the same loss through the real key path.
    restoreComposerState("c1");
    type("the real draft");
    input().value = "a prompt from the history"; // silent, like setInputValue
    mockPending.mockReturnValue(true);
    flushComposerDraft();
    expect(mockFlush).toHaveBeenCalledWith({ chatID: "c1", text: "the real draft" });
  });

  it("keeps the mapped draft when a save runs while history is on screen", () => {
    restoreComposerState("c1");
    type("the real draft");
    input().value = "a prompt from the history";
    saveComposerState();
    restoreComposerState("c1");
    expect(input().value).toBe("the real draft");
  });
});

describe("adopting the server's draft (the reload case)", () => {
  it("puts the stored draft back in the box on first load of a chat", () => {
    setSessions([makeSession("c1", "survived the reload")]);
    restoreComposerState("c1");
    expect(input().value).toBe("");
    seedComposerState("c1");
    expect(input().value).toBe("survived the reload");
  });

  it("loses to a draft this device is already holding", () => {
    setSessions([makeSession("c1", "stale server copy")]);
    restoreComposerState("c1");
    input().value = "what the user is typing now";
    noteComposerText(input().value);
    seedComposerState("c1");
    // Unchanged: the fetch does not get to overwrite live typing.
    expect(input().value).toBe("what the user is typing now");
    saveComposerState();
    restoreComposerState("c1");
    expect(input().value).toBe("what the user is typing now");
  });

  it("loses to text already in the box, even with no recorded draft", () => {
    // The fetch can land after a failed send put its text back.
    setSessions([makeSession("c1", "server copy")]);
    restoreComposerState("c1");
    input().value = "restored after a failed send";
    seedComposerState("c1");
    expect(input().value).toBe("restored after a failed send");
  });

  it("does not touch the box when the seed arrives for another chat", () => {
    setSessions([makeSession("c1", "c1 draft"), makeSession("c2", "c2 draft")]);
    restoreComposerState("c2");
    input().value = "c2 live text";
    seedComposerState("c1");
    expect(input().value).toBe("c2 live text");
  });
});

// The other half of what submit.ts does on a refused send. The version this
// replaced wrote the shared box with no chat argument at all, while the
// attachment half beside it was already chat-scoped.
describe("restoring a send that the server refused", () => {
  it("puts the text back when the failing chat is the one on screen", () => {
    restoreComposerState("c1");
    restoreFailedSend("c1", "the refused message");
    expect(input().value).toBe("the refused message");
  });

  it("parks it under the failing chat when the reader has moved on", () => {
    restoreComposerState("c1");
    saveComposerState();
    restoreComposerState("c2");

    restoreFailedSend("c1", "the refused message");
    // The visible conversation is untouched: it never sent this.
    expect(input().value).toBe("");

    saveComposerState();
    restoreComposerState("c1");
    expect(input().value).toBe("the refused message");
  });

  it("persists it under the failing chat, so a reload keeps it", () => {
    restoreComposerState("c2");
    mockDispatch.mockClear();
    restoreFailedSend("c1", "the refused message");
    expect(mockDispatch).toHaveBeenLastCalledWith({
      chatID: "c1",
      text: "the refused message",
    });
  });

  it("loses to a draft the user has started since the send", () => {
    restoreComposerState("c1");
    type("already typing the next thing");
    restoreFailedSend("c1", "the refused message");
    expect(input().value).toBe("already typing the next thing");
  });

  it("leaves a non-empty box alone even with no recorded draft", () => {
    // ArrowUp puts a previously sent prompt in the box through a write that emits
    // no `input` event, so the box can hold history while the draft is empty.
    restoreComposerState("c1");
    input().value = "a prompt recalled from history";
    restoreFailedSend("c1", "the refused message");
    expect(input().value).toBe("a prompt recalled from history");
  });

  it("ignores an empty chat id and empty text", () => {
    restoreComposerState("c1");
    restoreFailedSend("", "the refused message");
    restoreFailedSend("c1", "");
    expect(input().value).toBe("");
  });
});

describe("attachments across a chat switch", () => {
  it("parks the outgoing chat's attachments and brings them back", () => {
    restoreComposerState("c1");
    addAttachment("src/a.ts");
    addAttachment("src/b.ts");

    saveComposerState();
    restoreComposerState("c2");
    // c2 starts clean: the pills the user staged belong to c1.
    expect(takeAttachments()).toEqual([]);

    saveComposerState();
    restoreComposerState("c1");
    expect(takeAttachments().map((a) => a.path)).toEqual(["src/a.ts", "src/b.ts"]);
  });

  it("restores a failed send's attachments to the chat that failed, not the visible one", () => {
    restoreComposerState("c1");
    addAttachment("src/a.ts");
    takeAttachments(); // the send took them

    saveComposerState();
    restoreComposerState("c2");
    addAttachmentTo("c1", "src/a.ts"); // the failure lands after the switch
    expect(takeAttachments()).toEqual([]);

    saveComposerState();
    restoreComposerState("c1");
    expect(takeAttachments().map((a) => a.path)).toEqual(["src/a.ts"]);
  });

  it("forgets a closed chat's parked attachments", () => {
    restoreComposerState("c1");
    addAttachment("src/a.ts");
    saveComposerState();
    restoreComposerState("c2");

    dropComposerState("c1");
    saveComposerState();
    restoreComposerState("c1");
    expect(takeAttachments()).toEqual([]);
  });

  // Dropping the LIVE chat's state clears the box, and that is not cosmetic. On
  // an ordinary close the tab store activates a neighbour straight after, and
  // restoreComposerState overwrites the box anyway; on the close that empties the
  // strip nothing follows, so the text stayed on screen in a composer that was
  // still live while removeChat had already pointed the store's active chat at an
  // unrelated row. Send then posted it there, which is the reported "my message
  // ended up in a previous tab".
  it("clears the composer when the live chat's state is dropped", () => {
    restoreComposerState("c1");
    type("half a thought");

    dropComposerState("c1");

    expect(input().value).toBe("");
  });

  // The box belongs to whoever is live, so dropping some OTHER chat's state must
  // not reach into it.
  it("leaves the composer alone when a background chat's state is dropped", () => {
    restoreComposerState("c1");
    type("still typing");

    dropComposerState("c2");

    expect(input().value).toBe("still typing");
  });

  // The close and the failure race, and the close is the one that has to win: it
  // said this chat's staged files are forgotten, so a request that was already in
  // flight must not write them back for the next open to find.
  it("does not resurrect a closed chat's attachments when the send fails afterwards", () => {
    restoreComposerState("c1");
    addAttachment("src/a.ts");
    const gen = attachmentGeneration("c1"); // read where submit.ts reads it
    takeAttachments(); // Send fires: the row empties

    dropComposerState("c1"); // the × while the request is in flight
    addAttachmentTo("c1", "src/a.ts", gen); // the failure lands after the close

    restoreComposerState("c1"); // reopened from History
    expect(takeAttachments()).toEqual([]);
  });

  it("still restores a failed send's attachments when the chat was NOT closed", () => {
    // The other direction of the same guard: a failure is the normal case and must
    // keep putting the pills back, or a throttled turn costs the files too.
    restoreComposerState("c1");
    addAttachment("src/a.ts");
    const gen = attachmentGeneration("c1");
    takeAttachments();

    saveComposerState();
    restoreComposerState("c2"); // the user is looking elsewhere when it fails
    addAttachmentTo("c1", "src/a.ts", gen);

    saveComposerState();
    restoreComposerState("c1");
    expect(takeAttachments().map((a) => a.path)).toEqual(["src/a.ts"]);
  });

  it("keeps a genuinely new attachment on a reopened chat", () => {
    // The invalidation is per-generation, not per-chat-forever: attaching again
    // after a close is a new state and belongs to the chat like any other.
    restoreComposerState("c1");
    addAttachment("src/old.ts");
    const stale = attachmentGeneration("c1");
    takeAttachments();
    dropComposerState("c1");

    restoreComposerState("c1");
    addAttachment("src/new.ts");
    addAttachmentTo("c1", "src/old.ts", stale); // refused
    expect(takeAttachments().map((a) => a.path)).toEqual(["src/new.ts"]);
  });
});

// A `draft_changed` frame converges a device that is NOT typing. Before it, a
// phone that had looked at a chat kept whatever it saw until the next full
// activation — and a tab switch then flushed that stale copy back over the newer
// one, which is the drift the local-authoritative rule exists to prevent in the
// OTHER direction.
describe("adopting a remote composer change", () => {
  it("updates a chat this device is not looking at", () => {
    restoreComposerState("c1");
    adoptRemoteComposerState("c2", "typed on the desktop", []);

    saveComposerState();
    restoreComposerState("c2");
    expect(input().value).toBe("typed on the desktop");
  });

  // The LIVE chat's map entry is authoritative: adopting there would overwrite the
  // box under the caret with a value that was current 600ms ago somewhere else.
  it("ignores a frame for the chat on screen", () => {
    restoreComposerState("c1");
    type("what I am typing right now");
    adoptRemoteComposerState("c1", "what the desktop had", []);
    expect(input().value).toBe("what I am typing right now");

    // And the map, not just the box: a switch away and back must not surface it.
    saveComposerState();
    restoreComposerState("c2");
    saveComposerState();
    restoreComposerState("c1");
    expect(input().value).toBe("what I am typing right now");
  });

  it("drops a frame with no chat id", () => {
    restoreComposerState("c1");
    type("mine");
    adoptRemoteComposerState("", "nobody's", []);
    expect(input().value).toBe("mine");
  });

  // Unlike the seed it does NOT lose to a local copy. The frame was produced by a
  // write the server accepted, so it is newer than whatever this device flushed
  // before it stopped typing in that chat.
  it("replaces a parked draft rather than deferring to it", () => {
    restoreComposerState("c1");
    type("stale, flushed an hour ago");
    saveComposerState();
    restoreComposerState("c2");

    adoptRemoteComposerState("c1", "fresh, from the desktop", []);

    saveComposerState();
    restoreComposerState("c1");
    expect(input().value).toBe("fresh, from the desktop");
  });

  // BOTH halves ride one frame, because a receiver cannot know which of the two
  // commands fired. Carrying only the field that moved would blank the other one.
  it("carries the attachments with the text", () => {
    restoreComposerState("c1");
    adoptRemoteComposerState("c2", "look at these", ["docs/spec.pdf"]);

    saveComposerState();
    restoreComposerState("c2");
    expect(input().value).toBe("look at these");
    expect(takeAttachments().map((a) => a.path)).toEqual(["docs/spec.pdf"]);
  });

  // An adoption is not a local edit, so it must not schedule a save. Publishing it
  // back would bump the record for a change that came from it, and every device
  // would re-apply what it already had.
  it("persists nothing", () => {
    restoreComposerState("c1");
    mockDispatch.mockClear();
    adoptRemoteComposerState("c2", "from elsewhere", ["docs/spec.pdf"]);
    expect(mockDispatch).not.toHaveBeenCalled();
    expect(mockFlush).not.toHaveBeenCalled();
  });
});

// The seed adopts the whole composer, not half of it: a reload that restored the
// sentence without the files it describes is worse than restoring neither.
describe("seeding the staged attachments from the chat record", () => {
  it("restores the row a reload emptied", () => {
    setSessions([{ ...makeSession("c1", "half a question"), attachments: ["docs/spec.pdf"] }]);
    restoreComposerState("c1");
    seedComposerState("c1");

    expect(input().value).toBe("half a question");
    expect(takeAttachments().map((a) => a.path)).toEqual(["docs/spec.pdf"]);
  });

  it("loses to a row the user has already staged into", () => {
    setSessions([{ ...makeSession("c1", ""), attachments: ["docs/theirs.pdf"] }]);
    restoreComposerState("c1");
    addAttachment("src/mine.ts");
    seedComposerState("c1");

    expect(takeAttachments().map((a) => a.path)).toEqual(["src/mine.ts"]);
  });
});
