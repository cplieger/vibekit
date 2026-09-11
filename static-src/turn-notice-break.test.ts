// AN INTERRUPTED TURN'S REASON IS A FRAMED BREAK ADJACENT TO ITS DIVIDER.
//
// Two rows say a turn broke, and each owns half of it: the body's `.boundary`
// divider names the KIND ("Turn interrupted") and the card-level `.turn-notice`
// carries the server's own PROSE ("ACP bridge exited"). That ownership split is
// pinned elsewhere (messages-events.ts's `interrupted` entry, `turnFailureText`);
// what is pinned HERE is the geometry it left behind, which is what was reported:
// "then a large gap and then the rror 'ACP bridge exited' with 0 padding vs the
// footing. please copy what we did for the compaction banner".
//
// Measured before the fix, in this harness, and the two figures are in different
// frames of reference — state which, because an earlier note here mixed them and
// reported one number that was neither. BOX to box, the divider's bottom edge to
// the frame's rule: **28px** (16px of the divider's own trailing margin plus 12px
// of the body's `padding-block-end`), against the 12px `row-gap` every other pair
// of rows in that body sits at. INK to ink, the divider's label to the reason:
// **37.5px**, which is that 28 plus the notice's own 8px inset and a line-box
// fraction. The cases below assert the box distance, because that is the one a
// rule can move; the ink figure is what a reader perceives.
//
// Below it: **9px** of ink to the footer's box, and NO FRAME AT ALL — so the reason
// read as loose prose pressed onto the ledger strip rather than as one of the
// transcript's breaks. That 9px did not move; the frame is what changed.
//
// THREE claims, and they pull against each other, which is why all three are here:
// the reason is FRAMED like the compaction break, the frame's own inset is
// SYMMETRIC (the "0 padding vs the footing" half), and the pair is ADJACENT — one
// body row-gap apart, like any two rows in that body. A test for the frame alone
// passes for a framed break still sitting 36px below its own divider.
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

/** `--sp-3` (01-tokens.css), which is `.turn-body`'s `row-gap` AND its padding —
 *  so it is what "as adjacent as any two rows in this body" measures against. */
const ROW_GAP_PX = 12;
/** `--sp-2`, the compaction head's own inset and the notice's. */
const INSET_PX = 8;

const REASON = "ACP bridge exited";

let style: HTMLStyleElement;
const made: HTMLElement[] = [];

