// ---------------------------------------------------------------------------
// Tests for handlers/push-message.ts's own job: turning a posted MESSAGE into a
// push target, including the `subject ?? ""` the wire type leaves optional.
//
// The destination itself is push-route.test.ts's subject — that file asserts the
// worker's URL and this opener resolve to ONE route — so what is pinned here is the
// mapping from the message's two fields onto the target, through the real seam with
// a spy opener rather than a mock of it.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach } from "vitest";
import type { Route } from "../route-path.js";
import { registerNotificationOpener } from "../notification-open.js";

vi.mock("../toast.js", () => ({ info: vi.fn() }));

const toast = await import("../toast.js");
const { initPushMessages, routePushMessage } = await import("./push-message.js");

const opened = vi.fn<(route: Route) => void>();
// Installed ONCE for the file: the listener it adds outlives every test, so a second
// install would fan one posted message out to two callbacks.
const onSubscriptionChanged = vi.fn();
initPushMessages(onSubscriptionChanged);

/** What the worker posts: a MessageEvent on the page's ServiceWorkerContainer. */
function postFromWorker(data: unknown): void {
  navigator.serviceWorker.dispatchEvent(new MessageEvent("message", { data }));
}

beforeEach(() => {
  vi.clearAllMocks();
  registerNotificationOpener(opened);
});

describe("initPushMessages", () => {
  it("re-derives the presence tag on a rotated subscription, and neither routes nor toasts", () => {
    postFromWorker({
      type: "push",
      reason: "subscription_changed",
      chatId: "",
      subject: "",
      title: "",
      body: "",
    });
    expect(onSubscriptionChanged).toHaveBeenCalledTimes(1);
    expect(opened).not.toHaveBeenCalled();
    expect(toast.info).not.toHaveBeenCalled();
  });

  it("toasts an arrived chat push and leaves the presence tag alone", () => {
    postFromWorker({
      type: "push",
      reason: "arrived",
      chatId: "c1",
      subject: "",
      title: "Vibekit",
      body: "Agent finished",
    });
    expect(toast.info).toHaveBeenCalledWith("Agent finished");
    expect(onSubscriptionChanged).not.toHaveBeenCalled();
    expect(opened).not.toHaveBeenCalled();
  });
});

describe("routePushMessage", () => {
  it("sends a PR subject to the PRs tab, focused on that pull request", () => {
    routePushMessage({
      type: "push",
      reason: "clicked",
      chatId: "",
      subject: "pr:github:github.com:cplieger/vibekit#42",
      title: "Vibekit",
      body: "cplieger/vibekit #42 checks passed",
    });
    expect(opened).toHaveBeenCalledWith({
      kind: "git",
      tab: "prs",
      pr: "github:github.com:cplieger/vibekit#42",
    });
  });

  it("sends a chat notification to its chat", () => {
    routePushMessage({
      type: "push",
      reason: "clicked",
      chatId: "c1",
      title: "Vibekit",
      body: "Reviewing the poller",
    });
    expect(opened).toHaveBeenCalledWith({ kind: "chat", id: "c1" });
  });

  it("sends a workspace-global notification to the default chat", () => {
    routePushMessage({
      type: "push",
      reason: "clicked",
      chatId: "",
      title: "Vibekit",
      body: "something happened",
    });
    expect(opened).toHaveBeenCalledWith({ kind: "chat", id: "" });
  });

  it("prefers the subject over an accidental chat id", () => {
    // Exactly one subject field is set on the wire, so this is a defensive
    // ordering rather than a live case — and the PR branch is the right winner: a
    // PR notification has no chat to open.
    routePushMessage({
      type: "push",
      reason: "clicked",
      chatId: "c1",
      subject: "pr:github:github.com:a/b#1",
      title: "Vibekit",
      body: "checks failed",
    });
    expect(opened).toHaveBeenCalledWith({
      kind: "git",
      tab: "prs",
      pr: "github:github.com:a/b#1",
    });
  });
});
