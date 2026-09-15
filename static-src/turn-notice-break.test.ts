// AN INTERRUPTED TURN'S REASON IS THE FRAMED PROSE UNDER ITS OWN DIVIDER.
//
// Two rows say a turn broke, and each owns half of it: the body's `.boundary`
// divider names the KIND ("Turn interrupted") and the card-level `.turn-notice`
// carries the server's own PROSE ("ACP bridge exited"). That ownership split is
// pinned elsewhere (messages-events.ts's `interrupted` entry, `turnFailureText`);
// what is pinned HERE is the geometry it leaves behind, and the geometry has been
// reported wrong twice.
//
// FIRST REPORT, the spacing: "then a large gap and then the rror 'ACP bridge
// exited' with 0 padding vs the footing. please copy what we did for the
// compaction banner". Answered by framing the reason and trimming the divider's
// trailing margin.
//
// SECOND REPORT (2026-09-12), the reason this file's claim changed: the frame
// opened TWICE. Expected
//
//     --------- turn interrupted --------------
//     error message details
//     ------------------------------
//
// and rendered
//
//     -------- turn interrupted ----------------
//     ----------------------
//     error message details
//
// Three rules drew four lines around one sentence — `.boundary`'s own dashed rule
// flanking its label, `.turn-notice`'s `border-block-start`, and the footer's
// solid seam — so the label sat above a bare second rule and belonged to neither
// half of its own break. `.turn-header + .turn-notice` had been avoiding exactly
// that doubling one element over since the notice existed.
//
// THE CLAIM NOW, and the three parts pull against each other, which is why all
// three are here: the DIVIDER is the frame's top edge (its dashed rule, at the
// compaction break's weight and ink), the FOOTER BAND's own edge is the bottom one
// — a fill change rather than a rule since the card's two internal seams were
// deleted (29-turns.css's file header) — and the reason sits between them at a
// SYMMETRIC inset. A test for "no second rule" alone passes for a reason pressed
// against its own divider; a test for the inset alone passes for a break still
// opening twice; and a test for the bottom edge that read only the footer's
// `border-top` passed for a card whose band had stopped painting at all.
//
// Real layout, because every claim is a distance. `.boundary` carries
// `vk-slide-up … backwards`, whose `from` keyframe is `translateY(6px)`, so its
// RECT is 6px below its layout box until the animation has progressed — measured,
// and it is why the divider's animation is stopped in the harness rather than
// measured around (`vibekit-client.md` records the same 6px trap).
import { afterAll, beforeAll, describe, expect, it } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { buildEvent } from "./messages-events.js";
import type { Message } from "./types.js";

/** `--sp-2`, the compaction head's own inset and the notice's — so it is what
 *  "framed the way the compaction break is framed" measures against. */
const INSET_PX = 8;
/** `--sp-3` (01-tokens.css), `.turn-body`'s `row-gap` AND its padding. The air a
 *  break mid-body still keeps below it is larger than this; the break that ENDS a
 *  framed body keeps none. */
const ROW_GAP_PX = 12;

const REASON = "ACP bridge exited";

let style: HTMLStyleElement;
const made: HTMLElement[] = [];

interface Card {
  readonly card: HTMLElement;
  readonly body: HTMLElement;
  readonly boundary: HTMLElement | null;
  readonly notice: HTMLElement;
  readonly footer: HTMLElement;
}

interface CardOpts {
  /** Whether the body ENDS with the interrupted divider, which is what opens the
   *  frame. False builds a body whose last row is ordinary prose — the turn whose
   *  reason came from the carrier rather than from an event row, and this file's
   *  differential control. */
  readonly dividerLast?: boolean;
  /** Fold the card, which puts `.turn-face` between the body and the notice. */
  readonly folded?: boolean;
}

function interruptedEvent(content: string): Message {
  return {
    id: "m-interrupted",
    role: "event",
    content,
    event_kind: "interrupted",
    ts: 0,
  } as unknown as Message;
}

function prose(text: string): HTMLElement {
  const row = document.createElement("div");
  row.className = "msg-row";
  const msg = document.createElement("div");
  msg.className = "message assistant";
  msg.textContent = text;
  row.append(msg);
  return row;
}

/** An interrupted turn's card, in `buildTurn`'s own child order: header, body,
 *  then the FACE (folded only), then the card-level notice, then the ledger
 *  footer. The divider comes from the real builder so its classes and label are
 *  the shipped ones; the notice is assembled the way `syncTurnNotice` assembles
 *  it, and mounted after the face the way `mountTurn` mounts it. */
