// ---------------------------------------------------------------------------
// The status half of the update path, against the REAL tool card.
//
// Its own file rather than a case in `messages-tools.test.ts`, because that file
// replaces `./tool-card.js` wholesale — and every observable here belongs to that
// module: whether the region opened, whether the chevron survived. Asserted
// against a stub they could not fail.
//
// Two behaviours meet on one frame, which is why they are pinned together. A
// terminal `tool_call_update` commonly carries the status AND the output, so
// `applyStatusUpdate` runs against whatever the output region holds at that
// moment — and it reads that region twice, for the Explain-this-error gate and for
// the bare-disclosure predicate. Status is therefore applied LAST.
// ---------------------------------------------------------------------------

import { describe, it, expect, vi } from "vitest";
import type { ToolCall } from "./types.js";

// The signal layer only, so a MOUNTED card is reachable without dragging the chat
// store in. It hands back the snapshot it was given, so the mount's effect sees
// `next === lastApplied` and does not re-enter the update path.
vi.mock("./store-signals.js", () => ({
  toolCallSigKey: vi.fn((chatID: string, toolID: string) => `${chatID}\u0000${toolID}`),
  ensureToolCallSig: vi.fn((_chat: string, _id: string, tc: unknown) => ({ value: tc })),
  clearToolCallSig: vi.fn(),
  // Present-but-undefined so real-ESM linking succeeds: other modules in this
  // graph import these names and no path here calls them.
  blockKey: undefined,
  blockTextSigs: undefined,
  blockThinkingSigs: undefined,
  streamingReasoningSigs: undefined,
  streamingTextSigs: undefined,
  toolCallSigs: undefined,
  ensureStreamingSig: undefined,
  ensureReasoningSig: undefined,
  ensureBlockTextSig: undefined,
  ensureBlockThinkingSig: undefined,
  peekToolCallSig: undefined,
  clearStreamingSig: undefined,
  clearReasoningSig: undefined,
  clearBlockSigsFor: undefined,
  clearAllBlockSigs: undefined,
}));

// The real chrome reaches `scroll.ts`, which resolves the transcript scroller at
// module load and throws on a missing id.
for (const id of ["messages", "messages-wrap", "messages-wrap-outer", "chat-view"]) {
  if (document.getElementById(id) === null) {
    const d = document.createElement("div");
    d.id = id;
    document.body.appendChild(d);
  }
}
const scrollBottom = document.createElement("div");
scrollBottom.id = "scroll-bottom";
scrollBottom.appendChild(document.createElement("span"));
document.body.appendChild(scrollBottom);

const { buildToolCard } = await import("./tool-card.js");
const { buildToolGroupShell, groupBody, refreshGroupHeader, autoCollapseGroup } =
  await import("./tool-group.js");
const { updateToolCall, mountToolCallCard, appendTerminalChunk, disposeAllToolEffects } =
  await import("./messages-tools.js");

/** A live, in-flight call with nothing disclosable yet: no input, no output, and
 *  `other` for the depth 1 that gives it a details region at all. */
function liveCard(id: string): HTMLDivElement {
  const card = buildToolCard({
    id,
    title: "invoke_sub_agent",
    kind: "other",
    status: "in_progress",
    live: true,
  });
  document.body.appendChild(card);
  return card;
}

function frame(id: string, tc: Partial<ToolCall>): ToolCall {
  return { id, title: "invoke_sub_agent", kind: "other", ts: 0, ...tc } as ToolCall;
}

describe("a terminal frame carrying the failure AND its output", () => {
  it("auto-expands the card", () => {
    const card = liveCard("st-open");
    updateToolCall(card, frame("st-open", { status: "failed", output: "exit status 2\n" }), "c1");
    expect(card.querySelector(".tool-disclosure")?.getAttribute("aria-expanded")).toBe("true");
    card.remove();
  });

  it("keeps the chevron", () => {
    const card = liveCard("st-chevron");
    updateToolCall(
      card,
      frame("st-chevron", { status: "failed", output: "exit status 2\n" }),
      "c1",
    );
    expect(card.querySelector(".tool-disclosure")).not.toBeNull();
    card.remove();
  });

  it("offers Explain this error", () => {
    // The ordering pin: on the pre-fix order this frame reached
    // `applyStatusUpdate` with an unpainted region, so the gate saw no output and
    // no button was ever offered unless a later update happened to arrive.
    const card = liveCard("st-explain");
    updateToolCall(
      card,
      frame("st-explain", { status: "failed", output: "exit status 2\n" }),
      "c1",
    );
    expect(card.querySelector(".tool-explain-btn")).not.toBeNull();
    card.remove();
  });
});

