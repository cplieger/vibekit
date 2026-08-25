// ---------------------------------------------------------------------------
// The UI arrangement is server-owned, and these pin the three things that makes
// hard: what stays local, what a remote change does, and what the writer's own
// echo must NOT do.
//
// The arrangement moved off localStorage because a per-device copy is what made
// it not travel between devices. The sibling terminal app made the same mistake
// first (`wt-tab-order`), got the same complaint, and its steering doc now
// carries the rule this module obeys: do not reintroduce a local arrangement as
// an offline fallback, because two sources of truth for one ordering is how the
// original bug got its per-load reshuffle.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";

import * as uiState from "./ui-state.js";
import { LS_UI_STATE_KEY } from "./ls-keys.js";

/** The bytes localStorage holds, which must be the LOCAL fields only. */
function blob(): Record<string, unknown> {
  const raw = localStorage.getItem(LS_UI_STATE_KEY);
  return raw === null ? {} : (JSON.parse(raw) as Record<string, unknown>);
}

let fetchMock: ReturnType<typeof vi.fn>;
const originalFetch = globalThis.fetch;

beforeEach(() => {
  localStorage.clear();
  uiState._resetForTest();
  fetchMock = vi.fn();
  globalThis.fetch = fetchMock as unknown as typeof fetch;
});

afterEach(() => {
  globalThis.fetch = originalFetch;
  vi.useRealTimers();
});

function okJSON(body: unknown, status = 200): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: () => Promise.resolve(body),
  } as unknown as Response;
}

describe("hydrate reads the arrangement from the server", () => {
  it("adopts the served document and its revision", async () => {
    fetchMock.mockResolvedValue(
      okJSON({ revision: 7, tab_order: ["__git__", "chat-a"], theme: "light" }),
    );

    await uiState.hydrate();

    const s = uiState.load();
    expect(s.tab_order).toEqual(["__git__", "chat-a"]);
    expect(s.theme).toBe("light");
    expect(uiState.currentRevision()).toBe(7);
  });

  it("leaves the arrangement empty when the server is unreachable", async () => {
    // Deliberately NOT a localStorage fallback: an arrangement that opens the
    // wrong tabs is worse than one that opens none, and a local copy is the
    // shape whose drift this store replaced.
    localStorage.setItem(LS_UI_STATE_KEY, JSON.stringify({ tab_order: ["ghost"] }));
    fetchMock.mockRejectedValue(new Error("offline"));

    await uiState.hydrate();

    expect(uiState.load().tab_order).toEqual([]);
  });

  it("caches the theme locally so the pre-paint snippet has an answer", async () => {
    fetchMock.mockResolvedValue(okJSON({ revision: 1, theme: "dark" }));
    await uiState.hydrate();
    expect(blob()["theme"]).toBe("dark");
  });
});

describe("the local/synced split", () => {
  it("keeps the viewer's own fields in localStorage and out of the PUT", async () => {
    vi.useFakeTimers();
    fetchMock.mockResolvedValue(okJSON({ revision: 1 }));
    await uiState.hydrate();
    fetchMock.mockClear();
    fetchMock.mockResolvedValue(okJSON({ revision: 2, tab_order: ["a"] }));

    uiState.save({ active_view: "chat-x", shell_open: true, shell_h: 320, tab_order: ["a"] });
    await vi.advanceTimersByTimeAsync(600);

    // The three viewer fields are on this device only. shell_h is here because a
    // LENGTH is the one value whose right answer depends on the screen: 700px is
    // two thirds of a laptop and the whole of a phone.
    expect(blob()["active_view"]).toBe("chat-x");
    expect(blob()["shell_open"]).toBe(true);
    expect(blob()["shell_h"]).toBe(320);
    // And they are absent from what the server is told.
    const put = fetchMock.mock.calls.find(
      (c) => (c[1] as RequestInit | undefined)?.method === "PUT",
    );
    expect(put).toBeDefined();
    const sent = JSON.parse((put?.[1] as RequestInit).body as string) as Record<string, unknown>;
    expect(sent["tab_order"]).toEqual(["a"]);
    for (const local of ["active_view", "shell_open", "shell_h"]) {
      expect(local in sent).toBe(false);
    }
    // The revision it was based on rides along, or the write cannot be judged stale.
    expect(sent["revision"]).toBe(1);
  });

  it("reads the local fields back through load(), so a reload restores them", async () => {
    fetchMock.mockResolvedValue(okJSON({ revision: 1 }));
    await uiState.hydrate();
    uiState.save({ active_view: "chat-y", shell_h: 480 });
    expect(uiState.load().active_view).toBe("chat-y");
    expect(uiState.load().shell_h).toBe(480);
  });

  it("survives a remote change without losing the local fields", async () => {
    // applyRemote rebuilds the document from the server's, so the local half has
    // to be re-read rather than defaulted — otherwise another device's tab open
    // silently resets this screen's panel height and active tab.
    fetchMock.mockResolvedValue(okJSON({ revision: 1 }));
    await uiState.hydrate();
    uiState.save({ active_view: "chat-z", shell_h: 512 });

    uiState.applyRemote({ revision: 2, tab_order: ["a", "b"] });

    expect(uiState.load().active_view).toBe("chat-z");
    expect(uiState.load().shell_h).toBe(512);
    expect(uiState.load().tab_order).toEqual(["a", "b"]);
  });
});

