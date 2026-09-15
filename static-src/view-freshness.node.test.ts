// The one freshness question, answered off the digest subjects' version map. Node:
// the leaf touches no DOM, and a wrong answer here is a missed fetch on activation.

import { beforeEach, describe, expect, it } from "vitest";

import { _resetForTest, hasSubject, observeStamp } from "./subject-versions.js";
import type { TabKind } from "./types.js";
import { forgetView, viewStale } from "./view-freshness.js";

beforeEach(() => {
  _resetForTest();
});

describe("viewStale", () => {
  it.each<TabKind>(["run", "files", "editor", "docs", "settings", "history", "git"])(
    "a %s view has no subject and is always stale",
    (kind) => {
      expect(viewStale(kind, "anything")).toBe(true);
      // Even a held chat by the same ref proves nothing about it.
      observeStamp({ kind: "chat", ref: "anything", version: "1" });
      expect(viewStale(kind, "anything")).toBe(true);
    },
  );

  it("a chat is stale until its version is held, and stale again after forgetView", () => {
    expect(viewStale("chat", "c1")).toBe(true);
    observeStamp({ kind: "chat", ref: "c1", version: "3" });
    expect(viewStale("chat", "c1")).toBe(false);
    forgetView("chat", "c1");
    expect(viewStale("chat", "c1")).toBe(true);
  });

  it("a subagent page reads its launching chat's freshness", () => {
    expect(viewStale("subagent", "c1/task-9")).toBe(true);
    observeStamp({ kind: "chat", ref: "c1", version: "3" });
    expect(viewStale("subagent", "c1/task-9")).toBe(false);
    // Another chat's version says nothing about this one.
    expect(viewStale("subagent", "c2/task-9")).toBe(true);
  });

  it("a malformed subagent ref is stale rather than fresh by accident", () => {
    observeStamp({ kind: "chat", ref: "", version: "3" });
    expect(viewStale("subagent", "/task-9")).toBe(true);
    expect(viewStale("subagent", "no-slash")).toBe(true);
  });
});

describe("forgetView", () => {
  it("drops both of a chat's subjects: the transcript and its live turn", () => {
    observeStamp({ kind: "chat", ref: "c1", version: "3" });
    observeStamp({ kind: "live_turn", ref: "c1", version: "7:2" });
    forgetView("chat", "c1");
    expect(hasSubject("chat", "c1")).toBe(false);
    expect(hasSubject("live_turn", "c1")).toBe(false);
  });

  it("leaves every other chat alone", () => {
    observeStamp({ kind: "chat", ref: "c1", version: "3" });
    observeStamp({ kind: "chat", ref: "c2", version: "3" });
    forgetView("chat", "c1");
    expect(hasSubject("chat", "c2")).toBe(true);
  });

  it("does nothing for a kind with no subject", () => {
    observeStamp({ kind: "chat", ref: "x", version: "3" });
    forgetView("files", "x");
    expect(hasSubject("chat", "x")).toBe(true);
  });
});
