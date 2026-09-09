// ---------------------------------------------------------------------------
// Tests for elicitation.ts — the MCP elicitation FORM card in the interaction
// dock. The subject is the string-field control: which native input type a
// JSON-Schema `format` picks, and what that control reads back.
//
// Two facts here a type check cannot reach. `date-time` is the schema spelling
// and `datetime-local` the HTML one, so a swap in either direction is a
// one-token change that renders a bare text box or an invalid type. And a
// `datetime-local` value is `YYYY-MM-DDTHH:mm` — no seconds and NO offset —
// while the schema's `date-time` format is RFC 3339 `date-time`, which requires
// both, so a verbatim read answers a schema with a value invalid against it.
//
// Everything drives the public `buildElicitationCard` and its Submit button,
// because that is the only door to the reader: the control builder is private,
// and `collect` is what turns a control's `filled` flag into a present-or-absent
// key in the answer.
//
// No dialog and no focus-trap mock: the card is a plain subtree in a bottom-bar
// region. Queue and settle-once belong to decision-dock.ts.
// ---------------------------------------------------------------------------

import { describe, it, expect, vi, beforeEach } from "vitest";

import { buildElicitationCard } from "./elicitation.js";
import type { ElicitationNeededPayload, ElicitationPropertySchema } from "./types.js";

type Submit = (action: string, content?: Record<string, unknown>) => void;

/** A form of exactly one property, named `when`, optional unless asked. */
function oneField(schema: ElicitationPropertySchema, required = false): ElicitationNeededPayload {
  return {
    request_id: 1,
    mode: "form",
    message: "Fill this in",
    requested_schema: { properties: { when: schema }, required: required ? ["when"] : [] },
  };
}

function mount(p: ElicitationNeededPayload, onSubmit: Submit): HTMLElement {
  const card = buildElicitationCard(p, onSubmit);
  document.body.replaceChildren(card);
  return card;
}

/** The one rendered `<input>`. */
function field(card: HTMLElement): HTMLInputElement {
  const inp = card.querySelector<HTMLInputElement>("input.elicitation-input");
  if (inp === null) {
    throw new Error("no elicitation input rendered");
  }
  return inp;
}

function submitBtn(card: HTMLElement): HTMLButtonElement {
  const btn = [...card.querySelectorAll<HTMLButtonElement>("button")].find(
    (b) => b.textContent === "Submit",
  );
  if (btn === undefined) {
    throw new Error("no submit button rendered");
  }
  return btn;
}

/** The type the CODE chose, read off the content attribute rather than the
 *  property, so a value the UA does not implement still reports what was asked
 *  for instead of the `text` it degrades to. */
function typeOf(card: HTMLElement): string | null {
  return field(card).getAttribute("type");
}

/** Type a value into the single field, submit, and answer with the content the
 *  card reported. */
function answer(
  schema: ElicitationPropertySchema,
  value: string,
): Record<string, unknown> | undefined {
  const onSubmit = vi.fn<Submit>();
  const card = mount(oneField(schema), onSubmit);
  field(card).value = value;
  submitBtn(card).click();
  expect(onSubmit).toHaveBeenCalledTimes(1);
  return onSubmit.mock.calls[0]?.[1];
}

/** RFC 3339 `date-time`: a full date, `T`, a time WITH seconds, and an offset.
 *  Written as a shape rather than a literal so the suite says the same thing in
 *  every `TZ`. */
const RFC3339 = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/;

beforeEach(() => {
  document.body.replaceChildren();
});

describe("the format → native input type map", () => {
  it.each([
    ["email", "email"],
    ["uri", "url"],
    ["date", "date"],
    ["date-time", "datetime-local"],
  ])("a format of %s renders type=%s", (format, want) => {
    expect(typeOf(mount(oneField({ type: "string", format }), vi.fn<Submit>()))).toBe(want);
  });

  it.each([["duration"], ["ipv6"], ["hostname"], [""]])(
    "an unmapped format of %s falls back to a text box",
    (format) => {
      expect(typeOf(mount(oneField({ type: "string", format }), vi.fn<Submit>()))).toBe("text");
    },
  );

  it("a schema stating no format at all is a text box", () => {
    expect(typeOf(mount(oneField({ type: "string" }), vi.fn<Submit>()))).toBe("text");
  });

  // `format` is arbitrary text off the MCP wire, so the lookup must answer for
  // an inherited member the way it answers for an absent one. A record's
  // prototype does not: `table["constructor"] ?? "text"` yields a function, and
  // its stringification would land in the `type` attribute.
  it.each([["constructor"], ["toString"]])(
    "a format of %s is a text box, not a prototype member",
    (format) => {
      expect(typeOf(mount(oneField({ type: "string", format }), vi.fn<Submit>()))).toBe("text");
    },
  );
});

