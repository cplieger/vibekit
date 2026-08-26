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
  getLastEffort,
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

describe("setLastEffort — the level a new chat opens on", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    restoreLastEffort("__reset__");
  });

  it("patches once when the value changes from the cache", () => {
    setLastEffort("max");
    expect(patchSettings).toHaveBeenCalledTimes(1);
    expect(patchSettings).toHaveBeenCalledWith({ last_effort: "max" });
    expect(getLastEffort()).toBe("max");
  });

  it("does NOT patch when called again with the cached value", () => {
    // Same loop the model guard exists for: the settings_updated handler must not
    // be able to push a server-confirmed value back through the setter. It uses
    // restoreLastEffort for that, and this guard stops any other caller
    // reintroducing it. It also makes a repeat pick of the level already in force
    // free instead of waking the save indicator.
    setLastEffort("max");
    vi.clearAllMocks();

    setLastEffort("max");
    setLastEffort("max");

    expect(patchSettings).not.toHaveBeenCalled();
  });

  it("restoreLastEffort updates the cache without patching", () => {
    restoreLastEffort("xhigh");
    expect(patchSettings).not.toHaveBeenCalled();
    expect(getLastEffort()).toBe("xhigh");

    setLastEffort("xhigh");
    expect(patchSettings).not.toHaveBeenCalled();
  });

  it("starts empty, so a user who never picked gets the model's own default", () => {
    // The seed is absent rather than guessed: marking a tier nobody chose would
    // make the picker claim a level the session is not running at.
    restoreLastEffort(undefined);
    expect(getLastEffort()).toBe("__reset__");
  });
});
