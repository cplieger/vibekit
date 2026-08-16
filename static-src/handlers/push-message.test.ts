// @vitest-environment happy-dom
// ---------------------------------------------------------------------------
// Tests for handlers/push-message.ts's route: which surface a clicked
// notification lands on.
//
// D83's deep link is reused for a subject with no chat behind it — a pull request
// — so the branch is keyed on the SUBJECT rather than on a URL the server would
// have had to assemble. chat.ts and navigate.ts are mocked because both pull the
// whole app graph in behind them.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach } from "vitest";

const mockOpenChangeSet = vi.fn();
const mockOpenChatTab = vi.fn();

vi.mock("../navigate.js", () => ({
  openChangeSet: () => mockOpenChangeSet(),
}));
vi.mock("../chat.js", () => ({
  openChatTab: (id: string, name: string) => mockOpenChatTab(id, name),
}));
vi.mock("../store.js", () => ({ get: () => undefined }));
vi.mock("../toast.js", () => ({ info: vi.fn() }));

const { routePushMessage } = await import("./push-message.js");

beforeEach(() => {
  vi.clearAllMocks();
});

describe("routePushMessage", () => {
  it("sends a PR subject to the git view", () => {
    routePushMessage({
      type: "push",
      reason: "clicked",
      chatId: "",
      subject: "pr:github:github.com:cplieger/vibekit#42",
      title: "Vibekit",
      body: "cplieger/vibekit #42 checks passed",
    });
    expect(mockOpenChangeSet).toHaveBeenCalledTimes(1);
    expect(mockOpenChatTab).not.toHaveBeenCalled();
  });

  it("sends a chat notification to its chat", () => {
    routePushMessage({
      type: "push",
      reason: "clicked",
      chatId: "c1",
      title: "Vibekit",
      body: "Reviewing the poller",
    });
    // The store mock reports no chat, so the notification's TITLE is the tab-name
    // fallback (the body is the agent's line, which is not a tab name).
    expect(mockOpenChatTab).toHaveBeenCalledWith("c1", "Vibekit");
    expect(mockOpenChangeSet).not.toHaveBeenCalled();
  });

  it("does nothing for a workspace-global notification with no subject", () => {
    routePushMessage({
      type: "push",
      reason: "clicked",
      chatId: "",
      title: "Vibekit",
      body: "something happened",
    });
    expect(mockOpenChatTab).not.toHaveBeenCalled();
    expect(mockOpenChangeSet).not.toHaveBeenCalled();
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
    expect(mockOpenChangeSet).toHaveBeenCalledTimes(1);
    expect(mockOpenChatTab).not.toHaveBeenCalled();
  });
});