describe("format: date-time reads back as RFC 3339", () => {
  it("supplies the seconds and the offset a datetime-local omits", () => {
    expect(answer({ type: "string", format: "date-time" }, "2026-09-08T14:30")?.["when"]).toMatch(
      RFC3339,
    );
  });

  it("keeps the seconds a step-bearing control already supplied", () => {
    const got = answer({ type: "string", format: "date-time" }, "2026-09-08T14:30:45")?.["when"];
    // Anchored on the literal local part rather than rebuilt out of `got`: an
    // unconditional `:00` append yields `…14:30:45:00+02:00`, which this rejects
    // and an expectation built from `got` would satisfy on both sides. The offset
    // stays a shape, so the case reads the same in every `TZ`.
    expect(got).toMatch(/^2026-09-08T14:30:45(?:Z|[+-]\d{2}:\d{2})$/);
  });

  // The inverse property, and the one that catches a WRONG offset where the
  // regex above catches a missing one: `getTimezoneOffset()` is the minutes to
  // ADD to reach UTC, so it is negative east of UTC and an inverted sign lands
  // 2× the offset away. Only observable outside UTC, where the offset is zero
  // and a sign carries no information; red-checked under Europe/Paris,
  // Asia/Tokyo and America/New_York.
  it("names the same instant the local value named", () => {
    const local = "2026-09-08T14:30";
    const got = String(answer({ type: "string", format: "date-time" }, local)?.["when"]);
    expect(new Date(got).getTime()).toBe(new Date(local).getTime());
  });

  // Only has teeth where the two dates' offsets differ: measured, Europe/Paris
  // reports -60 for January against -120 for September and America/New_York 300
  // against 240, while Asia/Tokyo reports -540 for both and UTC 0 for both — so
  // in UTC, which CI runs in, this asserts nothing about the entered-instant
  // rule. Same TZ dependence its sibling above names.
  it("resolves a date on the other side of a DST boundary from its own offset", () => {
    const winter = "2026-01-08T14:30";
    const got = String(answer({ type: "string", format: "date-time" }, winter)?.["when"]);
    expect(new Date(got).getTime()).toBe(new Date(winter).getTime());
  });

  // `filled` reads the RAW value, so the `""` half is unobservable through the
  // card: a `toRFC3339` that turned "" into this moment's timestamp would still
  // be dropped here. The key's absence is the whole user-visible surface, since
  // an unfilled value never leaves the card.
  it("omits an empty control from the answer", () => {
    const onSubmit = vi.fn<Submit>();
    const card = mount(oneField({ type: "string", format: "date-time" }), onSubmit);
    submitBtn(card).click();
    expect(onSubmit).toHaveBeenCalledWith("accept", {});
  });
});

describe("format: date needs no normalizing", () => {
  // A date input's value IS RFC 3339 `full-date`, so anything appended to it
  // would be damage.
  it("passes its value through untouched", () => {
    expect(answer({ type: "string", format: "date" }, "2026-09-08")?.["when"]).toBe("2026-09-08");
  });
});

describe("a stated constraint outranks the picker", () => {
  // HTML applies `pattern`, `minLength` and `maxLength` to text-ish types only,
  // so on a date picker they are dropped in silence. Losing a constraint the
  // schema stated is worse than losing the picker.
  it("a date with a pattern becomes a text box that still enforces it", () => {
    const card = mount(
      oneField({ type: "string", format: "date", pattern: "^2026-" }),
      vi.fn<Submit>(),
    );
    expect(typeOf(card)).toBe("text");
    expect(field(card).pattern).toBe("^2026-");
  });

  it("a date-time with a minLength becomes a text box that still enforces it", () => {
    const card = mount(
      oneField({ type: "string", format: "date-time", minLength: 16 }),
      vi.fn<Submit>(),
    );
    expect(typeOf(card)).toBe("text");
    expect(field(card).minLength).toBe(16);
  });

  it("a date with a maxLength becomes a text box that still enforces it", () => {
    const card = mount(
      oneField({ type: "string", format: "date", maxLength: 10 }),
      vi.fn<Submit>(),
    );
    expect(typeOf(card)).toBe("text");
    expect(field(card).maxLength).toBe(10);
  });

  // The floor of the rule: every string is at least 0 long, so `minLength: 0`
  // is not a constraint the picker could lose. `maxLength: 0` is one, forbidding
  // any input at all, so it keeps its case above.
  it("a date with a minLength of 0 keeps its picker", () => {
    const card = mount(oneField({ type: "string", format: "date", minLength: 0 }), vi.fn<Submit>());
    expect(typeOf(card)).toBe("date");
  });

  // The other half, and the one that keeps the fallback from being a blanket
  // rule: these two types honour all three attributes, so they never give up
  // their native type. A condition hard-coded to two format strings would pass
  // the tests above and fail these.
  it("an email keeps its native type AND its pattern", () => {
    const card = mount(
      oneField({ type: "string", format: "email", pattern: ".+@example\\.com" }),
      vi.fn<Submit>(),
    );
    expect(typeOf(card)).toBe("email");
    expect(field(card).pattern).toBe(".+@example\\.com");
  });

  it("a uri keeps its native type AND its length bounds", () => {
    const card = mount(
      oneField({ type: "string", format: "uri", minLength: 8, maxLength: 200 }),
      vi.fn<Submit>(),
    );
    expect(typeOf(card)).toBe("url");
    expect(field(card).minLength).toBe(8);
    expect(field(card).maxLength).toBe(200);
  });
});
