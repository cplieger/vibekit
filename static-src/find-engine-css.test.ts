// The find walker against the SHIPPED stylesheet. find-in-chat.test.ts builds its
// transcript DOM with no CSS at all, which is the right shape for the navigation
// criteria and also why a walker that pruned every assistant reply shipped:
// `display: contents` measures as a plain block there.
import { describe, it, expect, beforeAll, afterAll, beforeEach, afterEach, vi } from "vitest";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { FindEngine } from "./find-engine.js";
import type * as ModFindInChat from "./find-in-chat.js";

// The find bar's own box needs the REAL overlay under the REAL stylesheet, so
// find-in-chat.ts is imported here — with the same mock set its own suite uses,
// for the same reasons (scroll.ts self-initialises against DOM this file does not
// build, and the block dispatcher's graph reaches through it).
vi.mock("./scroll.js", () => ({
  jumpTo: vi.fn(),
  onTranscriptMutate: vi.fn(() => () => undefined),
}));
vi.mock("./chat-search.js", { spy: true });
vi.mock("./store.js", { spy: true });
vi.mock("./store-load.js", { spy: true });
vi.mock("./run-view.js", () => ({ openRunView: vi.fn() }));
vi.mock("./subagent-view.js", () => ({ openSubagentView: vi.fn() }));
vi.mock("./messages-blocks.js", () => ({ blockElement: vi.fn() }));

let styleEl: HTMLStyleElement;
let host: HTMLElement;

beforeAll(() => {
  styleEl = mountAppCSS();
  host = document.createElement("div");
  // IN VIEW, at a real width. `.msg-row` carries `content-visibility: auto`, so an
  // off-screen fixture would be pruned for a reason this file is not about.
  host.style.cssText = "inline-size:640px;";
  document.body.prepend(host);
});

afterAll(() => {
  styleEl?.remove();
  host?.remove();
});

/** One assistant message as `buildBody` seats it: the boxless block region, a row
 *  inside it, the prose bubble inside that. */
function assistantReply(text: string): HTMLElement {
  host.innerHTML =
    `<div class="msg-wrap msg-wrap-assistant">` +
    `<div class="assistant-blocks">` +
    `<div class="msg-row"><div class="message assistant">${text}</div></div>` +
    `</div></div>`;
  return host.firstElementChild as HTMLElement;
}

/** One rendered frame: `content-visibility: auto` relevance is answered from the
 *  last lifecycle update, so a walk in the mount's own tick prunes a row that is
 *  not yet relevant. `navigateToHit` waits the same frame out with `nextRender`. */
function rendered(): Promise<void> {
  return new Promise((resolve) => {
    requestAnimationFrame(() => {
      requestAnimationFrame(() => {
        resolve();
      });
    });
  });
}

describe("the walker under the shipped stylesheet", () => {
  it("marks prose inside the boxless assistant block region", async () => {
    // The defect: `.assistant-blocks { display: contents }` answers
    // checkVisibility FALSE, the walker pruned the whole subtree at it, and search
    // could select a block and say why but never place a <mark> in a reply.
    const wrap = assistantReply("the retry backoff is documented TODO here");
    await rendered();
    expect(new FindEngine(wrap).search("TODO")).toBe(1);
  });

  it("puts the mark in the prose bubble itself", async () => {
    const wrap = assistantReply("the retry backoff is documented TODO here");
    await rendered();
    new FindEngine(wrap).search("TODO");
    const mark = wrap.querySelector("mark");
    expect(mark?.parentElement?.className).toBe("message assistant");
  });

  // The fixture's own premise, so a stylesheet change that quietly gives the
  // region a box turns the test above into one that cannot fail rather than
  // leaving it green for the wrong reason.
  it("is measuring a region Chromium reports as invisible", () => {
    const wrap = assistantReply("prose");
    const blocks = wrap.querySelector(".assistant-blocks") as HTMLElement;
    expect({
      display: getComputedStyle(blocks).display,
      // find-engine.ts's own option set.
      visible: blocks.checkVisibility({
        contentVisibilityAuto: true,
        visibilityProperty: true,
        opacityProperty: false,
      }),
    }).toEqual({ display: "contents", visible: false });
  });

  it("still prunes a subtree that is boxless because it is HIDDEN", async () => {
    // The other half of the contract: neither of these has client rects either, so
    // an implementation keyed on the absence of a box rather than on the computed
    // `display` passes the cases above and fails this one.
    host.innerHTML =
      `<div class="assistant-blocks">` +
      `<div style="display:none"><div class="message">display TODO</div></div>` +
      `<div style="content-visibility:hidden"><div class="message">skipped TODO</div></div>` +
      `<div class="msg-row"><div class="message">shown TODO</div></div>` +
      `</div>`;
    await rendered();
    expect(new FindEngine(host).search("TODO")).toBe(1);
  });

  it("still prunes an off-screen row that content-visibility SKIPPED", async () => {
    // The `auto` variant, which is the one a long transcript's cost rests on: the
    // walker must stay out of every card below the fold, and a skipped row is
    // boxless to `checkVisibility` exactly as the region above it is.
    host.innerHTML =
      `<div class="assistant-blocks">` +
      `<div class="msg-row"><div class="message">shown TODO</div></div>` +
      `<div style="block-size:2400px"></div>` +
      `<div class="msg-row"><div class="message">skipped TODO</div></div>` +
      `</div>`;
    await rendered();
    expect(new FindEngine(host).search("TODO")).toBe(1);
  });
});

