// The helper's own drift guard, and it exists because of a measured failure
// rather than symmetry with tabs-mock.test.ts beside it.
//
// `scrollMock` is only useful while it is TOTAL. `scroll.ts` gained
// `scrollableBy` and this helper did not, and the consequence was NOT a named
// missing export: the suites that spread it kept passing on their own, while the
// FULL run aborted after 145 of 220 files with "[vitest] There was an error when
// mocking a module" and no file, no export and no import chain in the message.
// Individually green, collectively dead — the worst shape a harness failure can
// take, because every obvious bisection step reports the file as fine.
//
// The helper's header comment already asks a writer to add new exports here.
// That comment is not a mechanism; this file is.
import { describe, it, expect, vi } from "vitest";

// `scroll.ts` builds its singleton against $.messages / $.messagesWrap at import
// and reads $.scrollBottom in init, so importing the REAL module needs those
// elements to exist. Borrowed verbatim from scroll.test.ts: an auto-creating
// Proxy answers whatever the module asks for, which is what keeps this guard from
// carrying a list of element ids that would itself drift.
vi.mock("../dom.js", () => ({
  $: new Proxy(
    {},
    {
      get: (_t, prop: string) => {
        const id = String(prop);
        let e = document.getElementById(id);
        if (e === null) {
          e = document.createElement(id === "scrollBottom" ? "button" : "div");
          e.id = id;
          if (id === "scrollBottom") {
            e.appendChild(document.createElement("span"));
          }
          document.body.appendChild(e);
        }
        return e;
      },
    },
  ),
}));

import { scrollMock } from "./scroll-mock.js";
import * as scroll from "../scroll.js";

// Types are erased at runtime, so both sides compare the VALUE surface — exactly
// what an ESM link needs to resolve, which is what the mock stands in for.
const real = Object.keys(scroll as Record<string, unknown>).sort();
const mocked = Object.keys(scrollMock).sort();

describe("the scroll.js mock helper stays total", () => {
  it("names every value scroll.ts exports", () => {
    expect(real.length, "the real module exported nothing; the import is wrong").toBeGreaterThan(0);
    const missing = real.filter((k) => !mocked.includes(k));
    expect(missing, `scroll-mock.ts is missing: ${missing.join(", ")}`).toEqual([]);
  });

  it("names nothing scroll.ts does not export, so a rename cannot hide behind it", () => {
    const extra = mocked.filter((k) => !real.includes(k));
    expect(extra, `scroll-mock.ts has stale entries: ${extra.join(", ")}`).toEqual([]);
  });
});
