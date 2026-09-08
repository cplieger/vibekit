// TEMPORARY PROBE — delete before finishing.
import { describe, it, expect, vi, beforeEach } from "vitest";

for (const id of [
  "messages",
  "messages-wrap",
  "messages-wrap-outer",
  "chat-view",
  "scroll-bottom",
]) {
  const d = document.createElement("div");
  d.id = id;
  document.body.appendChild(d);
}

vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));
vi.mock("./actions/messages.js", () => ({
  copyClipboard: { dispatch: () => Promise.resolve() },
  explainError: { dispatch: () => Promise.resolve(null) },
}));
vi.mock("./api-client.js", async () => ({
  ...(await vi.importActual<Record<string, unknown>>("./api-client.js")),
  apiGet: vi.fn(() => Promise.resolve(null)),
}));

const { mountChatView, activeTranscriptView, mountTurnBody } = await import("./messages.js");
const { setSessions, setActive, bumpMessages } = await import("./store.js");
const { setTurnOpen, resetFoldState } = await import("./fold-state.js");
const { RESIDENT_BLOCKS } = await import("./block-window.js");
const { KEY_ATTR } = await import("./reconcile.js");
const { resetTurnRail } = await import("./turn-rail.js");

const messagesEl = document.getElementById("messages") as HTMLElement;

interface Msg {
  id: string;
  role: string;
  ts: number;
  content?: string;
  blocks?: unknown[];
  tool_calls?: unknown[];
}

function user(id: string, text = `prompt ${id}`): Msg {
  return { id, role: "user", ts: 1, content: text };
}

function heavyTurn(id: string, blocks: number): Msg[] {
  const rows = Math.ceil(blocks / 8);
  const out: Msg[] = [user(id, `prompt ${id}`)];
  for (let r = 0; r < rows; r++) {
    out.push({
      id: `${id}-a${String(r)}`,
      role: "assistant",
      ts: 2,
      content: "",
      blocks: Array.from({ length: 8 }, (_, b) => ({
        type: "text",
        text: `chunk ${String(r)}.${String(b)}`,
      })),
    });
  }
  return out;
}

function toolTurns(n: number): Msg[] {
  const out: Msg[] = [];
  for (let i = 1; i <= n; i++) {
    const tc = `tc${String(i)}`;
    out.push(user(`u${String(i)}`), {
      id: `a${String(i)}`,
      role: "assistant",
      ts: 2,
      content: `reply u${String(i)}`,
      blocks: [
        { type: "tool_use", tool_call_id: tc },
        { type: "text", text: `reply u${String(i)}` },
      ],
      tool_calls: [{ id: tc, title: "Read file", kind: "read", status: "completed" }],
    });
  }
  return out;
}

function activate(chatID: string, messages: Msg[], thinking = false): void {
  setSessions([
    {
      id: chatID,
      name: "c",
      model: "",
      acp_session_id: "",
      current_mode_id: "",
      supervised_mode: false,
      effort: "",
      effort_levels: [],
      effort_active: "",
      usage: { context_size: 0 },
      message_count: messages.length,
      messages,
      has_more: false,
      thinking,
      working_label: "Thinking",
    },
  ] as never);
  setActive(chatID);
  bumpMessages(chatID);
}

function card(turnID: string): HTMLElement {
  const root = activeTranscriptView() ?? messagesEl;
  for (const child of root.children) {
    if (child.getAttribute(KEY_ATTR) === turnID) {
      return child as HTMLElement;
    }
  }
  throw new Error(`no card for turn ${turnID}`);
}

beforeEach(() => {
  mountChatView();
  localStorage.clear();
  resetFoldState();
  resetTurnRail();
  setSessions([] as never);
  setActive("");
});

describe("probe", () => {
  it("measures the empty open turn's spacer", () => {
    const id = "c-probe-1";
    setTurnOpen(id, "old", true);
    activate(id, [...heavyTurn("old", 400), ...heavyTurn("big", RESIDENT_BLOCKS + 64)]);
    const c = card("old");
    const body = c.querySelector<HTMLElement>(":scope > .turn-body");
    const keys = [...(body?.querySelectorAll(`:scope > [${KEY_ATTR}]`) ?? [])].map((e) =>
      e.getAttribute(KEY_ATTR),
    );
    const spacer = body?.querySelector<HTMLElement>(":scope > .turn-space");
    // eslint-disable-next-line no-console
    console.log("PROBE-1", {
      folded: c.hasAttribute("data-folded"),
      bodyless: c.classList.contains("is-bodyless"),
      keys,
      spacerStyle: spacer?.style.blockSize,
      cardHeight: c.offsetHeight,
      bodyHeight: body?.offsetHeight,
    });
    expect(true).toBe(true);
  });

  it("measures the pin expiry collapse after a reader expands a far turn", async () => {
    const id = "c-probe-2";
    activate(id, [...heavyTurn("old", 400), ...heavyTurn("big", RESIDENT_BLOCKS + 64)]);
    // The reader clicks the fold toggle on the stub: this is what the toggle does.
    setTurnOpen(id, "old", true);
    await mountTurnBody(id, "old", 0);
    bumpMessages(id);
    await new Promise((r) => setTimeout(r, 30));
    const before = card("old");
    const bodyBefore = before.querySelector<HTMLElement>(":scope > .turn-body");
    // eslint-disable-next-line no-console
    console.log("PROBE-2 within pin", {
      folded: before.hasAttribute("data-folded"),
      rows: bodyBefore?.querySelectorAll(":scope > .msg-wrap").length,
      spacer: bodyBefore?.querySelector<HTMLElement>(":scope > .turn-space")?.style.blockSize,
      cardHeight: before.offsetHeight,
    });
    // Wait out PIN_GOAL_MS, then repaint.
    await new Promise((r) => setTimeout(r, 2100));
    bumpMessages(id);
    await new Promise((r) => setTimeout(r, 60));
    const after = card("old");
    const bodyAfter = after.querySelector<HTMLElement>(":scope > .turn-body");
    // eslint-disable-next-line no-console
    console.log("PROBE-2 after expiry", {
      folded: after.hasAttribute("data-folded"),
      bodyless: after.classList.contains("is-bodyless"),
      rows: bodyAfter?.querySelectorAll(":scope > .msg-wrap").length,
      keys: [...(bodyAfter?.querySelectorAll(`:scope > [${KEY_ATTR}]`) ?? [])].map((e) =>
        e.getAttribute(KEY_ATTR),
      ),
      spacer: bodyAfter?.querySelector<HTMLElement>(":scope > .turn-space")?.style.blockSize,
      cardHeight: after.offsetHeight,
    });
    expect(true).toBe(true);
  }, 20000);

  it("measures the tiny existing fixture", () => {
    const id = "c-probe-3";
    setTurnOpen(id, "u1", true);
    activate(id, [...toolTurns(1), ...heavyTurn("big", RESIDENT_BLOCKS + 64)]);
    const c = card("u1");
    const body = c.querySelector<HTMLElement>(":scope > .turn-body");
    // eslint-disable-next-line no-console
    console.log("PROBE-3", {
      spacer: body?.querySelector<HTMLElement>(":scope > .turn-space")?.style.blockSize,
      cardHeight: c.offsetHeight,
    });
    expect(true).toBe(true);
  });
});
