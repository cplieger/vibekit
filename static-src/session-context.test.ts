import { describe, it, expect, vi, beforeEach } from "vitest";

// Mock persist.js so we observe patchSettings calls without touching the
// network. Mirrors persist.test.ts's pattern.
vi.mock("./persist.js", () => ({
  patchSettings: vi.fn(),
}));

import {
  setLastModel,
  getLastModel,
  restoreLastModel,
  setLastEffort,
  getLastEffortFor,
  restoreLastEffort,
} from "./session-context.js";
import { patchSettings } from "./persist.js";

describe("setLastModel — redundant-write guard", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    // Reset module-level cache by restoring to a known sentinel.
    restoreLastModel("__reset__");
  });

  it("patches once when the value changes from the cache", () => {
    setLastModel("claude-opus-4.6");
    expect(patchSettings).toHaveBeenCalledTimes(1);
    expect(patchSettings).toHaveBeenCalledWith({ last_model: "claude-opus-4.6" });
    expect(getLastModel()).toBe("claude-opus-4.6");
  });

  it("does NOT patch when called again with the cached value", () => {
    // Regression: the SSE settings_updated handler used to push the
    // server-confirmed value back through setLastModel, which called
    // patchSettings, which triggered another settings_updated, which
    // looped at debounce speed forever. The guard below makes any
    // setLastModel call with the already-cached value a no-op.
    setLastModel("claude-opus-4.6");
    vi.clearAllMocks();

    setLastModel("claude-opus-4.6");
    setLastModel("claude-opus-4.6");
    setLastModel("claude-opus-4.6");

    expect(patchSettings).not.toHaveBeenCalled();
  });

  it("patches again only when the value actually changes", () => {
    setLastModel("claude-opus-4.6");
    setLastModel("claude-opus-4.6"); // no-op
    setLastModel("gpt-5"); // change
    setLastModel("gpt-5"); // no-op

    expect(patchSettings).toHaveBeenCalledTimes(2);
    expect(patchSettings).toHaveBeenNthCalledWith(1, { last_model: "claude-opus-4.6" });
    expect(patchSettings).toHaveBeenNthCalledWith(2, { last_model: "gpt-5" });
  });

  it("restoreLastModel updates the cache without patching", () => {
    restoreLastModel("kiro-default");
    expect(patchSettings).not.toHaveBeenCalled();
    expect(getLastModel()).toBe("kiro-default");

    // After a restore, setLastModel with the same value is a no-op too.
    setLastModel("kiro-default");
    expect(patchSettings).not.toHaveBeenCalled();
  });
});

describe("setLastEffort — the level a new chat opens on, scoped to its model", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    restoreLastEffort("__reset__", "__reset__");
  });

  it("patches once when the pair changes from the cache", () => {
    setLastEffort("max", "claude-opus-5");
    expect(patchSettings).toHaveBeenCalledTimes(1);
    expect(patchSettings).toHaveBeenCalledWith({
      last_effort: "max",
      last_effort_model: "claude-opus-5",
    });
    expect(getLastEffortFor("claude-opus-5")).toBe("max");
  });

  it("the seed answers ONLY for the model it was picked under", () => {
    // A tier is a judgement about one model; carried onto another it overrode
    // that model's own default (user report, 2026-08-31).
    setLastEffort("max", "claude-opus-5");
    expect(getLastEffortFor("gpt-luna")).toBe("");
    expect(getLastEffortFor("")).toBe("");
    expect(getLastEffortFor("claude-opus-5")).toBe("max");
  });

  it("does NOT patch when called again with the cached pair", () => {
    // Same loop the model guard exists for: the settings_updated handler must not
    // be able to push a server-confirmed value back through the setter. It uses
    // restoreLastEffort for that, and this guard stops any other caller
    // reintroducing it. It also makes a repeat pick of the level already in force
    // free instead of waking the save indicator.
    setLastEffort("max", "m1");
    vi.clearAllMocks();

    setLastEffort("max", "m1");
    setLastEffort("max", "m1");

    expect(patchSettings).not.toHaveBeenCalled();
  });

  it("the same level under a DIFFERENT model is a real change and patches", () => {
    setLastEffort("max", "m1");
    vi.clearAllMocks();

    setLastEffort("max", "m2");

    expect(patchSettings).toHaveBeenCalledWith({ last_effort: "max", last_effort_model: "m2" });
  });

  it("restoreLastEffort updates the cache without patching", () => {
    restoreLastEffort("xhigh", "m1");
    expect(patchSettings).not.toHaveBeenCalled();
    expect(getLastEffortFor("m1")).toBe("xhigh");

    setLastEffort("xhigh", "m1");
    expect(patchSettings).not.toHaveBeenCalled();
  });

  it("starts empty, so a user who never picked gets the model's own default", () => {
    // The seed is absent rather than guessed: marking a tier nobody chose would
    // make the picker claim a level the session is not running at.
    restoreLastEffort(undefined, undefined);
    expect(getLastEffortFor("__reset__")).toBe("__reset__");
  });
});
