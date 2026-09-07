import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";

// ---------------------------------------------------------------------------
// A metering unit label wraps inside the context pill's card.
//
// The label is `MeteringItem.UnitPlural` — whatever kiro-cli reports for a
// usage dimension, with nothing bounding its length — and `.pill-metering`'s
// own comment has always said such a label wraps. It could not: the card sets
// `white-space: nowrap`, and an underscored identifier carries no wrap
// opportunity of its own, so BOTH `white-space: normal` and
// `overflow-wrap: anywhere` are needed and neither works alone.
//
// The assertions are the two halves of one outcome, because a box can also stop
// overflowing by clipping: the card stays within the width it was given, AND
// the label got taller, which is only true if the text wrapped.
//
// The phone cap that made this visible (`max-inline-size` under
// `@media (width <= 48rem)`) is not what is exercised here — the mechanism is
// min-content sizing, so a shrink-to-fit card in a narrow host reproduces it at
// every width. Measured in Chromium: 260px card / 4-line label as shipped, and
// 301px card / 1-line label with either declaration reverted.
// ---------------------------------------------------------------------------

/** Long enough to exceed the host, with no break opportunity anywhere in it. */
const UNBREAKABLE_UNIT = "cache_read_input_tokens_including_prompt_prefix";

/** Narrower than the label's max-content width, wider than the card's
 *  `min-width: 14rem`, so the card's width is genuinely the host's to give. */
const HOST_W = 260;

const host = document.createElement("div");
host.style.cssText = `position:fixed;top:-9999px;left:0;inline-size:${String(HOST_W)}px;`;
document.body.appendChild(host);

let style: HTMLStyleElement;

beforeAll(() => {
  style = mountAppCSS();
});

afterAll(() => {
  style.remove();
  host.remove();
});

afterEach(() => {
  host.replaceChildren();
});

/** The metering row as `status.ts` renders it, inside the card that owns the
 *  inherited `nowrap`. Returns the card and the label. */
function mountMeteringRow(unit: string): { card: HTMLElement; label: HTMLElement } {
  const card = document.createElement("span");
  card.className = "pill-expand-content pill-context-content is-open";

  const box = document.createElement("span");
  box.className = "pill-metering";

  const row = document.createElement("span");
  row.className = "pill-metering-row";

  const label = document.createElement("span");
  label.className = "pill-metering-label";
  label.textContent = unit;

  const value = document.createElement("span");
  value.className = "pill-metering-value";
  value.textContent = "1.2M";

  row.append(label, value);
  box.append(row);
  card.append(box);
  host.append(card);
  return { card, label };
}

describe("a metering unit label inside the context pill card", () => {
  it("keeps the card within the width it was given", () => {
    const { card } = mountMeteringRow(UNBREAKABLE_UNIT);

    expect(card.offsetWidth).toBeLessThanOrEqual(HOST_W);
  });

  it("wraps rather than being clipped, so the label grows taller", () => {
    const { label: long } = mountMeteringRow(UNBREAKABLE_UNIT);
    const longHeight = long.getBoundingClientRect().height;

    host.replaceChildren();
    const { label: short } = mountMeteringRow("tokens");
    const oneLine = short.getBoundingClientRect().height;

    expect(oneLine).toBeGreaterThan(0);
    expect(longHeight).toBeGreaterThan(oneLine);
  });
});
