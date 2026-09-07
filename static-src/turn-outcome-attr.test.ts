// Does anything actually WRITE `data-severity`?
//
// `turn-outcome-css.test.ts` asserts the hue partition in the stylesheet, and it
// cannot answer this: a CSS rule keyed on an attribute nothing writes paints
// nothing and fails silently — no error, no missing element, just a mark in the
// resting state that reads as a decision somebody made. So the two files are one
// guard in two halves, and neither is meaningful alone. Cross-referenced from
// there.
//
// The three writers are the three surfaces the partition covers:
// `updateTurnFooter`, `updateTurnHeader` and `turn-rail.ts`'s marker + cluster.
// Every one of them already wrote `data-outcome`, which is why the attribute the
// stylesheet now keys on is the thing worth pinning.
//
// Driven through the REAL writers in a REAL DOM rather than by re-deriving the
// value: an assertion against a second copy of `severityOf`'s table would pass
// with every writer deleted.
import { describe, it, expect, vi, beforeAll, beforeEach } from "vitest";

// The spread mocks below need the ORIGINAL module's type, and `import()` type
// annotations are forbidden by the shared eslint config — so the two modules are
// type-imported here, the way handlers/turn.test.ts already does it.
import type * as ApiClient from "./api-client.js";
import type * as StoreLoad from "./store-load.js";

type ApiClientModule = typeof ApiClient;
type StoreLoadModule = typeof StoreLoad;

// The rail imports scroll.ts, which self-initialises a singleton against
// `#messages` at module load, and api-client for its session-wide index. Neither
// is under test here. Both stubs go through `vi.hoisted` because the factories are
// hoisted above these declarations.
const { scrollable } = vi.hoisted(() => ({
  scrollable: { by: 500 },
}));
vi.mock("./scroll.js", () => ({
  jumpTo: vi.fn(),
  scrollableBy: () => scrollable.by,
  onReaderGesture: () => () => {
    /* no reader in this suite */
  },
  getScrollEl: () => ({
    addEventListener: () => {
      /* the rail's own re-measure is not under test */
    },
    getBoundingClientRect: () => ({ top: 0, bottom: 600, height: 600 }),
  }),
}));
// `apiGet` alone, with the rest of the module real: a replacing factory has to
// satisfy every name ANY module in this file's import graph reaches, and the
// header's own graph reaches `apiGetTyped`. Spreading the original keeps the mock
// to the one function under stub.
vi.mock("./api-client.js", async (orig) => ({
  ...(await orig<ApiClientModule>()),
  apiGet: vi.fn(),
}));
vi.mock("./store-load.js", async (orig) => ({
  ...(await orig<StoreLoadModule>()),
  loadMessages: vi.fn(),
  loadList: vi.fn(),
}));

import { buildTurnFooter } from "./fundamentals/turn-footer.js";
import { buildTurnHeader } from "./fundamentals/turn-header.js";
import { mountTurnRail, loadTurnRail, resetTurnRail, type TurnSummary } from "./turn-rail.js";
import { apiGet } from "./api-client.js";
import { severityOf } from "./turn-severity.js";
import type { TurnOutcome } from "./turns.js";

/** Every outcome the wire can send. The severity coverage case in
 *  `turn-outcome-css.test.ts` is what pins this list total. */
const OUTCOMES: TurnOutcome[] = [
  "running",
  "completed",
  "cancelled",
  "interrupted",
  "refused",
  "unknown",
  "failed",
];

describe("the footer stamps its severity", () => {
  it("writes data-severity from the shared table, for every outcome", () => {
    for (const outcome of OUTCOMES) {
      const footer = buildTurnFooter({ outcome, commands: 1 });
      expect(footer.dataset["severity"], outcome).toBe(severityOf(outcome));
      // Both attributes, because they answer different questions: `data-outcome`
      // still carries the lead WORD and the one `unknown` hue exception.
      expect(footer.dataset["outcome"], outcome).toBe(outcome);
    }
  });

  it("defaults an ABSENT outcome to clean, matching the key it defaults", () => {
    // A turn persisted before the field existed carries none, and the footer
    // already defaults `data-outcome` to `completed`. The severity has to default
    // the same way or a legacy transcript paints an outcome-less footer with a
    // stopped wash.
    const footer = buildTurnFooter({ commands: 1 });
    expect(footer.dataset["outcome"]).toBe("completed");
    expect(footer.dataset["severity"]).toBe("clean");
  });
});

