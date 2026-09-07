// ---------------------------------------------------------------------------
// The run-control vocabulary's own rules.
//
// The status→verbs matrix these cases used to pin is GONE, and its absence is the
// point. It was a copy of the server's own gates, twinned test for twinned test,
// and the pair proved the copies agreed while neither could express the third
// input the rule actually has — whether anything still hosts the run. So a run
// could pass both suites and still be drawn a button whose only outcome was a 409.
// `GET /api/runs/{id}/controls` answers all three inputs; what is left here is the
// boundary narrow and the outcome sentences.
// ---------------------------------------------------------------------------

import { describe, it, expect } from "vitest";
import {
  CONTROL_LABEL,
  asRunVerb,
  offeredVerbs,
  refusalSentences,
  retryOutcomeNotice,
} from "./run-controls.js";

describe("narrowing a verb off the wire", () => {
  it("accepts every verb this client has a button for", () => {
    for (const verb of ["pause", "resume", "cancel", "retry"] as const) {
      expect(asRunVerb(verb)).toBe(verb);
      expect(CONTROL_LABEL[verb]).toBeTruthy();
    }
  });

  // The label lookup is TOTAL over RunVerb, so a word the server grows before this
  // client is taught it must be dropped at the boundary rather than reaching
  // CONTROL_LABEL and rendering a button with no text. Prototype keys are the same
  // hazard through a different door: `CONTROL_LABEL["toString"]` is truthy.
  it("rejects a word it has no button for, prototype keys included", () => {
    for (const name of ["", "delete", "some_future_verb", "toString", "constructor"]) {
      expect(asRunVerb(name)).toBeUndefined();
    }
  });

  it("drops the verbs it cannot name and keeps the server's row order", () => {
    expect(offeredVerbs(["retry", "some_future_verb", "cancel"])).toEqual(["retry", "cancel"]);
    // The server sends destructive last; re-sorting here would be this module
    // deciding a thing it was just relieved of.
    expect(offeredVerbs(["resume", "cancel"])).toEqual(["resume", "cancel"]);
    expect(offeredVerbs([])).toEqual([]);
  });
});

describe("the refusal sentences", () => {
  it("orders them by verb so the list does not reshuffle between fetches", () => {
    const refused = {
      cancel: "cancel sentence",
      retry: "retry sentence",
      pause: "pause sentence",
    };
    expect(refusalSentences(refused)).toEqual([
      "retry sentence",
      "pause sentence",
      "cancel sentence",
    ]);
  });

  // A sentence for a verb this client cannot NAME is still shown: it is the
  // server's prose about the RUN, not about a button, and dropping it would leave a
  // reader with an empty row and no reason — which is the state the whole answer
  // exists to end. Sorted last, behind the verbs a row would have drawn.
  it("keeps a sentence for a verb it has no button for, last", () => {
    expect(
      refusalSentences({ some_future_verb: "future sentence", retry: "retry sentence" }),
    ).toEqual(["retry sentence", "future sentence"]);
  });

  it("has nothing to say for an absent or empty refusal map", () => {
    expect(refusalSentences(undefined)).toEqual([]);
    expect(refusalSentences({})).toEqual([]);
    // An empty sentence is not a sentence: rendering it would produce a blank
    // paragraph where the buttons were.
    expect(refusalSentences({ pause: "" })).toEqual([]);
  });
});

describe("reporting a retry's outcome", () => {
  // The defect, stated as a rule. A retry that reset ZERO nodes is a first-class
  // outcome upstream and a no-op from the reader's seat, and reporting it as a
  // success is precisely what made "I pressed Retry and nothing happened" the
  // design's expected appearance.
  it("does NOT report a zero-node retry as a success", () => {
    const notice = retryOutcomeNotice(0);
    expect(notice.level).toBe("error");
    expect(notice.text).toContain("Nothing to retry");
  });

  it("states how many steps a real retry reset", () => {
    expect(retryOutcomeNotice(5)).toEqual({ level: "success", text: "Retrying 5 steps" });
  });

  // Singular, because "Retrying 1 steps" is the kind of line that makes a reader
  // distrust the number beside it.
  it("agrees with itself about one step", () => {
    expect(retryOutcomeNotice(1)).toEqual({ level: "success", text: "Retrying 1 step" });
  });

  // A negative count cannot arrive from a decoded array length, but the guard is
  // `<= 0` rather than `=== 0` so the no-op branch cannot be skipped by an
  // impossible value that would otherwise read as a success.
  it("treats a nonsensical count as nothing rather than as a success", () => {
    expect(retryOutcomeNotice(-1).level).toBe("error");
  });
});
