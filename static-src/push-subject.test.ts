// ---------------------------------------------------------------------------
// push-subject.ts's own suite. The cross-language PREFIX contract is
// push-kinds.test.ts's; what is pinned here is what the module DECIDES.
// ---------------------------------------------------------------------------

import { describe, it, expect } from "vitest";
import pushTypesGo from "../internal/vibekit/push_types.go?raw";
import {
  askTarget,
  chatTarget,
  parsePushTarget,
  prIdentity,
  pushTargetRoute,
  pushTargetTag,
  runTarget,
} from "./push-subject.js";

describe("parsePushTarget", () => {
  it("reads every prefix, an empty remainder and an empty pair", () => {
    expect(parsePushTarget({ chatId: "", subject: "pr:gh:github.com:a/b#1" })).toEqual({
      kind: "pr",
      identity: "gh:github.com:a/b#1",
    });
    expect(parsePushTarget({ chatId: "", subject: "run:wf_1" })).toEqual({
      kind: "run",
      workflowID: "wf_1",
    });
    expect(parsePushTarget({ chatId: "c1", subject: "" })).toEqual({ kind: "chat", chatID: "c1" });
    // A bare prefix is reachable only from a malformed envelope, and it resolves to the
    // workspace rather than to /git or /run/.
    expect(parsePushTarget({ chatId: "", subject: "pr:" })).toEqual({ kind: "workspace" });
    expect(parsePushTarget({ chatId: "", subject: "run:" })).toEqual({ kind: "workspace" });
    expect(parsePushTarget({ chatId: "", subject: "" })).toEqual({ kind: "workspace" });
  });

  it("falls to the chat id for a subject with no recognised prefix", () => {
    expect(parsePushTarget({ chatId: "c1", subject: "something-else" })).toEqual({
      kind: "chat",
      chatID: "c1",
    });
  });
});

describe("pushTargetRoute", () => {
  it("maps each kind to its destination", () => {
    expect(pushTargetRoute({ kind: "chat", chatID: "c1" })).toEqual({ kind: "chat", id: "c1" });
    expect(pushTargetRoute({ kind: "pr", identity: "gh:github.com:a/b#1" })).toEqual({
      kind: "git",
      tab: "prs",
      pr: "gh:github.com:a/b#1",
    });
    expect(pushTargetRoute({ kind: "run", workflowID: "wf_1" })).toEqual({
      kind: "run",
      id: "wf_1",
    });
    // The default chat route: "/" is what the worker opens when no page is up.
    expect(pushTargetRoute({ kind: "workspace" })).toEqual({ kind: "chat", id: "" });
  });
});

describe("pushTargetTag", () => {
  it("gives a keyed subject, a chat and the workspace their own tray slot", () => {
    expect(pushTargetTag({ kind: "pr", identity: "gh:github.com:a/b#1" })).toBe(
      "vibekit:pr:gh:github.com:a/b#1",
    );
    expect(pushTargetTag({ kind: "run", workflowID: "wf_1" })).toBe("vibekit:run:wf_1");
    expect(pushTargetTag({ kind: "chat", chatID: "c1" })).toBe("vibekit:c1");
    expect(pushTargetTag({ kind: "workspace" })).toBe("vibekit");
  });
});

describe("prIdentity", () => {
  it("is vibekit.PRSubject's composition minus the prefix", () => {
    // The Go side reads `PRSubjectPrefix + forgeID + ":" + repo + "#" + strconv.Itoa(number)`;
    // this is the same key with the prefix stripped, which is what makes the identity
    // comparable against one the PRs tab builds for its own rows.
    const m =
      /func PRSubject\([^)]*\)[^{]*\{\s*return PushSubject\{Key: PRSubjectPrefix \+ ([^}]+)\}/.exec(
        pushTypesGo,
      );
    expect(m, "vibekit.PRSubject's composition not found").not.toBeNull();
    expect((m?.[1] ?? "").replace(/\s+/g, " ").trim()).toBe(
      'forgeID + ":" + repo + "#" + strconv.Itoa(number)',
    );
    expect(prIdentity("gh:github.com", "cplieger/vibekit", 42)).toBe(
      "gh:github.com:cplieger/vibekit#42",
    );
  });
});

describe("the constructors", () => {
  it("refuses a synthetic run: chat id and answers the RUN", () => {
    // internal/agent/run_host.go registers a parentless run's bridge under
    // `run:<workflowId>`, so that run's asks arrive as the envelope chat id. The only
    // mechanical proof of the parentless-run ruling.
    expect(chatTarget("run:wf_1")).toEqual({ kind: "run", workflowID: "wf_1" });
  });

  it("answers the workspace for an id that cannot be addressed", () => {
    expect(chatTarget("")).toEqual({ kind: "workspace" });
    expect(runTarget("")).toEqual({ kind: "workspace" });
  });

  it("lets a non-empty run id win over the envelope's chat id", () => {
    expect(askTarget("c-7", "wf_1")).toEqual({ kind: "run", workflowID: "wf_1" });
    expect(askTarget("c-7", "")).toEqual({ kind: "chat", chatID: "c-7" });
    expect(askTarget("c-7", undefined)).toEqual({ kind: "chat", chatID: "c-7" });
  });
});