describe("the header stamps its severity", () => {
  it("writes data-severity from the shared table, for every outcome", () => {
    for (const outcome of OUTCOMES) {
      const header = buildTurnHeader({
        n: 1,
        outcome,
        ts: 1_700_000_000_000,
        request: "do the thing",
        attachments: [],
      });
      expect(header.dataset["severity"], outcome).toBe(severityOf(outcome));
      expect(header.dataset["outcome"], outcome).toBe(outcome);
    }
  });
});

describe("the rail stamps its severity", () => {
  const host = document.createElement("div");

  beforeAll(() => {
    document.body.appendChild(host);
    mountTurnRail(host);
  });

  beforeEach(() => {
    resetTurnRail();
  });

  function rail(): HTMLElement {
    const el = host.querySelector<HTMLElement>(".turn-rail");
    if (el === null) {
      throw new Error("rail not mounted");
    }
    return el;
  }

  it("writes data-severity on every marker, for every outcome", async () => {
    // One turn per outcome, so the assertion is a partition rather than a sample.
    // A minute apart, which is well inside the gap threshold.
    const index: TurnSummary[] = OUTCOMES.map((outcome, i) => ({
      id: `m${String(i + 1)}`,
      n: i + 1,
      outcome,
      ts: (i + 1) * 60_000,
    }));
    vi.mocked(apiGet).mockResolvedValue({ turns: index });
    await loadTurnRail("c-attr");

    const markers = [...rail().querySelectorAll<HTMLElement>(".rail-marker")];
    expect(markers, "one marker per turn, no clustering at seven").toHaveLength(OUTCOMES.length);
    for (const [i, marker] of markers.entries()) {
      const outcome = OUTCOMES[i];
      expect(marker.dataset["outcome"], `marker ${String(i)}`).toBe(outcome);
      expect(marker.dataset["severity"], `marker ${String(i)}`).toBe(severityOf(outcome));
    }
  });

  it("writes data-severity on a CLUSTER too", async () => {
    // The rail's fourth writer, and the one a marker-only assertion would miss:
    // past capacity EVERY turn is inside a cluster, so on a long session the
    // cluster is the only outcome mark the rail paints.
    //
    // The `.turn-rail` element carries no layout here (no app stylesheet is
    // mounted), so capacity comes from the module's own 600px fallback — 25 rows
    // at the production pitch. 200 turns is comfortably past it either way.
    const index: TurnSummary[] = Array.from({ length: 200 }, (_, i) => ({
      id: `m${String(i + 1)}`,
      n: i + 1,
      outcome: (i === 0 ? "failed" : "completed") satisfies TurnOutcome as TurnOutcome,
      ts: (i + 1) * 60_000,
    }));
    vi.mocked(apiGet).mockResolvedValue({ turns: index });
    await loadTurnRail("c-cluster");

    const clusters = [...rail().querySelectorAll<HTMLElement>(".rail-cluster")];
    expect(clusters.length, "200 turns cluster").toBeGreaterThan(0);
    for (const cluster of clusters) {
      const outcome = cluster.dataset["outcome"];
      expect(outcome, "a cluster names its worst member's outcome").toBeDefined();
      expect(cluster.dataset["severity"]).toBe(severityOf(outcome as TurnOutcome));
    }
    // The first cluster holds the `failed` turn, so it is the one that proves the
    // stamp is not uniformly `clean`.
    expect(clusters[0]?.dataset["outcome"]).toBe("failed");
    expect(clusters[0]?.dataset["severity"]).toBe("broken");
  });
});
