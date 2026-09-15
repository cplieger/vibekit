// THE PAINT INVARIANT'S EXTRAS HALF. Not `fundamentals/turn-footer.test.ts`'s: neither
// extra is a summary field, and each one's control is mounted by THIS module or by
// `messages-turn-actions.ts` into a footer `buildTurnFooter` had already returned.
import { describe, it, expect, vi, beforeAll, afterAll } from "vitest";
import { FRAME_BUDGET_MS } from "./__test-helpers__/frame-budget.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import type { Message, Session } from "./types.js";

// The page's own nesting, from messages-turn-number.test.ts's harness minus pagination.
const outer = document.createElement("div");
outer.id = "messages-wrap-outer";
outer.style.cssText = "position:relative;";
const wrap = document.createElement("div");
wrap.id = "messages-wrap";
wrap.style.cssText = "height:600px;overflow-y:auto;overflow-anchor:none;position:relative;";
const messagesEl = document.createElement("div");
messagesEl.id = "messages";
wrap.appendChild(messagesEl);
outer.appendChild(wrap);
document.body.appendChild(outer);
for (const [id, tag] of [
  ["chat-view", "div"],
  ["scroll-bottom", "button"],
  ["send-btn", "button"],
  ["prompt-input", "textarea"],
] as const) {
  const e = document.createElement(tag);
  e.id = id;
  if (id === "scroll-bottom") {
    e.appendChild(document.createElement("span"));
  }
  document.body.appendChild(e);
}

// The rail's index and pagination are network reads; neither is what these cases test.
vi.mock("./api-client.js", { spy: true });
vi.mock("./store-load.js", () => ({ loadMessages: vi.fn(), loadList: vi.fn() }));

const store = await import("./store.js");
const { observeStamp } = await import("./subject-versions.js");
const messages = await import("./messages.js");

messages.mountChatView();

let bundle: HTMLStyleElement;

beforeAll(() => {
  // The WHOLE assembled cascade: `.turn-actions-more { display: contents }` and its
  // `::details-content` override are what put the buttons inline. No `data-pointer`
  // write: `:root` in 01-tokens.css already declares the FINE values, and the coarse
  // fallback is `@media (width <= 48rem)`, which a 1280px viewport never matches.
  bundle = mountAppCSS();
});

afterAll(() => {
  bundle.remove();
});

/** A turn's trigger. One `ts` for every turn here: these two cases are about which
 *  CONTROL an extra mounts, so the summary fields are deliberately empty and the fact
 *  slot paints nothing. */
function user(id: string): Message {
  return { id, role: "user", ts: 1000, content: `prompt ${id}` } as Message;
}

/** A settled reply carrying `text`. `turn_outcome` is what settles the turn AND what
 *  keeps a text-free reply out of `carriesNothing`, so the empty form still opens a
 *  turn and still grades `completed` rather than `unknown`. */
function reply(id: string, text: string): Message {
  return {
    id,
    role: "assistant",
    ts: 1000,
    content: text,
    blocks: text === "" ? [] : [{ type: "text", text }],
    turn_outcome: "completed",
  } as unknown as Message;
}

let seq = 0;
function nextChat(): string {
  seq += 1;
  return `x${String(seq)}`;
}

async function until(pred: () => boolean, what: string): Promise<void> {
  const deadline = Date.now() + FRAME_BUDGET_MS;
  while (!pred()) {
    if (Date.now() > deadline) {
      throw new Error(`timed out after ${String(FRAME_BUDGET_MS)}ms waiting for ${what}`);
    }
    await new Promise((r) => setTimeout(r, 8));
  }
}

function cards(): HTMLElement[] {
  const root = messages.activeTranscriptView();
  return root === null ? [] : [...root.querySelectorAll<HTMLElement>(":scope > .turn")];
}