function interruptedCard({ dividerLast = true, folded = false }: CardOpts = {}): Card {
  const card = document.createElement("div");
  card.className = "turn";
  if (folded) {
    card.setAttribute("data-folded", "");
  }

  const header = document.createElement("div");
  header.className = "turn-header";
  const req = document.createElement("div");
  req.className = "turn-req-text";
  req.textContent = "run the build";
  header.append(req);

  const body = document.createElement("div");
  body.className = "turn-body";
  body.append(prose("Building."));
  let boundary: HTMLElement | null = null;
  if (dividerLast) {
    boundary = buildEvent(interruptedEvent(REASON));
    if (boundary === null) {
      throw new Error("buildEvent produced no divider for an interrupted event");
    }
    // See the header note: the entry animation's backwards fill offsets the rect
    // by 6px. Stopping it changes no layout — the keyframes touch opacity and
    // transform.
    boundary.style.animation = "none";
    body.append(boundary);
  }

  const notice = document.createElement("div");
  notice.className = "turn-notice";
  notice.dataset["severity"] = "broken";
  notice.dataset["outcome"] = "interrupted";
  notice.setAttribute("role", "status");
  notice.textContent = REASON;

  const footer = document.createElement("div");
  footer.className = "turn-footer";
  footer.dataset["severity"] = "broken";
  const ledger = document.createElement("button");
  ledger.type = "button";
  ledger.className = "turn-ledger-summary";
  ledger.textContent = "2 cmds";
  footer.append(ledger);

  card.append(header, body);
  if (folded) {
    const face = document.createElement("div");
    face.className = "turn-face";
    face.append(prose("Building."));
    card.append(face);
  }
  card.append(notice, footer);
  document.body.replaceChildren(card);
  made.push(card);
  return { card, body, boundary, notice, footer };
}

/** A compaction break, from the same builder, as the reference frame: the skin the
 *  reason is meant to have copied. */
function compactionBreak(): HTMLElement {
  const node = buildEvent({
    id: "m-compacted",
    role: "event",
    content: "a summary",
    event_kind: "compacted",
    ts: 0,
  } as unknown as Message);
  if (node === null) {
    throw new Error("buildEvent produced no compaction break");
  }
  document.body.append(node);
  made.push(node);
  return node;
}

/** Where the INK is, not where the box is: the notice's content is one text node,
 *  so a Range over it is the only way to say how much air sits above and below the
 *  sentence a reader sees. */
function textRect(el: HTMLElement): DOMRect {
  const range = document.createRange();
  range.selectNodeContents(el);
  return range.getBoundingClientRect();
}

beforeAll(() => {
  style = mountAppCSS();
});

afterAll(() => {
  style.remove();
  for (const el of made.splice(0)) {
    el.remove();
  }
});

