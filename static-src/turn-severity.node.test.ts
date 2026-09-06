// The TypeScript half of the turn-severity cross-language pin.
//
// The Go half is `TestTurnSeverityContract` in `internal/vibekit/turns_test.go`,
// and both read ONE fixture. A rule changed in only one language fails in the
// other, which is the only mechanism keeping the two honest — there is no code
// generation for behaviour, only for the value's spelling.
//
// A node test, and it must stay one: it reads a file off disk, which is what
// `*.node.test.ts` means here. The module under test is DOM-free on purpose, so
// nothing about the subject wants a browser.
import { readFileSync } from "node:fs";
import { describe, it, expect } from "vitest";

import { severityOf, isBroken, defaultFailureReason } from "./turn-severity.js";
import type { TurnOutcome } from "./wire/types.gen.js";

const FIXTURE_PATH = "../internal/vibekit/testdata/turn_severity.json";

interface SeverityCase {
  readonly outcome: string;
  readonly severity: string;
  readonly default_reason: string;
}

function loadCases(): readonly SeverityCase[] {
  const raw = readFileSync(new URL(FIXTURE_PATH, import.meta.url), "utf8");
  const parsed: unknown = JSON.parse(raw);
  if (typeof parsed !== "object" || parsed === null) {
    throw new Error(`${FIXTURE_PATH} is not an object`);
  }
  const cases = (parsed as Record<string, unknown>)["cases"];
  if (!Array.isArray(cases) || cases.length === 0) {
    throw new Error(`${FIXTURE_PATH} has no cases`);
  }
  return cases as readonly SeverityCase[];
}

/** Every value the generated `TurnOutcome` union can hold.
 *
 *  Hand-listed for the reason the Go side's list is: a union is not enumerable at
 *  runtime, and that is exactly what makes the coverage assertion below worth
 *  having — this list, the union and the fixture must all agree, so an outcome
 *  added to the wire and forgotten in one of them fails here. The `satisfies`
 *  clause is what ties it to the generated type, so a member that stops being a
 *  `TurnOutcome` is a compile error rather than a silent extra row. */
const EVERY_OUTCOME = [
  "running",
  "completed",
  "cancelled",
  "interrupted",
  "failed",
  "refused",
  "unknown",
] as const satisfies readonly TurnOutcome[];

describe("the severity table agrees with the Go implementation", () => {
  it("grades every fixture row the same way", () => {
    for (const c of loadCases()) {
      const outcome = c.outcome as TurnOutcome;
      expect(severityOf(outcome), `severityOf(${c.outcome})`).toBe(c.severity);
      expect(defaultFailureReason(outcome), `defaultFailureReason(${c.outcome})`).toBe(
        c.default_reason,
      );
    }
  });

  it("covers every outcome the wire can send", () => {
    // The other direction, and it is what makes the table a contract rather than a
    // sample: an eighth outcome with no row reaches five surfaces that must be
    // total over the severity, so it fails here instead of there.
    const rows = new Set(loadCases().map((c) => c.outcome));
    for (const outcome of EVERY_OUTCOME) {
      expect(rows.has(outcome), `${outcome} has a fixture row`).toBe(true);
      rows.delete(outcome);
    }
    expect([...rows], "fixture rows naming no declared outcome").toEqual([]);
  });
});

describe("severityOf", () => {
  it("never grades anything but a completed turn as clean", () => {
    // The one direction a status mark must not fail in, and the defect the whole
    // module exists to remove: `interrupted` graded as nothing, so a broken turn
    // painted the hollow ring that means nothing is happening here.
    for (const outcome of EVERY_OUTCOME) {
      if (outcome === "completed") {
        continue;
      }
      expect(severityOf(outcome), `${outcome} must not read clean`).not.toBe("clean");
    }
    expect(severityOf("completed")).toBe("clean");
  });

  it("grades an absent outcome as stopped, not clean", () => {
    // A turn projected from a transcript with no durable outcome (every record
    // written before the field existed) reaches here as undefined. Stopped is the
    // safe answer for `unknown`'s reason; clean would claim it worked.
    expect(severityOf(undefined)).toBe("stopped");
  });

  it("reads a value the wire adds later as stopped rather than clean", () => {
    // The decoder makes this unreachable in practice — TurnOutcome is a generated
    // union, so a subject carrying anything else fails at the boundary — and the
    // arm is what makes the failure DIRECTION safe if it ever is reached. The cast
    // is the only way to express an input the type forbids.
    expect(severityOf("teleported" as TurnOutcome)).toBe("stopped");
  });
});

describe("isBroken", () => {
  it("answers true for exactly the three broken outcomes", () => {
    const broken = EVERY_OUTCOME.filter((o) => isBroken(o));
    expect(broken).toEqual(["interrupted", "failed", "refused"]);
  });
});

describe("defaultFailureReason", () => {
  it("gives every turn that ended badly something to say", () => {
    // This is the property symptom 1 turned on: a card with a red mark and an
    // empty body is what a reader of the reported chat actually got.
    for (const outcome of EVERY_OUTCOME) {
      const severity = severityOf(outcome);
      if (severity === "broken" || severity === "stopped") {
        expect(defaultFailureReason(outcome), `${outcome} says something`).not.toBe("");
      } else {
        expect(defaultFailureReason(outcome), `${outcome} says nothing`).toBe("");
      }
    }
  });
});