async function paint(msgs: Message[]): Promise<HTMLElement[]> {
  const id = nextChat();
  const expected = msgs.filter((m) => m.role === "user").length;
  // A held `chat` version is what marks the window fresh, or the activation refetches
  // and the mocked loader answers nothing.
  observeStamp({ kind: "chat", ref: id, version: "1" });
  store.setSessions([
    {
      id,
      name: id,
      messages: msgs,
      message_count: msgs.length,
      has_more: false,
      residency: "loaded",
      thinking: false,
      working_label: "",
    } as unknown as Session,
  ]);
  store.setActive(id);
  await until(() => cards().length === expected, `${String(expected)} cards for ${id}`);
  // The footer fades in from `opacity: 0` (`@starting-style`), so a control read before
  // that settles is the pre-transition value, not the rest value.
  await until(
    () =>
      [...document.querySelectorAll(".turn-footer")].every(
        (f) => getComputedStyle(f).opacity === "1",
      ),
    "the footers' entry transition to settle",
  );
  return cards();
}

function footerOf(card: HTMLElement): HTMLElement | null {
  return card.querySelector<HTMLElement>(":scope > .turn-footer");
}

/** Whether a CONTROL is genuinely on screen. The field probes' text check is the wrong
 *  observable for a button whose label is `display: none` at this width, so the
 *  equivalent is a painted box plus an accessible name: not `hidden`, visible by the
 *  platform's own answer, a non-zero box, and something to announce.
 *
 *  Opacity IS read, which needs `paint` to have settled the footer's entry
 *  transition first: without it a fully transparent control passes. */
function controlShows(el: HTMLElement | null): boolean {
  if (el === null || el.hidden || !el.checkVisibility({ opacityProperty: true })) {
    return false;
  }
  const box = el.getBoundingClientRect();
  const name = el.getAttribute("aria-label") ?? el.textContent ?? "";
  return box.width > 0 && box.height > 0 && name.trim() !== "";
}

/** The two channels the footer MODULE owns, so a case can state that the extra it is
 *  about earned the footer through its control and not through a fact. */
function footerPaintsText(footer: HTMLElement): boolean {
  const fact = footer.querySelector<HTMLElement>(":scope > .turn-fact");
  const word = footer.querySelector<HTMLElement>(
    ":scope > .turn-ledger-summary > .turn-ledger-text",
  );
  for (const el of [fact, word]) {
    if (el !== null && !el.hidden && el.checkVisibility() && (el.textContent ?? "") !== "") {
      return true;
    }
  }
  return false;
}

describe("a footer earned by an extra mounts that extra's control", () => {
  it("mounts a visible Rewind for a turn earned by `rewindable` alone", async () => {
    // Turn 1 did nothing, so its only reason is that turn 2 gives it a trigger.
    const [first, second] = await paint([
      user("u1"),
      reply("a1", ""),
      user("u2"),
      reply("a2", "reply"),
    ]);
    expect(first).toBeDefined();
    expect(second).toBeDefined();
    const footer = footerOf(first as HTMLElement);
    expect(footer, "the turn earned a footer").not.toBeNull();
    expect(footerPaintsText(footer as HTMLElement), "and it earned it by nothing else").toBe(false);
    expect(
      controlShows((footer as HTMLElement).querySelector<HTMLElement>(":scope > .turn-rewind")),
      "so Rewind is what paints",
    ).toBe(true);
  });

  it("mounts visible turn actions for a turn earned by `settledProse` alone", async () => {
    // Turn 2 is LAST, so no rewind target, and its prose carries no ledger behind it.
    const [, second] = await paint([user("u1"), reply("a1", ""), user("u2"), reply("a2", "reply")]);
    expect(second).toBeDefined();
    const footer = footerOf(second as HTMLElement);
    expect(footer, "the turn earned a footer").not.toBeNull();
    expect(footerPaintsText(footer as HTMLElement), "and it earned it by nothing else").toBe(false);
    expect(
      (footer as HTMLElement).querySelector(":scope > .turn-rewind"),
      "the last turn has no rewind target",
    ).toBeNull();
    const actions = (footer as HTMLElement).querySelector<HTMLElement>(
      ":scope > .turn-actions-buttons",
    );
    expect(controlShows(actions), "so the actions row is what paints").toBe(true);
    // `:not(.turn-action-more)` excludes the phone layout's `…` trigger, which carries
    // the same class and is `display: none` at this width BY DESIGN.
    const buttons = [
      ...(actions?.querySelectorAll<HTMLElement>(".turn-action-btn:not(.turn-action-more)") ?? []),
    ];
    expect(buttons.length, "and it carries real buttons").toBeGreaterThan(0);
    expect(
      buttons.every((b) => controlShows(b)),
      "every one of them visible",
    ).toBe(true);
  });
});