describe("an interrupted turn's reason", () => {
  it("opens its frame with the divider's own rule and draws no second one", () => {
    const { boundary, notice, footer } = interruptedCard();
    if (boundary === null) {
      throw new Error("no divider");
    }
    const frame = getComputedStyle(compactionBreak());

    // THE TOP EDGE is the divider's own dashed rule, which `.boundary` draws as
    // the `::before`/`::after` flanking its label. Read as a computed style rather
    // than assumed, because it is the edge the notice is now allowed to omit.
    const lead = getComputedStyle(boundary, "::before");
    expect(lead.borderTopStyle, "the divider's rule opens the frame").toBe(frame.borderTopStyle);
    expect(lead.borderTopWidth, "at the compaction break's weight").toBe(frame.borderTopWidth);
    expect(lead.borderTopColor, "in the same ink").toBe(frame.borderTopColor);

    // And the notice adds NOTHING to it. This is the second report: a rule here is
    // a second opening edge one row below the label that names the break.
    expect(
      getComputedStyle(notice).borderTopWidth,
      "the reason draws no rule of its own under that divider",
    ).toBe("0px");

    // THE BOTTOM EDGE is the footer BAND, and it is a fill change rather than a
    // rule: the card draws no internal seam any more, so what closes the frame is
    // the tint step the deleted `border-top` used to sit on. Read as two
    // backgrounds that DIFFER rather than as a colour literal, so a token retune
    // moves with the assertion — and paired with the width, because `0px` alone
    // passes for a footer that stopped painting a band at all, which is the shape
    // that would reopen the "floating red prose" report this file exists for.
    const trailing = getComputedStyle(footer);
    expect(trailing.borderTopWidth, "the band closes the frame, so it draws no rule").toBe("0px");
    expect(trailing.backgroundColor, "and the band is what the reader sees").not.toBe(
      getComputedStyle(notice).backgroundColor,
    );
  });

  it("insets its text equally inside that frame", () => {
    // The "0 padding vs the footing" half, now measured across the WHOLE frame
    // rather than inside the notice's own box: the top edge is the divider and the
    // bottom edge is where the footer band starts, so the two insets are the air
    // below the divider's box and the air above the band's. Unaffected by the
    // seam's deletion — the footer's `border-top` was inside its own border box,
    // so removing it left that box's top edge exactly where it was.
    const { boundary, notice, footer } = interruptedCard();
    if (boundary === null) {
      throw new Error("no divider");
    }
    const ink = textRect(notice);
    const above = ink.top - boundary.getBoundingClientRect().bottom;
    const below = footer.getBoundingClientRect().top - ink.bottom;
    expect(above, "the air between the divider and the sentence").toBeCloseTo(INSET_PX, 0);
    // WITHIN 1px, not equal: a Range's rect is the INK box, and this font's
    // ascent/descent split it 1px off the line box's centre — measured 8 above and
    // 9 below on an inset that is symmetric by declaration. Tightening this to
    // equality would pin a font metric.
    expect(
      Math.abs(below - above),
      "and the air between the sentence and the ledger",
    ).toBeLessThanOrEqual(1);
  });

  it("keeps its own rule when no divider opened a frame above it", () => {
    // THE DIFFERENTIAL, and it is required rather than thorough: `0px` is also
    // what a notice with no frame rule at all reports, so the first case on its
    // own passes for a reason that lost its top edge on every card. This is the
    // turn whose reason came from the carrier rather than from an event row, so
    // nothing above the notice draws a line and the notice draws its own.
    const { notice } = interruptedCard({ dividerLast: false });
    expect(getComputedStyle(notice).borderTopWidth, "one edge, drawn by the notice").toBe("1px");
  });

  it("keeps its own rule on a FOLDED card, where the face breaks the adjacency", () => {
    // The hidden body is still in the DOM at `block-size: 0` with
    // `content-visibility: hidden`, so its divider is invisible while its element
    // is not — and what keeps the frame rules off the notice is `.turn-face`
    // sitting between: `syncTurnFace` anchors on the notice, so the face lands
    // above it whichever of the two mounted first. The rule is load-bearing there
    // rather than merely retained, and that is the half the seam deletion changed:
    // the face's fill IS the body's, so nothing but this rule marks that edge.
    // The ORDER itself is not asserted here — this harness builds it, so an
    // assertion on it would be reading back its own fixture. It is pinned against
    // the real paint in turn-reason-once.test.ts, which mounts a folded broken
    // card through `mountChatView`.
    const { notice } = interruptedCard({ folded: true });
    expect(getComputedStyle(notice).borderTopWidth, "the notice draws the header's seam").toBe(
      "1px",
    );
  });

  it("leaves a divider that is NOT the body's last row alone", () => {
    // The control for the trailing-margin trim AND for the body's trailing
    // padding. Without it the frame cases pass just as well for a rule that
    // flattened every break's spacing in the body, which is the air those margins
    // exist to give.
    const { card, boundary } = interruptedCard();
    if (boundary === null) {
      throw new Error("no divider");
    }
    const body = card.querySelector<HTMLElement>(".turn-body");
    if (body === null) {
      throw new Error("no body");
    }
    const after = prose("Retrying.");
    body.append(after);
    const gap = after.getBoundingClientRect().top - boundary.getBoundingClientRect().bottom;
    expect(gap, "a break mid-body keeps its own trailing air").toBeGreaterThan(ROW_GAP_PX);
  });

  it("withdraws its rule against the header band on a card with no body", () => {
    // A tier-3 stub whose turn produced no prose puts the notice straight under the
    // header, and a tinted band carries that edge with its fill — the same call
    // `.turn-face` makes against the same band. It withdrew for the opposite reason
    // until the seams were deleted (the header drew a rule of its own and two 1px
    // lines a pixel apart is the doubling this file is named for), so the assertion
    // is unchanged and its warrant is not. DIFFERENTIAL against the same no-divider
    // card the third case reads, for that case's reason.
    const bodied = interruptedCard({ dividerLast: false });
    expect(
      getComputedStyle(bodied.notice).borderTopWidth,
      "premise: a notice with no divider above it DOES carry the rule",
    ).toBe("1px");

    const { card } = interruptedCard({ dividerLast: false });
    card.querySelector(".turn-body")?.remove();
    const notice = card.querySelector<HTMLElement>(".turn-notice");
    if (notice === null) {
      throw new Error("no notice");
    }
    expect(getComputedStyle(notice).borderTopWidth, "one rule, not two").toBe("0px");
  });
});