// ---------------------------------------------------------------------------
// The find bar's own geometry. The note is a SECOND LINE of the bar, mounted at
// all times because it is an aria-live region, so what keeps the resting bar one
// row is the note's own zero-height treatment — and that treatment reaches it
// only through the spec's `noteClass`, since the shell defaults the class to
// `search-note`, which no stylesheet defines.
// ---------------------------------------------------------------------------

let bootSeq = 0;

describe("the transcript find bar under the shipped stylesheet", () => {
  let fixture: HTMLElement;

  beforeEach(async () => {
    bootSeq++;
    fixture = document.createElement("div");
    fixture.innerHTML =
      `<div id="chat-view" data-tab-view>` +
      `<button type="button" id="find-btn" class="icon-btn" aria-pressed="false"></button>` +
      `<div id="messages-wrap-outer">` +
      `<div id="messages-wrap"><div id="messages"></div></div>` +
      `</div>` +
      `<textarea id="prompt-input"></textarea>` +
      `</div>`;
    document.body.appendChild(fixture);
    const mod = (await import(
      /* @vite-ignore */ `./find-in-chat.ts?css-boot=${bootSeq}`
    )) as typeof ModFindInChat;
    mod.handleFindHotkey(
      new KeyboardEvent("keydown", { key: "f", ctrlKey: true, cancelable: true }),
    );
    await rendered();
  });

  afterEach(() => {
    fixture.remove();
  });

  it("keeps the resting bar one row, with the empty note costing no height", () => {
    const region = document.getElementById("chat-find") as HTMLElement;
    const row = region.querySelector<HTMLElement>(".chat-find-row") as HTMLElement;
    const note = document.getElementById("chat-find-note") as HTMLElement;
    // Read before the removal below: a detached element's computed style answers
    // empty strings for everything.
    const noteStyle = getComputedStyle(note);
    const noteOpacity = noteStyle.opacity;
    const noteOverflow = noteStyle.overflowY;
    const noteHeight = note.getBoundingClientRect().height;
    const barHeight = region.getBoundingClientRect().height;
    const rowHeight = row.getBoundingClientRect().height;
    // The direct statement of "costs no height", measured rather than derived
    // from the box's padding and border: the bar is the same height it would be
    // if the note were not in the layout at all.
    note.remove();
    const withoutNote = region.getBoundingClientRect().height;
    expect({
      noteHeight,
      barUnchanged: barHeight === withoutNote,
      // One ROW: nothing else in the column has a box, so the controls row is
      // taller than nothing and shorter than the bar's own padded box.
      rowFitsOnce: rowHeight > 0 && rowHeight < barHeight,
      // MOUNTED but silent, which is what a live region wants — and both
      // declarations belong to `.chat-find-note`, so a note left on the shell's
      // `search-note` default (a class no stylesheet defines) is a visible,
      // unclipped region that grows the bar the moment text arrives.
      noteOpacity,
      noteOverflow,
    }).toEqual({
      noteHeight: 0,
      barUnchanged: true,
      rowFitsOnce: true,
      noteOpacity: "0",
      noteOverflow: "clip",
    });
  });
});
