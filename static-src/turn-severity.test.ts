// The runtime VOCABULARY half of turn-severity.ts.
//
// Distinct from `turn-severity.node.test.ts` beside it, which is the cross-language
// fixture pin and reads `internal/vibekit/testdata/turn_severity.json` off disk. This
// file owns the exports that have no Go twin, so it needs no file and no fixture.
import { describe, it, expect } from "vitest";

import { TURN_OUTCOME_VALUES } from "./turn-severity.js";
import type { TurnOutcome } from "./wire/types.gen.js";

/** Every member of `TurnOutcome`, SORTED.
 *
 *  Hardcoded rather than joined against the generated array, because
 *  `decoders.gen.ts`'s `TURN_OUTCOMES` is module-private with no export knob — so
 *  there is nothing to compare to and a hand-written list is the only assertion
 *  available. The `satisfies` clause is what ties it to the generated union: a
 *  member that stops being a `TurnOutcome` is a compile error rather than a silent
 *  extra row.
 *
 *  Sorted so the assertion is structure-insensitive: reordering the record the
 *  values are derived from is not a behaviour change, while adding or removing a
 *  key is. */
const EVERY_OUTCOME_SORTED = [
  "cancelled",
  "completed",
  "failed",
  "interrupted",
  "refused",
  "running",
  "unknown",
] as const satisfies readonly TurnOutcome[];

describe("TURN_OUTCOME_VALUES", () => {
  it("holds exactly the seven outcomes the wire can send", () => {
    // The case exists for the DERIVATION rather than for the list. It is
    // `Object.keys` over a total record, so `Object.keys` over the WRONG table, or
    // over one that gained a key which is not an outcome, answers a wrong array
    // that type-checks — the cast is what stops the compiler seeing it. The
    // decoder this feeds then admits or rejects a persisted string by that answer,
    // so a wrong list reads an out-of-vocabulary outcome as valid.
    expect([...TURN_OUTCOME_VALUES].sort()).toEqual([...EVERY_OUTCOME_SORTED]);
  });
});