interface Card {
  readonly card: HTMLElement;
  readonly boundary: HTMLElement;
  readonly notice: HTMLElement;
  readonly footer: HTMLElement;
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

/** An interrupted turn's card, in `buildTurn`'s own child order: header, body
 *  (a reply then the divider), the card-level notice, the ledger footer. The
 *  divider comes from the real builder so its classes and label are the shipped
 *  ones; the notice is assembled the way `syncTurnNotice` assembles it. */
function interruptedCard(): Card {
  const card = document.createElement("div");
  card.className = "turn";

  const header = document.createElement("div");
  header.className = "turn-header";
  const req = document.createElement("div");
  req.className = "turn-req-text";
  req.textContent = "run the build";
  header.append(req);

  const body = document.createElement("div");
  body.className = "turn-body";
  const row = document.createElement("div");
  row.className = "msg-row";
  const msg = document.createElement("div");
  msg.className = "message assistant";
  msg.textContent = "Building.";
  row.append(msg);
  const boundary = buildEvent(interruptedEvent(REASON));
  if (boundary === null) {
    throw new Error("buildEvent produced no divider for an interrupted event");
  }
  // See the header note: the entry animation's backwards fill offsets the rect by
  // 6px. Stopping it changes no layout — the keyframes touch opacity and transform.
  boundary.style.animation = "none";
  body.append(row, boundary);

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

  card.append(header, body, notice, footer);
  document.body.replaceChildren(card);
  made.push(card);
  return { card, boundary, notice, footer };
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
  it("is framed like the compaction break", () => {
    const { notice, footer } = interruptedCard();
    const frame = getComputedStyle(compactionBreak());
    const cs = getComputedStyle(notice);
    expect(cs.borderTopStyle, "the reason takes a rule of its own").toBe(frame.borderTopStyle);
    expect(cs.borderTopWidth, "at the same weight").toBe(frame.borderTopWidth);
    expect(cs.borderTopColor, "in the same ink").toBe(frame.borderTopColor);

    // THE FRAME'S SECOND RULE, so "framed" means two edges rather than one. It is
    // the FOOTER's own `border-top` — declaring a second here would double a 1px
    // line — which is why this reads the footer and why the STYLE is allowed to
    // differ: `.compaction` is dashed on both edges, and this one takes the solid
    // rule every turn card already draws between its body and its ledger. Making
    // it dashed would give a failed turn a seam no other turn has.
    const trailing = getComputedStyle(footer);
    expect(trailing.borderTopWidth, "the trailing rule exists, at the same weight").toBe(
      frame.borderTopWidth,
    );
    expect(trailing.borderTopStyle, "and is the card's own solid seam, deliberately").toBe("solid");
  });

  it("insets its text equally above and below", () => {
    // The "0 padding vs the footing" half. The frame's TRAILING rule is the
    // footer's own `border-top`, so the air below the text is the distance to the
    // footer's box — which is what a reader sees as the break's bottom edge.
    const { notice, footer } = interruptedCard();
    const ink = textRect(notice);
    const box = notice.getBoundingClientRect();
    const above = ink.top - box.top - Number.parseFloat(getComputedStyle(notice).borderTopWidth);
    const below = footer.getBoundingClientRect().top - ink.bottom;
    expect(above, "the air between the rule and the sentence").toBeCloseTo(INSET_PX, 0);
    // WITHIN 1px, not equal: a Range's rect is the INK box, and this font's
    // ascent/descent split it 1px off the line box's centre — measured 8 above and
    // 9 below on an inset that is symmetric by declaration. Tightening this to
    // equality would pin a font metric.
    expect(
      Math.abs(below - above),
      "and the air between the sentence and the ledger",
    ).toBeLessThanOrEqual(1);
  });

  it("sits one body row-gap below its own divider", () => {
    // The "large gap" half, measured from the divider's LAYOUT box to the frame's
    // rule: 36.5px before, and the body's own rows are 12px apart.
    const { boundary, notice } = interruptedCard();
    const gap = notice.getBoundingClientRect().top - boundary.getBoundingClientRect().bottom;
    expect(gap, "the divider and the reason are adjacent rows").toBeCloseTo(ROW_GAP_PX, 0);
  });

  it("leaves a divider that is NOT the body's last row alone", () => {
    // The control for the trailing-margin trim. Without it the third case passes
    // just as well for a rule that flattened every break's spacing in the body,
    // which is the air those margins exist to give.
    const { card, boundary } = interruptedCard();
    const body = card.querySelector<HTMLElement>(".turn-body");
    if (body === null) {
      throw new Error("no body");
    }
    const after = document.createElement("div");
    after.className = "msg-row";
    const msg = document.createElement("div");
    msg.className = "message assistant";
    msg.textContent = "Retrying.";
    after.append(msg);
    body.append(after);
    const gap = after.getBoundingClientRect().top - boundary.getBoundingClientRect().bottom;
    expect(gap, "a break mid-body keeps its own trailing air").toBeGreaterThan(ROW_GAP_PX);
  });

  it("does not double the header's rule on a card with no body", () => {
    // A tier-3 stub whose turn produced no prose puts the notice straight under the
    // header, which already draws a rule.
    //
    // DIFFERENTIAL, and that is required rather than thorough: `0px` is also what a
    // notice with no frame rule at all reports, so on its own this case passes for
    // the very defect the first case exists to catch — measured, by deleting
    // `.turn-notice`'s `border-block-start` and watching this stay green. The bodied
    // card in the same page is what makes the zero mean "suppressed here".
    const bodied = interruptedCard();
    expect(
      getComputedStyle(bodied.notice).borderTopWidth,
      "premise: a bodied card's notice DOES carry the rule",
    ).toBe("1px");

    const { card } = interruptedCard();
    card.querySelector(".turn-body")?.remove();
    const notice = card.querySelector<HTMLElement>(".turn-notice");
    if (notice === null) {
      throw new Error("no notice");
    }
    expect(getComputedStyle(notice).borderTopWidth, "one rule, not two").toBe("0px");
  });
});
