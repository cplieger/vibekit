// Tests for the PWA share-target + URL shortcut handling in share-target.ts.
// chat.ts has a heavy import graph, so it's mocked to a spy — this file only
// verifies the wiring: which URL params trigger which action.
import { describe, it, expect, vi, beforeEach } from "vitest";

const { createPlannerSessionMock } = vi.hoisted(() => ({
  createPlannerSessionMock: vi.fn(async () => undefined),
}));

vi.mock("./chat.js", () => ({
  createPlannerSession: createPlannerSessionMock,
}));

// share-target.ts touches only $.promptInput; back it with a real textarea so
// the value/focus writes land on a real element. The element is created inside
// the factory (which is hoisted) and read back via the mocked import below,
// avoiding a top-level TDZ reference.
vi.mock("./dom.js", () => ({
  $: { promptInput: document.createElement("textarea") },
}));

import { applyShareTarget } from "./share-target.js";
import { $ } from "./dom.js";

beforeEach(() => {
  vi.clearAllMocks();
  $.promptInput.value = "";
  history.replaceState(null, "", "/");
});

describe("applyShareTarget", () => {
  it("creates a planner session on ?agent=planner", async () => {
    history.replaceState(null, "", "/?agent=planner");
    await applyShareTarget();
    expect(createPlannerSessionMock).toHaveBeenCalledTimes(1);
  });

  it("does NOT create a planner session without ?agent=planner", async () => {
    history.replaceState(null, "", "/?agent=other");
    await applyShareTarget();
    expect(createPlannerSessionMock).not.toHaveBeenCalled();
  });

  it("does nothing (no planner) on a bare URL", async () => {
    await applyShareTarget();
    expect(createPlannerSessionMock).not.toHaveBeenCalled();
    expect($.promptInput.value).toBe("");
  });

  it("populates the prompt input from ?prompt= without creating a planner", async () => {
    history.replaceState(null, "", "/?prompt=hello");
    await applyShareTarget();
    expect($.promptInput.value).toBe("hello");
    expect(createPlannerSessionMock).not.toHaveBeenCalled();
  });

  // AWAITED, and that is the assertion: the planner create is the one thing here
  // that has to finish before boot applies the initial route, because the chat id
  // is the server's now. A synchronous call would strip the query while the tab
  // the route resolves against did not exist yet.
  it("strips the query string after applying so a reload doesn't re-fire", async () => {
    history.replaceState(null, "", "/?agent=planner");
    await applyShareTarget();
    expect(location.search).toBe("");
  });
});