describe("a live call that has produced nothing YET", () => {
  it("has no chevron while the region is empty", () => {
    // The wire status is not evidence about the region: both production call sites
    // pass `live: true`, so a REPLAYED in-progress call is indistinguishable from
    // one still filling.
    const card = liveCard("st-inflight-bare");
    expect(card.querySelector(".tool-disclosure")).toBeNull();
    card.remove();
  });

  it("gains one on its first output frame, still in flight", () => {
    const card = liveCard("st-inflight-out");
    updateToolCall(
      card,
      frame("st-inflight-out", { status: "in_progress", output: "step 1 of 3\n" }),
      "c1",
    );
    expect(card.querySelector(".tool-disclosure")).not.toBeNull();
    card.remove();
  });
});

describe("a terminal frame whose output is blank", () => {
  it("paints nothing into the region", () => {
    // One value, three consumers, and the two downstream ones already trimmed: the
    // predicate and the Explain gate both read this region as empty, so a `<pre>`
    // built here was DOM nothing could ever reach.
    const card = liveCard("st-blank-pre");
    updateToolCall(card, frame("st-blank-pre", { status: "completed", output: "   \n  \n" }), "c1");
    expect(card.querySelector(".tool-output pre")).toBeNull();
    card.remove();
  });

  it("offers no Explain button on a failure", () => {
    const card = liveCard("st-blank-explain");
    updateToolCall(card, frame("st-blank-explain", { status: "failed", output: "  \n" }), "c1");
    expect(card.querySelector(".tool-explain-btn")).toBeNull();
    card.remove();
  });
});

describe("a call that failed having produced nothing", () => {
  it("is not force-opened", () => {
    const card = liveCard("st-bare-open");
    updateToolCall(card, frame("st-bare-open", { status: "failed" }), "c1");
    expect(card.querySelector(".tool-details")?.getAttribute("aria-hidden")).toBe("true");
    card.remove();
  });

  it("loses its chevron", () => {
    const card = liveCard("st-bare-chevron");
    updateToolCall(card, frame("st-bare-chevron", { status: "failed" }), "c1");
    expect(card.querySelector(".tool-disclosure")).toBeNull();
    card.remove();
  });

  it("gets the chevron back if output arrives on a later frame", () => {
    const card = liveCard("st-bare-late");
    updateToolCall(card, frame("st-bare-late", { status: "failed" }), "c1");
    expect(card.querySelector(".tool-disclosure")).toBeNull();

    updateToolCall(card, frame("st-bare-late", { output: "late stderr\n" }), "c1");
    expect(card.querySelector(".tool-disclosure")).not.toBeNull();
    card.remove();
  });

  it("gets the chevron back if its terminal writes a chunk afterwards", () => {
    // A terminal's lifecycle is its own: `terminal_output` frames keep arriving
    // until `terminal_exited`, so one can land after the call has already reported
    // failed. Without the refresh those bytes sit in a region nothing can open.
    const tc = frame("st-bare-term", { status: "in_progress", terminal_id: "term-1" });
    const card = mountToolCallCard("c1", tc);
    document.body.appendChild(card);
    updateToolCall(card, frame("st-bare-term", { status: "failed" }), "c1");
    expect(card.querySelector(".tool-disclosure")).toBeNull();

    appendTerminalChunk("term-1", "late stderr\n", [], 0);

    expect(card.querySelector(".tool-disclosure")).not.toBeNull();
    disposeAllToolEffects();
    card.remove();
  });
});

describe("a group whose members ran LIVE and then settled", () => {
  it("folds once the next element supersedes it", () => {
    // The live path end to end, which is the population the transcript-level
    // fixtures miss: they build cards born `completed`, so nothing there ever
    // carries the in-flight marker. Two cards mounted in flight, settled by a
    // terminal frame carrying the SERVER's duration, then superseded.
    const group = buildToolGroupShell();
    const a = liveCard("st-fold-a");
    const b = liveCard("st-fold-b");
    groupBody(group).append(a, b);
    document.body.appendChild(group);
    refreshGroupHeader(group);

    updateToolCall(a, frame("st-fold-a", { status: "completed", duration_ms: 1200 }), "c1");
    updateToolCall(b, frame("st-fold-b", { status: "completed", duration_ms: 3400 }), "c1");
    expect(a.dataset["startMs"]).toBeUndefined();
    expect(b.dataset["startMs"]).toBeUndefined();

    autoCollapseGroup(group);

    expect(group.classList.contains("tool-group-auto-collapsed")).toBe(true);
    expect(group.querySelector(".tool-group-header")?.getAttribute("aria-expanded")).toBe("false");
    group.remove();
  });
});
