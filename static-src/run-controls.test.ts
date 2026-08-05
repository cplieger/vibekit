import { describe, it, expect } from "vitest";
import { RUN_CONTROLS, CONTROL_LABEL, RUN_STATUSES } from "./run-controls.js";

// KAS's WorkflowStatusSchema, exhaustively.

describe("run control gating", () => {
  // The client table must agree with the server's runVerb.from gates, which in
  // turn mirror what KAS accepts. Two copies of one rule is a deliberate choice
  // (the server is the authority; the client renders only buttons that can
  // succeed) and this is what stops them drifting.
  it("offers each verb only from the statuses that accept it", () => {
    expect(RUN_CONTROLS.running).toEqual(["pause", "cancel"]);
    expect(RUN_CONTROLS.paused).toEqual(["resume", "cancel"]);
    expect(RUN_CONTROLS.completed).toEqual([]);
    expect(RUN_CONTROLS.failed).toEqual(["retry"]);
    expect(RUN_CONTROLS.aborted).toEqual(["retry"]);
  });

  // A terminal run must never offer cancel: there is nothing to stop, and the
  // button would be the one control that does nothing.
  it("never offers cancel on a terminal run", () => {
    for (const status of ["completed", "failed", "aborted"] as const) {
      expect(RUN_CONTROLS[status]).not.toContain("cancel");
    }
  });

  // Retry is offered on failed and aborted only, NOT on completed. KAS's retry
  // throws on the no-nodeId branch unless the status is failed or aborted, so a
  // Retry button on a completed run could only ever produce an error. An earlier
  // revision of this table included it and both the Go and the TS test asserted
  // the wrong matrix, which is what a matrix test is supposed to prevent.
  it("offers retry only where KAS accepts it", () => {
    expect(RUN_CONTROLS.failed).toContain("retry");
    expect(RUN_CONTROLS.aborted).toContain("retry");
    expect(RUN_CONTROLS.completed).not.toContain("retry");
    expect(RUN_CONTROLS.running).not.toContain("retry");
    expect(RUN_CONTROLS.paused).not.toContain("retry");
  });

  // Pause and resume are opposites and must never both be on offer, or the row
  // asks the reader to decide which of two contradictory states the run is in.
  it("never offers pause and resume together", () => {
    for (const status of RUN_STATUSES) {
      const verbs = RUN_CONTROLS[status] ?? [];
      expect(verbs.includes("pause") && verbs.includes("resume")).toBe(false);
    }
  });

  // An unknown status renders NO controls rather than guessing. A future KAS
  // status should degrade to a read-only view, not to a wrong button.
  it("offers nothing for an unknown status", () => {
    expect(RUN_CONTROLS.some_future_status).toBeUndefined();
    expect(RUN_CONTROLS[""]).toBeUndefined();
  });

  // Every verb any status offers needs a label, or a button renders blank.
  it("labels every offered verb", () => {
    for (const status of RUN_STATUSES) {
      for (const verb of RUN_CONTROLS[status] ?? []) {
        expect(CONTROL_LABEL[verb]).toBeTruthy();
      }
    }
  });

  // Every live status offers a way to stop. A running or paused run with no
  // terminal control is a run the user cannot get rid of except by deleting the
  // chat, which is the defect the whole control set exists to close.
  it("always offers a way out of a live run", () => {
    for (const status of ["running", "paused"] as const) {
      expect(RUN_CONTROLS[status]).toContain("cancel");
    }
  });
});
