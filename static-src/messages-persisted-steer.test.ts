// ---------------------------------------------------------------------------
// A PERSISTED steer renders through the same primitive a live mark does.
//
// A steer is a user row carrying `user_kind: "steer"`, so `projectTurns` leaves it
// in its turn's BODY rather than promoting it to the turn header — and the body
// renderer's `case "user"` is therefore reachable for the first time. Rendering it
// as the grey system fallback would make the reader's own mid-turn correction the
// one message in the transcript with no voice.
//
// REAL store, REAL renderer, REAL layout (Browser Mode). Harness shape borrowed
// from messages-steer-note.test.ts: DOM hosts before the imports, the shipped
// transcript stylesheet, `messages.teardownAll()` per case.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeEach } from "vitest";
import type { Message, Session } from "./types.js";

// messages.ts's graph reads the shared DOM registry at module scope, and `byId`
// throws on a missing element, so every host exists before an import resolves.
for (const id of [
  "chat-view",
  "messages-wrap-outer",
  "messages-wrap",
  "messages",
  "scroll-bottom",
  "send-btn",
  "prompt-input",
]) {
  const d = document.createElement(id === "prompt-input" ? "textarea" : "div");
  d.id = id;
  if (id === "scroll-bottom") {
    d.appendChild(document.createElement("span"));
  }
  if (id === "messages-wrap") {
    document.getElementById("messages-wrap-outer")?.appendChild(d);
  } else if (id === "messages") {
    document.getElementById("messages-wrap")?.appendChild(d);
  } else {
    document.body.appendChild(d);
  }
}

import { loadCSS } from "./__test-helpers__/css-rules.js";

const style = document.createElement("style");
style.textContent =
  loadCSS("13-messages.css") +
  `
  #messages-wrap-outer { height: 400px; }
`;
document.head.appendChild(style);

const store = await import("./store.js");
const messages = await import("./messages.js");

messages.mountChatView();

let seq = 0;
function freshID(prefix: string): string {
  return `${prefix}-${String(++seq)}`;
}

function session(id: string, over: Partial<Session> = {}): Session {
  return {
    id,
    name: id,
    messages: [],
    message_count: 0,
    has_more: false,
    thinking: false,
    working_label: "",
    usage: { context_size: 0 },
    ...over,
  } as unknown as Session;
}

function user(id: string, content: string): Message {
  return { id, role: "user", ts: 1, content } as Message;
}

/** The persisted shape the replay projection produces for a `steer-` row. */
function steer(id: string, content: string): Message {
  return { id, role: "user", ts: 2, content, user_kind: "steer" } as unknown as Message;
}

function assistant(id: string, content: string, ts = 3): Message {
  return {
    id,
    role: "assistant",
    ts,
    content,
    blocks: [{ type: "text", text: content }],
    turn_outcome: "completed",
  } as unknown as Message;
}

/** One microtask: the store's per-chat coalescer flushes, and the flush paints. */
async function flushed(): Promise<void> {
  await Promise.resolve();
}

function viewOf(chatID: string): HTMLElement {
  const el = messages.transcriptViewFor(chatID);
  if (el === null) {
    throw new Error(`no resident view for ${chatID}`);
  }
  return el;
}

/** Mount `msgs` as `chatID`'s whole transcript and paint. */
async function paint(chatID: string, msgs: Message[]): Promise<HTMLElement> {
  store.setSessions([session(chatID, { messages: msgs, message_count: msgs.length })]);
  store.setActive(chatID);
  store.bumpMessages(chatID, "load");
  await flushed();
  return viewOf(chatID);
}

beforeEach(() => {
  // The multiplexer's registry persists at module scope, so an earlier case's
  // parked view would otherwise count against this one's LRU budget.
  messages.teardownAll();
  store.setSessions([]);
  store.setActive("");
});

describe("a persisted steer renders as a read steer note", () => {
  it("mounts .steer-note with data-origin=user, data-state=read and no restore button", async () => {
    const c = freshID("c-steer");
    const view = await paint(c, [
      user(`${c}-u`, "go"),
      steer(`${c}-s`, "use tabs"),
      assistant(`${c}-a`, "done"),
    ]);

    const found = [...view.querySelectorAll<HTMLElement>(".steer-note")];
    expect(found).toHaveLength(1);
    const note = found[0];
    expect(note?.dataset["origin"]).toBe("user");
    expect(note?.dataset["state"]).toBe("read");
    // `dropped` is false for a read steer, so the restore button is DEAD code on
    // this path — hence `onRestore` and `ack` are omitted entirely rather than
    // passed as undefined (exactOptionalPropertyTypes is on).
    expect(note?.querySelector(".steer-note-restore")).toBeNull();
    expect(note?.querySelector(".steer-note-ack")).toBeNull();
    expect(note?.querySelector(".steer-note-text")?.textContent).toBe("use tabs");
  });

  it("does not render the steer as a grey system row", async () => {
    const c = freshID("c-steer-not-system");
    const view = await paint(c, [
      user(`${c}-u`, "go"),
      steer(`${c}-s`, "use tabs"),
      assistant(`${c}-a`, "done"),
    ]);

    expect(view.querySelector(".message.system")).toBeNull();
  });

  it("keeps the steer inside the prompt's turn rather than opening one", async () => {
    const c = freshID("c-steer-one-turn");
    const view = await paint(c, [
      user(`${c}-u`, "go"),
      steer(`${c}-s`, "use tabs"),
      assistant(`${c}-a`, "done"),
    ]);

    expect(view.querySelectorAll(".turn")).toHaveLength(1);
    // The PROMPT heads the turn; the steer is body content beside the reply.
    const heads = [...view.querySelectorAll<HTMLElement>(".turn-req-text")].map(
      (e) => e.textContent,
    );
    expect(heads).toEqual(["go"]);
  });
});

// A plain user row cannot reach the body renderer at all: `projectTurns` promotes
// every PROMPT to its turn's header, which is exactly the distinction `case "user"`
// now branches on. So the reachable form of "a plain user row is not a steer note"
// is this control — same content, no `user_kind`, and it heads a turn of its own
// with no note anywhere in the transcript.
describe("a plain user row is a turn header, not a steer note", () => {
  it("opens its own turn and renders no steer note", async () => {
    const c = freshID("c-plain");
    const view = await paint(c, [
      user(`${c}-u1`, "go"),
      assistant(`${c}-a1`, "done", 2),
      user(`${c}-u2`, "use tabs"),
      assistant(`${c}-a2`, "done again", 4),
    ]);

    expect(view.querySelector(".steer-note")).toBeNull();
    expect(view.querySelectorAll(".turn")).toHaveLength(2);
    const heads = [...view.querySelectorAll<HTMLElement>(".turn-req-text")].map(
      (e) => e.textContent,
    );
    expect(heads).toEqual(["go", "use tabs"]);
  });
});
