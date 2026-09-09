// The four states of the connect's declaration, and the one value it may never
// answer. `""` is the transport's unregistered default, which the server reads as
// "never declared" and answers with every open chat's snapshot — so a resolver
// returning it re-enters the payload this whole mechanism exists to bound, silently.

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { SNAPSHOT_NONE, snapshotDeclaration } from "./snapshot-declaration.js";
import { setSessions, setActive } from "./store.js";
import type { Session } from "./types.js";

const m = vi.hoisted(() => ({ bootMode: vi.fn<() => "full" | "reduced">(() => "full") }));

// The boot mode is a persisted count over a `sessionStorage` record, so it is mocked
// rather than driven: what this file is about is which rung the resolver takes, and a
// real record would make every case depend on the guard's own window arithmetic.
vi.mock("./reload-guard.js", () => ({ bootMode: m.bootMode }));

/** Seat a chat in the store and make it active, which is the second rung's input. */
function activate(id: string): void {
  setSessions([
    {
      id,
      name: "c",
      messages: [],
      message_count: 0,
      has_more: false,
      thinking: false,
      working_label: "",
    },
  ] as unknown as Session[]);
  setActive(id);
}

const original = location.pathname + location.search;

beforeEach(() => {
  m.bootMode.mockReturnValue("full");
  setSessions([]);
  setActive("");
});

afterEach(() => {
  history.replaceState(null, "", original);
});

describe("what a connect declares as on screen", () => {
  it("declares nothing in reduced mode, whatever else is true", () => {
    m.bootMode.mockReturnValue("reduced");
    activate("c-visible");
    history.replaceState(null, "", "/chat/c-in-the-url");

    // Ahead of both other rungs deliberately, and the reason is the BYTE LEVER rather
    // than an absent transcript: `boot.ts` gates only the pre-network `resumeSnapshot`
    // paint on the mode, so a reduced boot's FIRST FRAME draws none while the restore
    // below it loads and paints one like any other boot. What that costs the chat it
    // IS showing is recorded beside the connect-payload decisions in `vibekit.md`.
    expect(snapshotDeclaration()).toBe(SNAPSHOT_NONE);
  });

  it("declares the ACTIVE chat when there is one", () => {
    activate("c-visible");
    history.replaceState(null, "", "/chat/c-in-the-url");

    // The active chat outranks the URL: every connect after the first is a reconnect,
    // where the store knows more than the address bar does.
    expect(snapshotDeclaration()).toBe("c-visible");
  });

  it("declares the chat the URL names when no chat is active yet", () => {
    history.replaceState(null, "", "/chat/c-in-the-url");

    // The BOOT connect: `transport.init` opens the stream synchronously and every
    // writer of the active chat runs behind `startBoot`, so this is the state the
    // first connect of every full boot is actually in.
    expect(snapshotDeclaration()).toBe("c-in-the-url");
  });

  it("declares the LAUNCHING chat for a subagent deep link", () => {
    history.replaceState(null, "", "/chat/c-in-the-url/subagent/u-7");

    // `parseRoute` answers `{kind:"subagent", chat, id}` here, and that page renders
    // the launching chat's own blocks — so the chat whose in-flight transcript it
    // needs is the same one, and reading only `kind === "chat"` declared nothing on
    // the one door that opens straight onto a live delegate.
    expect(snapshotDeclaration()).toBe("c-in-the-url");
  });

  it("declares nothing when neither the store nor the URL names a chat", () => {
    history.replaceState(null, "", "/");

    expect(snapshotDeclaration()).toBe(SNAPSHOT_NONE);
  });

  it("never answers the empty string, in any of the four states", () => {
    const answers: string[] = [];
    for (const mode of ["full", "reduced"] as const) {
      for (const path of ["/", "/chat/c-in-the-url", "/git", "/files/x"]) {
        for (const active of ["", "c-visible"]) {
          m.bootMode.mockReturnValue(mode);
          history.replaceState(null, "", path);
          if (active === "") {
            setSessions([]);
            setActive("");
          } else {
            activate(active);
          }
          answers.push(snapshotDeclaration());
        }
      }
    }

    // The one value the server cannot tell from a client that registered no provider
    // at all, so it is the one answer this resolver may never give.
    expect(answers).not.toContain("");
    expect(answers).toHaveLength(16);
  });
});
