// ---------------------------------------------------------------------------
// REQUIRED TEST 1: the worker's destination and the page's opener resolve to the
// SAME route.
//
// Each row asserts three things, and (3) against (1) is the killing property: it
// fails if either half stops going through `pushTargetRoute`, which is the only
// thing keeping the two agreeing. A row asserting only `buildPath` would pass while
// the page diverged, and that is precisely the state this change removes.
//
// The route literals and URLs are HARDCODED, so the table is a spec rather than a
// restatement of the functions it drives.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach } from "vitest";
import { buildPath, type Route } from "./route-path.js";
import { parsePushTarget, pushTargetRoute } from "./push-subject.js";
import {
  registerNotificationOpener,
  _resetNotificationOpenerForTest,
} from "./notification-open.js";

vi.mock("./toast.js", () => ({ info: vi.fn() }));

const { routePushMessage } = await import("./handlers/push-message.js");

interface Row {
  readonly name: string;
  readonly chatId: string;
  readonly subject: string;
  readonly route: Route;
  readonly url: string;
}

const rows: readonly Row[] = [
  {
    name: "a chat",
    chatId: "c-7f3a",
    subject: "",
    route: { kind: "chat", id: "c-7f3a" },
    url: "/chat/c-7f3a",
  },
  {
    name: "a pull request",
    chatId: "",
    subject: "pr:github:github.com:cplieger/vibekit#42",
    route: { kind: "git", tab: "prs", pr: "github:github.com:cplieger/vibekit#42" },
    // The identity rides as a `#pr=` fragment, so a cold click from the tray carries
    // the pull request into `openWindow` rather than only the tab. The `#` inside it
    // percent-encodes to `%23`, which is why the fragment is one encoded value.
    url: "/git/prs#pr=github%3Agithub.com%3Acplieger%2Fvibekit%2342",
  },
  {
    name: "a workflow run",
    chatId: "",
    subject: "run:wf_42",
    route: { kind: "run", id: "wf_42" },
    url: "/run/wf_42",
  },
  {
    name: "the workspace",
    chatId: "",
    subject: "",
    route: { kind: "chat", id: "" },
    url: "/",
  },
];

const opened = vi.fn<(route: Route) => void>();

describe("one subject, one destination", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    registerNotificationOpener(opened);
  });

  for (const row of rows) {
    it(`${row.name} resolves to one route for both halves`, () => {
      const expected = pushTargetRoute(
        parsePushTarget({ chatId: row.chatId, subject: row.subject }),
      );
      expect(expected).toEqual(row.route);

      // The WORKER's destination: what `openWindow` receives when no page is open.
      expect(buildPath(expected)).toBe(row.url);

      // The PAGE's destination, through the real seam.
      routePushMessage({
        type: "push",
        reason: "clicked",
        chatId: row.chatId,
        subject: row.subject,
        title: "Vibekit",
        body: "",
      });
      expect(opened).toHaveBeenCalledTimes(1);
      expect(opened).toHaveBeenCalledWith(expected);
    });
  }
});

describe("an unregistered opener", () => {
  // Its own describe with the reset, because the opener is ONE module-level slot and
  // Vitest links a module once per file: the table above has already registered a spy,
  // so without this the unregistered state is unreachable.
  beforeEach(() => {
    _resetNotificationOpenerForTest();
  });

  it("throws rather than losing the navigation", () => {
    expect(() => {
      routePushMessage({
        type: "push",
        reason: "clicked",
        chatId: "c1",
        subject: "",
        title: "Vibekit",
        body: "",
      });
    }).toThrow(/no opener registered/);
  });
});
