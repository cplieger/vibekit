// The catalog fetch POLICY, tested where app.ts could not be: the verdict
// mapping, the retry bound and the exhaustion settle were the one untested part
// of this change, and both previous rounds' defects were found in exactly those
// three decisions.
//
// The module takes its reader and its two sinks as parameters, so every case
// here drives real production code with no DOM and no endpoint.
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

import { readVerdict, refreshCatalog } from "./model-catalog.js";
import type { CatalogAnswer } from "./model-catalog.js";
import type { CatalogPhase } from "./picker.js";
import type { CatalogState } from "./wire/types.gen.js";

function answer(catalog: CatalogState): CatalogAnswer {
  return { catalog };
}

interface Recorder {
  readonly deps: Parameters<typeof refreshCatalog<CatalogAnswer>>[0];
  readonly applied: CatalogAnswer[];
  readonly phases: CatalogPhase[];
  reads: number;
}

/** A refresh whose reads are scripted. The last entry repeats, so a loop that
 *  keeps asking keeps getting the same answer rather than running off the end. */
function recorder(script: readonly (CatalogAnswer | null)[]): Recorder {
  const applied: CatalogAnswer[] = [];
  const phases: CatalogPhase[] = [];
  const r: Recorder = {
    applied,
    phases,
    reads: 0,
    deps: {
      read: () => {
        const at = Math.min(r.reads, script.length - 1);
        r.reads += 1;
        return Promise.resolve(script[at] ?? null);
      },
      apply: (a) => {
        applied.push(a);
      },
      setPhase: (p) => {
        phases.push(p);
      },
    },
  };
  return r;
}

describe("the catalog verdict mapping", () => {
  it("treats an empty catalog as a real answer, not a failure to retry", () => {
    // A verdict decides whether to keep ASKING. Whether a LIST replaces a cached
    // vocabulary is a separate per-list rule at the sink, and conflating the two
    // fails in both directions: as a verdict gate, an empty models list withholds
    // the PHASE too and the picker says "loading" forever; with no rule at all, an
    // empty answer clears a populated catalog.
    expect(readVerdict("empty")).toBe("usable");
  });

  it("maps a populated catalog to usable and an unavailable one to retry", () => {
    expect(readVerdict("ready")).toBe("usable");
    expect(readVerdict("unavailable")).toBe("retry");
  });
});

describe("refreshCatalog", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("applies a usable first answer and issues no second read", async () => {
    const r = recorder([answer("ready")]);

    await refreshCatalog(r.deps);

    expect(r.reads).toBe(1);
    expect(r.applied).toEqual([answer("ready")]);
    expect(r.phases).toEqual(["ready"]);
  });

  it("issues no second read for an EMPTY catalog", async () => {
    // `_kiro/config/template` is a pure cache read that triggers no model
    // refresh, so a second call re-reads the same empty cache: looping on it
    // would be hammering with no convergence.
    const r = recorder([answer("empty")]);

    await refreshCatalog(r.deps);

    expect(r.reads).toBe(1);
    expect(r.applied).toEqual([answer("empty")]);
    expect(r.phases).toEqual(["ready"]);
  });

  it("retries an unavailable answer and applies the one that converges", async () => {
    const r = recorder([answer("unavailable"), answer("ready")]);

    const done = refreshCatalog(r.deps);
    await vi.advanceTimersByTimeAsync(3_000);
    await done;

    expect(r.reads).toBe(2);
    expect(r.applied).toEqual([answer("ready")]);
    // "unavailable" is never reported on the way: the loop is still working on it,
    // and a settled failure line over a live retry is a claim it has to take back.
    expect(r.phases).toEqual(["ready"]);
  });

  it("never applies an unavailable answer, so a degraded read replaces nothing", async () => {
    // The vocabulary write is what this protects: `unavailableTemplate` emits an
    // empty effort list BY CONSTRUCTION, so a login-triggered fetch that degrades
    // used to replace the tiers a successful boot fetch had already landed.
    const r = recorder([answer("unavailable")]);

    const done = refreshCatalog(r.deps);
    await vi.advanceTimersByTimeAsync(200_000);
    await done;

    expect(r.applied).toEqual([]);
  });

  it("settles unavailable once the retry budget is spent, and stops reading", async () => {
    const r = recorder([answer("unavailable")]);

    const done = refreshCatalog(r.deps);
    await vi.advanceTimersByTimeAsync(200_000);
    await done;

    expect(r.phases).toEqual(["unavailable"]);
    const settled = r.reads;
    await vi.advanceTimersByTimeAsync(200_000);
    expect(r.reads).toBe(settled);
  });

  it("bounds the retry: a transient failure cannot exceed the attempt ceiling", async () => {
    // A `null` read is transient (network, decode), and the 180s budget admits
    // about three ~50s attempts — the attempt ceiling is the guard against a
    // pathologically fast failure turning that budget into hundreds of requests.
    const r = recorder([null]);

    const done = refreshCatalog(r.deps);
    await vi.advanceTimersByTimeAsync(200_000);
    await done;

    expect(r.reads).toBeLessThanOrEqual(7);
    expect(r.phases).toEqual(["unavailable"]);
  });

  it("refuses a second caller while a loop is running", async () => {
    const first = recorder([answer("unavailable")]);
    const second = recorder([answer("ready")]);

    const done = refreshCatalog(first.deps);
    await refreshCatalog(second.deps);
    expect(second.reads).toBe(0);

    await vi.advanceTimersByTimeAsync(200_000);
    await done;
  });

  it("lets a RESET restart a live loop instead of being refused by it", async () => {
    // A login is exactly the new information that may have fixed the read;
    // refusing it means it contributes nothing until the 180s loop exhausts.
    const first = recorder([answer("unavailable")]);
    const login = recorder([answer("ready")]);

    const done = refreshCatalog(first.deps);
    await refreshCatalog(login.deps, { reset: true });

    expect(login.applied).toEqual([answer("ready")]);
    expect(login.phases).toEqual(["ready"]);
    // The aborted loop reports NOTHING: the new one owns the answer, and a settle
    // here would flash "couldn't load" over a catalog that just arrived.
    await vi.advanceTimersByTimeAsync(200_000);
    await done;
    expect(first.phases).toEqual([]);
    expect(first.applied).toEqual([]);
  });

  it("releases the slot for the next caller after a reset", async () => {
    const first = recorder([answer("unavailable")]);
    const login = recorder([answer("ready")]);
    const later = recorder([answer("ready")]);

    const done = refreshCatalog(first.deps);
    await refreshCatalog(login.deps, { reset: true });
    await vi.advanceTimersByTimeAsync(200_000);
    await done;

    await refreshCatalog(later.deps);
    expect(later.reads).toBe(1);
  });
});