describe("a remote change", () => {
  it("notifies subscribers and adopts the new revision", async () => {
    fetchMock.mockResolvedValue(okJSON({ revision: 1, tab_order: ["a"] }));
    await uiState.hydrate();

    const seen: string[][] = [];
    const stop = uiState.onRemoteChange((s) => {
      seen.push(s.tab_order);
    });

    const changed = uiState.applyRemote({ revision: 2, tab_order: ["a", "b"] });
    expect(changed).toBe(true);
    expect(seen).toEqual([["a", "b"]]);
    expect(uiState.currentRevision()).toBe(2);
    stop();
  });

  it("ignores the writer's OWN echo, so a just-dragged tab does not snap back", async () => {
    fetchMock.mockResolvedValue(okJSON({ revision: 1, tab_order: ["a", "b"] }));
    await uiState.hydrate();

    const seen: string[][] = [];
    const stop = uiState.onRemoteChange((s) => {
      seen.push(s.tab_order);
    });

    // Same synced content, higher revision: this is what a device receives back
    // after its own PUT lands.
    const changed = uiState.applyRemote({ revision: 2, tab_order: ["a", "b"] });
    expect(changed).toBe(false);
    expect(seen).toEqual([]);
    stop();
  });

  it("does not republish what it just adopted", async () => {
    vi.useFakeTimers();
    fetchMock.mockResolvedValue(okJSON({ revision: 1, tab_order: ["a"] }));
    await uiState.hydrate();
    fetchMock.mockClear();

    uiState.applyRemote({ revision: 2, tab_order: ["a", "b"] });
    await vi.advanceTimersByTimeAsync(600);

    // An echo that bumps the revision makes every other device's next write
    // stale for a change nobody made.
    const puts = fetchMock.mock.calls.filter(
      (c) => (c[1] as RequestInit | undefined)?.method === "PUT",
    );
    expect(puts).toEqual([]);
  });

  it("refuses a document older than the one it holds", async () => {
    fetchMock.mockResolvedValue(okJSON({ revision: 5, tab_order: ["current"] }));
    await uiState.hydrate();

    expect(uiState.applyRemote({ revision: 4, tab_order: ["stale"] })).toBe(false);
    expect(uiState.load().tab_order).toEqual(["current"]);
  });
});

describe("publishing", () => {
  it("coalesces a burst of mutations into one request", async () => {
    vi.useFakeTimers();
    fetchMock.mockResolvedValue(okJSON({ revision: 1 }));
    await uiState.hydrate();
    fetchMock.mockClear();
    fetchMock.mockResolvedValue(okJSON({ revision: 2, tab_order: ["a", "b", "c"] }));

    // A drag rewrites the order on every commit; three commits must not cost
    // three requests.
    uiState.save({ tab_order: ["a"] });
    uiState.save({ tab_order: ["a", "b"] });
    uiState.save({ tab_order: ["a", "b", "c"] });
    await vi.advanceTimersByTimeAsync(600);

    const puts = fetchMock.mock.calls.filter(
      (c) => (c[1] as RequestInit | undefined)?.method === "PUT",
    );
    expect(puts).toHaveLength(1);
  });

  it("adopts the server's document on a 409 instead of re-sending", async () => {
    vi.useFakeTimers();
    fetchMock.mockResolvedValue(okJSON({ revision: 1, tab_order: ["a"] }));
    await uiState.hydrate();
    fetchMock.mockClear();
    // The server says this device is behind, and hands back the truth.
    fetchMock.mockResolvedValue(okJSON({ revision: 9, tab_order: ["theirs"] }, 409));

    uiState.save({ tab_order: ["mine"] });
    await vi.advanceTimersByTimeAsync(600);

    // Re-sending would be a fight the stale writer cannot win.
    expect(uiState.load().tab_order).toEqual(["theirs"]);
    expect(uiState.currentRevision()).toBe(9);
    const puts = fetchMock.mock.calls.filter(
      (c) => (c[1] as RequestInit | undefined)?.method === "PUT",
    );
    expect(puts).toHaveLength(1);
  });

  it("flush sends a pending write immediately, for a page going away", async () => {
    vi.useFakeTimers();
    fetchMock.mockResolvedValue(okJSON({ revision: 1 }));
    await uiState.hydrate();
    fetchMock.mockClear();
    fetchMock.mockResolvedValue(okJSON({ revision: 2, tab_order: ["a"] }));

    uiState.save({ tab_order: ["a"] });
    uiState.flush();
    await vi.advanceTimersByTimeAsync(0);

    const puts = fetchMock.mock.calls.filter(
      (c) => (c[1] as RequestInit | undefined)?.method === "PUT",
    );
    expect(puts).toHaveLength(1);
  });

  it("never publishes before hydration, so an empty document cannot overwrite a real one", async () => {
    vi.useFakeTimers();
    // No hydrate() at all.
    uiState.save({ tab_order: ["a"] });
    await vi.advanceTimersByTimeAsync(2000);

    const puts = fetchMock.mock.calls.filter(
      (c) => (c[1] as RequestInit | undefined)?.method === "PUT",
    );
    expect(puts).toEqual([]);
  });
});
