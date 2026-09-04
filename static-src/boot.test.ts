// The boot: four reads in the first frame, three regions, one chat-list read.
//
// It was a five-`await` serial chain behind an opaque splash, and the whole app
// waited on the slowest link — whoami, measured at p50 457 ms with three hard
// 5-second timeouts in 88 reads. Nothing in that order was a data dependency, so
// the shape under test here is: the reads are ISSUED TOGETHER, each answer is
// adopted where it lands, and the identity verdict gates one sidebar row rather
// than the app's existence.
//
// Every collaborator is mocked, because what is being tested is the ORDER the
// boot issues its reads in and the branches it takes over their answers — none of
// which involves what any of them does.

import { describe, it, expect, vi, beforeEach } from "vitest";
import type * as BootModule from "./boot.js";
import type { BootSnapshot } from "./boot-snapshot.js";
import type { IdentityVerdict } from "./identity.js";
import type { EffectiveSettings } from "./persist.js";
import { settingsPayload } from "./__test-helpers__/settings.js";

/** A promise plus the handle to settle it, so a test can hold one read open and
 *  watch what the boot does without it. */
function deferred<T>(): { promise: Promise<T>; resolve: (v: T) => void } {
  let resolve!: (v: T) => void;
  const promise = new Promise<T>((r) => {
    resolve = r;
  });
  return { promise, resolve };
}

const m = vi.hoisted(() => {
  // The router's suppression COUNT, modelled rather than left an unrelated spy.
  // `pushRoute` and `replaceRoute` return early while the depth is above zero
  // (router.ts, where the count's own semantics are pinned), so the depth a URL
  // write is made AT is the whole question at each of the boot's two window
  // boundaries — and while this was a bare `vi.fn()` both boundaries were wrong
  // and the suite was green.
  const suppression = { depth: 0 };
  /** The depth each URL-writing call was made at, in order. */
  const depths = {
    activate: [] as number[],
    replaceRoute: [] as number[],
    share: [] as number[],
  };
  return {
    suppression,
    depths,
    resetDepths: (): void => {
      suppression.depth = 0;
      depths.activate.length = 0;
      depths.replaceRoute.length = 0;
      depths.share.length = 0;
    },
    loadList: vi.fn(),
    loadSettings: vi.fn(),
    resolveIdentity: vi.fn(),
    refreshRetention: vi.fn(),
    listTabs: vi.fn(),
    renderIdentity: vi.fn(),
    restoreAll: vi.fn(),
    adoptThemeFromSettings: vi.fn(),
    initPostAuthUI: vi.fn(),
    showLoginModal: vi.fn(),
    createSession: vi.fn(),
    // Records the depth because `activateTab`'s onShow ends in `pushRoute`
    // (tabs.ts): what this call does to the URL is decided by the window it is
    // made inside.
    activateRestoredTab: vi.fn(() => {
      depths.activate.push(suppression.depth);
    }),
    markHydrated: vi.fn(),
    markBootDone: vi.fn(),
    applyShareTarget: vi.fn(() => {
      depths.share.push(suppression.depth);
      return Promise.resolve();
    }),
    toastError: vi.fn(),
    getSessions: vi.fn(),
    getActive: vi.fn(),
    getActiveId: vi.fn(),
    getActiveTabRoute: vi.fn(),
    setStatus: vi.fn(),
    applyRoute: vi.fn(),
    readBootSnapshot: vi.fn(),
    paintBootSnapshot: vi.fn(),
    clearBootSnapshot: vi.fn(),
    startBootSnapshot: vi.fn(),
    clearDeviceKeys: vi.fn(),
    resetFoldState: vi.fn(),
    parseRoute: vi.fn(() => ({ kind: "chat", id: "" })),
    replaceRoute: vi.fn(() => {
      depths.replaceRoute.push(suppression.depth);
    }),
    suppressPush: vi.fn((v: boolean) => {
      suppression.depth = v ? suppression.depth + 1 : Math.max(0, suppression.depth - 1);
    }),
    // Typed, because the cases below read the registered listener back OFF the mock
    // and call it: an inferred zero-arg signature makes `mock.calls` an empty tuple.
    subscribeByName: vi.fn<
      (name: string, fn: (inst: { readonly status: string }) => void) => () => void
    >(() => () => undefined),
  };
});

vi.mock("./actions/index.js", () => ({ subscribeByName: m.subscribeByName }));
// Only the action's NAME is read here (boot.ts subscribes by it rather than by a
// literal), so the definition itself needs no behaviour.
vi.mock("./actions/settings.js", () => ({ logout: { name: "settings.logout" } }));
vi.mock("./store-load.js", () => ({ loadList: m.loadList }));
vi.mock("./persist.js", () => ({}));
vi.mock("./store.js", () => ({
  getActive: m.getActive,
  getActiveId: m.getActiveId,
  getSessions: m.getSessions,
  registerEvictionExemption: vi.fn(),
  startEvictionSweep: vi.fn(),
}));
vi.mock("./settings.js", () => ({
  adoptThemeFromSettings: m.adoptThemeFromSettings,
  initPostAuthUI: m.initPostAuthUI,
  loadSettings: m.loadSettings,
  renderIdentity: m.renderIdentity,
  restoreAll: m.restoreAll,
}));
vi.mock("./session-context.js", () => ({
  restoreLastEffort: vi.fn(),
  restoreLastModel: vi.fn(),
}));
vi.mock("./identity.js", () => ({ resolveIdentity: m.resolveIdentity }));
vi.mock("./session-catalog.js", () => ({ fetchCatalog: vi.fn() }));
vi.mock("./transport.js", () => ({ markHydrated: m.markHydrated }));
vi.mock("./modals.js", () => ({ showLoginModal: m.showLoginModal }));
vi.mock("./tabs.js", () => ({
  activateRestoredTab: m.activateRestoredTab,
  getActiveTabRoute: m.getActiveTabRoute,
}));
vi.mock("./tabs-sync.js", () => ({ listTabs: m.listTabs }));
vi.mock("./router.js", () => ({
  parseRoute: m.parseRoute,
  replaceRoute: m.replaceRoute,
  suppressPush: m.suppressPush,
}));
vi.mock("./chat.js", () => ({ createSession: m.createSession }));
vi.mock("./governance.js", () => ({ initGovernance: vi.fn() }));
vi.mock("./runtime-health.js", () => ({ initRuntimeHealth: vi.fn() }));
vi.mock("./status.js", () => ({ initStatusVersions: vi.fn(), setStatus: m.setStatus }));
vi.mock("./versions.js", () => ({ loadVersions: vi.fn() }));
vi.mock("./retention.js", () => ({ refreshRetention: m.refreshRetention }));
vi.mock("./run-store.js", () => ({
  hasLiveRunForChat: vi.fn(),
  rebuildLiveRuns: vi.fn(),
}));
vi.mock("./subagent-view.js", () => ({ subagentTabProjectsChat: vi.fn() }));
vi.mock("./view-swap.js", () => ({ markBootDone: m.markBootDone }));
vi.mock("./share-target.js", () => ({ applyShareTarget: m.applyShareTarget }));
vi.mock("./toast.js", () => ({ error: m.toastError }));
vi.mock("./boot-snapshot.js", () => ({
  readBootSnapshot: m.readBootSnapshot,
  paintBootSnapshot: m.paintBootSnapshot,
  clearBootSnapshot: m.clearBootSnapshot,
  startBootSnapshot: m.startBootSnapshot,
}));
vi.mock("./ls-keys.js", () => ({ clearDeviceKeys: m.clearDeviceKeys }));
vi.mock("./fold-state.js", () => ({ resetFoldState: m.resetFoldState }));

/** A fresh module per test: `postAuthInitDone` and the connected latch are module
 *  state, and `vi.resetModules()` does not re-evaluate a module in Browser Mode —
 *  the module map is URL-keyed, so a busted specifier is what mints a new
 *  instance. The `.ts` extension is load-bearing for coverage attribution. */
let bootSeq = 0;
async function freshBoot(): Promise<typeof BootModule> {
  bootSeq++;
  return (await import(/* @vite-ignore */ `./boot.ts?t=${bootSeq}`)) as typeof BootModule;
}

const SIGNED_IN: IdentityVerdict = { state: "signed_in", email: "someone@example.test" };

/** A record with something in it, so a paint that DID happen is distinguishable
 *  from the empty default. */
const SNAPSHOT: BootSnapshot = {
  tabs: [{ id: "t1", kind: "chat", ref: "c1", parent: "", pinned: false, owns: true }],
  chats: [],
  transcript_chat_id: "",
  messages: [],
};

/** The default happy answers: settings load, one chat, the tab set adopts, and no
 *  snapshot — a first-ever boot on this screen. */
function arrangeHappy(settings: EffectiveSettings = settingsPayload()): void {
  m.loadSettings.mockResolvedValue(settings);
  m.resolveIdentity.mockResolvedValue(SIGNED_IN);
  m.loadList.mockResolvedValue(true);
  m.refreshRetention.mockResolvedValue(undefined);
  m.listTabs.mockResolvedValue(true);
  m.createSession.mockResolvedValue(undefined);
  m.getSessions.mockReturnValue([{ id: "c1" }]);
  // No chat and no restored non-chat tab, so the default "/" boot canonicalizes
  // nothing until a case says otherwise.
  m.getActive.mockReturnValue(undefined);
  m.getActiveId.mockReturnValue("");
  m.getActiveTabRoute.mockReturnValue(null);
  m.readBootSnapshot.mockResolvedValue(null);
  m.paintBootSnapshot.mockReturnValue(false);
  m.clearBootSnapshot.mockResolvedValue(undefined);
}

beforeEach(() => {
  vi.clearAllMocks();
  // Neither the depth nor the recorded call sites are mock state, so
  // `clearAllMocks` does not reach them.
  m.resetDepths();
  arrangeHappy();
});

describe("the boot issues its reads together", () => {
  it("has all five in flight before any of them answers", async () => {
    // Held open, all of them, so the only way a call can be recorded below is if
    // the boot issued it WITHOUT waiting for the others.
    const settings = deferred<EffectiveSettings>();
    const identity = deferred<IdentityVerdict>();
    const chats = deferred<boolean>();
    const retention = deferred<undefined>();
    m.loadSettings.mockReturnValue(settings.promise);
    m.resolveIdentity.mockReturnValue(identity.promise);
    m.loadList.mockReturnValue(chats.promise);
    m.refreshRetention.mockReturnValue(retention.promise);

    const { startBoot } = await freshBoot();
    const booted = startBoot({ applyRoute: m.applyRoute });

    expect(m.loadSettings).toHaveBeenCalledTimes(1);
    expect(m.resolveIdentity).toHaveBeenCalledTimes(1);
    expect(m.loadList).toHaveBeenCalledTimes(1);
    expect(m.refreshRetention).toHaveBeenCalledTimes(1);
    expect(m.readBootSnapshot).toHaveBeenCalledTimes(1);
    // And nothing has been adopted, because nothing has answered.
    expect(m.renderIdentity).not.toHaveBeenCalled();
    expect(m.restoreAll).not.toHaveBeenCalled();

    settings.resolve(settingsPayload());
    identity.resolve(SIGNED_IN);
    chats.resolve(true);
    retention.resolve(undefined);
    await booted;
  });

  it("renders the identity row without waiting for the chat list", async () => {
    const chats = deferred<boolean>();
    m.loadList.mockReturnValue(chats.promise);

    const { startBoot } = await freshBoot();
    const booted = startBoot({ applyRoute: m.applyRoute });
    await vi.waitFor(() => {
      expect(m.renderIdentity).toHaveBeenCalledWith(SIGNED_IN);
    });
    // The strip is still pending: the tab set is adopted after the chat fold,
    // because a chat tab's row is named from the chat store.
    expect(m.listTabs).not.toHaveBeenCalled();

    chats.resolve(true);
    await booted;
    expect(m.listTabs).toHaveBeenCalledTimes(1);
  });

  it("restores settings without waiting for the identity verdict", async () => {
    const identity = deferred<IdentityVerdict>();
    m.resolveIdentity.mockReturnValue(identity.promise);

    const { startBoot } = await freshBoot();
    const booted = startBoot({ applyRoute: m.applyRoute });
    await vi.waitFor(() => {
      expect(m.restoreAll).toHaveBeenCalledTimes(1);
      expect(m.adoptThemeFromSettings).toHaveBeenCalledTimes(1);
    });
    // …and the workspace comes up too: the whole point of the fan-out is that a
    // 5-second whoami cannot hold the app back.
    await vi.waitFor(() => {
      expect(m.activateRestoredTab).toHaveBeenCalledTimes(1);
    });

    identity.resolve(SIGNED_IN);
    await booted;
  });
});

describe("the identity verdict gates one row", () => {
  it("comes up working when whoami is unavailable, and offers a re-read", async () => {
    m.resolveIdentity.mockResolvedValue({ state: "unavailable", reason: "timed out" });

    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    // NOT a sign-out: no login modal over a working app.
    expect(m.showLoginModal).not.toHaveBeenCalled();
    // The app is interactive: the tab set was adopted and a tab activated.
    expect(m.listTabs).toHaveBeenCalledTimes(1);
    expect(m.activateRestoredTab).toHaveBeenCalledTimes(1);
    // And the post-auth fan-out ran, so the app is not half-booted.
    expect(m.initPostAuthUI).toHaveBeenCalledTimes(1);
    // With a way back to a real answer.
    expect(m.toastError).toHaveBeenCalledWith(
      expect.stringContaining("timed out"),
      expect.objectContaining({ label: "Retry" }),
    );
  });

  it("paints an EMPTY workspace's strip while whoami is still pending", async () => {
    // The one boot that used to wait: a first run has no chats, and the starter
    // chat needs the verdict — so the whole strip sat behind a read measured at a
    // 5-second timeout.
    const identity = deferred<IdentityVerdict>();
    m.resolveIdentity.mockReturnValue(identity.promise);
    m.getSessions.mockReturnValue([]);

    const { startBoot } = await freshBoot();
    const booted = startBoot({ applyRoute: m.applyRoute });

    await vi.waitFor(() => {
      expect(m.listTabs).toHaveBeenCalledTimes(1);
      expect(m.activateRestoredTab).toHaveBeenCalledTimes(1);
    });
    expect(m.createSession).not.toHaveBeenCalled();

    identity.resolve(SIGNED_IN);
    await booted;
    // And the chat still arrives, once the verdict says it may.
    expect(m.createSession).toHaveBeenCalledTimes(1);
  });

  it("raises the login modal on a sign-out and mints no chat behind it", async () => {
    m.resolveIdentity.mockResolvedValue({ state: "signed_out" });
    m.getSessions.mockReturnValue([]);

    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    expect(m.showLoginModal).toHaveBeenCalledTimes(1);
    // And the next boot must not paint this workspace at a login screen.
    expect(m.clearBootSnapshot).toHaveBeenCalledTimes(1);
    // A starter chat is `onLoginSuccess`'s to create, once the identity is real.
    expect(m.createSession).not.toHaveBeenCalled();
    // No post-auth fetches on the login screen.
    expect(m.initPostAuthUI).not.toHaveBeenCalled();
    // The held SSE frames are released anyway: nothing will hydrate the store
    // behind a login modal, and leaving the gate shut stalls the stream until the
    // watchdog fires.
    expect(m.markHydrated).toHaveBeenCalled();
  });

  it("says nothing about unreachable chats while the login modal is up", async () => {
    m.resolveIdentity.mockResolvedValue({ state: "signed_out" });
    m.loadList.mockResolvedValue(false);
    m.getSessions.mockReturnValue([]);

    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    expect(m.toastError).not.toHaveBeenCalledWith("Couldn't load your chats.", expect.anything());
  });

  it("complains about unreachable chats when someone IS signed in", async () => {
    m.loadList.mockResolvedValue(false);
    m.getSessions.mockReturnValue([]);

    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    expect(m.toastError).toHaveBeenCalledWith(
      "Couldn't load your chats.",
      expect.objectContaining({ label: "Reload" }),
    );
    // And the empty state still gets its fallback chat.
    expect(m.createSession).toHaveBeenCalledTimes(1);
  });
});

describe("the chat list is read once per cold boot", () => {
  it("does not re-read it on the connection the boot itself rides", async () => {
    const { startBoot, onTransportStatus } = await freshBoot();
    // app.ts opens the transport BEFORE the boot runs, so this is the order a
    // cold load produces. The hook used to fire `loadList` here as well, which is
    // what made every boot fetch the whole chat list twice.
    onTransportStatus("connected");
    await startBoot({ applyRoute: m.applyRoute });

    expect(m.loadList).toHaveBeenCalledTimes(1);
  });

  it("re-reads it on a RE-connect, which is what the hook is for", async () => {
    const { startBoot, onTransportStatus } = await freshBoot();
    onTransportStatus("connected");
    await startBoot({ applyRoute: m.applyRoute });
    onTransportStatus("disconnected");
    onTransportStatus("connected");

    expect(m.loadList).toHaveBeenCalledTimes(2);
  });

  it("recovers a read that FAILED while the stream was already up", async () => {
    // THE CASE NOTHING ELSE COVERS, and the reason the latch carries an answer
    // rather than only "has it settled". The connection the boot rides arrives
    // while the read is in flight, so the hook skips it; the read then fails; and
    // the stream never dropped, so no later `connected` arrives to carry the fetch.
    // The store kept whatever the snapshot painted until the user took the toast's
    // Reload.
    m.loadList.mockResolvedValue(false);
    m.getSessions.mockReturnValue([]);

    const { startBoot, onTransportStatus } = await freshBoot();
    onTransportStatus("connected");
    await startBoot({ applyRoute: m.applyRoute });

    // The boot's own read, plus the recovery it triggered. No third: the hook
    // skipped the connect, because at that moment the read had not settled.
    expect(m.loadList).toHaveBeenCalledTimes(2);
  });

  it("does not recover a read that SUCCEEDED", async () => {
    const { startBoot, onTransportStatus } = await freshBoot();
    onTransportStatus("connected");
    await startBoot({ applyRoute: m.applyRoute });

    expect(m.loadList).toHaveBeenCalledTimes(1);
  });

  it("does not recover a failed read with no stream up, because a connect will cover it", async () => {
    // The offline boot below: nothing to fetch over yet, so the recovery must not
    // fire a request into a dead link — the first `connected` is what covers it.
    m.loadList.mockResolvedValue(false);
    m.getSessions.mockReturnValue([]);

    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    expect(m.loadList).toHaveBeenCalledTimes(1);
  });

  it("reads it on the FIRST connect when the boot's own read failed", async () => {
    // An offline boot: every read fails and the EventSource never opens. The link
    // comes up seconds later and the stream connects for the first time, carrying no
    // Last-Event-ID — so nothing declares a gap, and the boot's own read answered
    // nothing. This is the one connect that has to fetch, and a latch on "a
    // connection happened" swallowed it, leaving the store empty behind a toast.
    m.loadList.mockResolvedValue(false);
    m.getSessions.mockReturnValue([]);

    const { startBoot, onTransportStatus } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });
    expect(m.loadList).toHaveBeenCalledTimes(1);

    onTransportStatus("connected");

    expect(m.loadList).toHaveBeenCalledTimes(2);
  });
});

// Two windows, and every boot-time URL write falls on one side or the other.
// `pushRoute`/`replaceRoute` return early while the depth is above zero, so the
// depth a call is made at IS whether it lands: the activation must not write, and
// the canonicalization must.
describe("the push-suppression window", () => {
  it("covers the resumed activation, so a deep-linked launch survives to be read", async () => {
    // `activateRestoredTab` ends in `pushRoute` (tabs.ts). Unsuppressed it adds a
    // history entry Back walks into, and rewrites location.pathname before
    // `applyInitialRoute` parses it — resolving a launch at /chat/{id} to whatever
    // tab the snapshot was last on.
    m.paintBootSnapshot.mockReturnValue(true);

    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    expect(m.depths.activate).toEqual([1]);
  });

  it("covers the tab set's own activation when there was nothing to resume", async () => {
    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    expect(m.depths.activate).toEqual([1]);
  });

  it("is CLOSED before the URL is canonicalized", async () => {
    // A boot at "/" with a chat on screen. `applyInitialRoute`'s `replaceRoute` is
    // the one write that makes the address bar agree with what is visible, and the
    // restored tab's own boot-time push was suppressed — so suppressing this too
    // leaves the URL naming nothing.
    m.getActiveId.mockReturnValue("c1");
    m.getActive.mockReturnValue({ id: "c1" });

    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    expect(m.replaceRoute).toHaveBeenCalledWith({ kind: "chat", id: "c1" });
    expect(m.depths.replaceRoute).toEqual([0]);
  });

  it("is CLOSED before a share is delivered", async () => {
    // A `?agent=planner` launch creates a chat and activates it; inside the window
    // that activation's push AND the canonicalization that would name it are both
    // no-ops, so the launch ends on a URL naming nothing.
    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    expect(m.depths.share).toEqual([0]);
  });
});

// A resume paints what this screen was showing before the network answers. The
// ordering is the whole property: the paint happens ahead of the chat fold, and
// the boot's own activation is not run a second time over it.
describe("the local snapshot", () => {
  it("drops the hint when the chat list answers first", async () => {
    // The interleaving a resume produces: a warm server against a cold IndexedDB
    // open. `paintBootSnapshot` REPLACES the chat store, so painting here would
    // substitute the hint for the answer it was supposed to be superseded by — and
    // nothing on the boot path reads the list again.
    const snapshot = deferred<BootSnapshot | null>();
    m.readBootSnapshot.mockReturnValue(snapshot.promise);

    const { startBoot } = await freshBoot();
    const booted = startBoot({ applyRoute: m.applyRoute });

    // The chat list landed, and the boot moved on rather than waiting on the hint.
    await vi.waitFor(() => {
      expect(m.listTabs).toHaveBeenCalledTimes(1);
    });

    snapshot.resolve(SNAPSHOT);
    await booted;

    // The record arrived, and was never painted.
    expect(m.paintBootSnapshot).toHaveBeenCalledTimes(1);
    expect(m.paintBootSnapshot).toHaveBeenCalledWith(null);
  });

  it("paints the hint when the chat list FAILS to answer", async () => {
    // The resume the snapshot exists for: an unreachable server. `loadList` resolves
    // FALSE rather than rejecting and retries nothing, so it settles well ahead of a
    // cold IndexedDB open — and counting that as an answer discarded the hint on the
    // one boot with nothing else to show, then minted a starter chat over the empty
    // store.
    const snapshot = deferred<BootSnapshot | null>();
    m.loadList.mockResolvedValue(false);
    m.readBootSnapshot.mockReturnValue(snapshot.promise);
    m.paintBootSnapshot.mockReturnValue(true);

    const { startBoot } = await freshBoot();
    // A macrotask out, which is what makes the failed fetch the first to settle.
    // Armed AFTER the import: the import spans macrotasks of its own, so a timer
    // set before it fires inside it and both arms are already settled by the time
    // the race is built — which is not the interleaving under test.
    setTimeout(() => {
      snapshot.resolve(SNAPSHOT);
    }, 0);
    await startBoot({ applyRoute: m.applyRoute });

    expect(m.paintBootSnapshot).toHaveBeenCalledWith(SNAPSHOT);
  });

  it("finishes the workspace when the hint's paint throws", async () => {
    // A hint is best-effort: everything below it in `restoreWorkspace` is the
    // authoritative restore, and a throw here used to skip the lot — no
    // `markHydrated` (so every held SSE frame waits out the 20s timeout), no tab
    // set, no route, and a tab strip shimmering forever.
    m.paintBootSnapshot.mockImplementation(() => {
      throw new Error("a row would not build");
    });
    const skeleton = document.createElement("div");
    skeleton.id = "tab-strip-skeleton";
    document.body.appendChild(skeleton);

    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    expect(m.markHydrated).toHaveBeenCalled();
    expect(m.listTabs).toHaveBeenCalledTimes(1);
    expect(m.activateRestoredTab).toHaveBeenCalledTimes(1);
    expect(document.getElementById("tab-strip-skeleton")).toBeNull();
  });

  it("falls back to the tab set's activation when the resumed one throws", async () => {
    // A resume that got as far as its activation and failed there has NOT restored
    // the workspace, so the tab set's own activation must still run — which is why
    // `resumed` is only true once the whole hint path completed.
    m.paintBootSnapshot.mockReturnValue(true);
    m.activateRestoredTab.mockImplementationOnce(() => {
      throw new Error("onShow rejected");
    });

    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    expect(m.activateRestoredTab).toHaveBeenCalledTimes(2);
    expect(m.listTabs).toHaveBeenCalledTimes(1);
  });

  it("stops capturing and drops the record when the user logs out", async () => {
    // The door with a button. The page keeps running after a logout, so a live
    // capture goes on writing a signed-out user's workspace to disk and the next
    // boot paints it before whoami can say `signed_out`.
    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });
    expect(m.clearBootSnapshot).not.toHaveBeenCalled();

    const listener = m.subscribeByName.mock.calls.find(([name]) => name === "settings.logout")?.[1];
    expect(listener).toBeDefined();
    listener?.({ status: "success" });

    expect(m.clearBootSnapshot).toHaveBeenCalledTimes(1);
  });

  it("forgets every per-device record on a logout, not only the snapshot", async () => {
    // FOUR RECORDS, THREE OWNERS. The snapshot is the one this phase added; the
    // three localStorage blobs (the UI-state document, the turn folds, the
    // dismissed banners) predate it and a sign-out left all three. Clearing the
    // fold KEY alone forgets nothing either — `persist` rewrites the whole document
    // out of the in-memory map, so the next fold would put the previous user's
    // folds straight back.
    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    const listener = m.subscribeByName.mock.calls.find(([name]) => name === "settings.logout")?.[1];
    listener?.({ status: "success" });

    expect(m.clearDeviceKeys).toHaveBeenCalledTimes(1);
    expect(m.resetFoldState).toHaveBeenCalledTimes(1);
  });

  it("forgets every per-device record on a signed_out boot too", async () => {
    m.resolveIdentity.mockResolvedValue({ state: "signed_out" });

    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    expect(m.clearBootSnapshot).toHaveBeenCalledTimes(1);
    expect(m.clearDeviceKeys).toHaveBeenCalledTimes(1);
    expect(m.resetFoldState).toHaveBeenCalledTimes(1);
  });

  it("restarts the capture on a login in the same page, after a logout stopped it", async () => {
    // `initPostAuth` is latched, and the capture used to sit INSIDE the latch: a
    // signed-in boot spent the latch, the logout disposed the capture, and
    // `onLoginSuccess`'s call was then a no-op — so the record stayed absent for the
    // rest of the page's life. The capture's lifetime is the SESSION, not the page.
    const { startBoot, initPostAuth } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });
    expect(m.startBootSnapshot).toHaveBeenCalledTimes(1);

    const listener = m.subscribeByName.mock.calls.find(([name]) => name === "settings.logout")?.[1];
    listener?.({ status: "success" });

    // The login door, which is the same door the boot came through.
    initPostAuth();
    expect(m.startBootSnapshot).toHaveBeenCalledTimes(2);
    // And nothing behind the latch runs twice.
    expect(m.initPostAuthUI).toHaveBeenCalledTimes(1);
  });

  it("keeps capturing when a logout FAILS, because the user is still signed in", async () => {
    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    const listener = m.subscribeByName.mock.calls.find(([name]) => name === "settings.logout")?.[1];
    listener?.({ status: "error" });

    expect(m.clearBootSnapshot).not.toHaveBeenCalled();
  });

  it("paints the strip and activates it before the chat list answers", async () => {
    const chats = deferred<boolean>();
    m.loadList.mockReturnValue(chats.promise);
    m.paintBootSnapshot.mockReturnValue(true);
    const skeleton = document.createElement("div");
    skeleton.id = "tab-strip-skeleton";
    document.body.appendChild(skeleton);

    const { startBoot } = await freshBoot();
    const booted = startBoot({ applyRoute: m.applyRoute });

    await vi.waitFor(() => {
      expect(m.activateRestoredTab).toHaveBeenCalledTimes(1);
    });
    // The placeholder is gone because real rows replaced it, not because an answer
    // landed — none has.
    expect(document.getElementById("tab-strip-skeleton")).toBeNull();
    expect(m.listTabs).not.toHaveBeenCalled();

    chats.resolve(true);
    await booted;
  });

  it("activates ONCE when it painted, so the transcript is fetched once", async () => {
    m.paintBootSnapshot.mockReturnValue(true);

    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    // The tab set still lands and still reconciles; what it must not do is re-run
    // the activation, whose onShow is a second /api/chats/{id}.
    expect(m.listTabs).toHaveBeenCalledTimes(1);
    expect(m.activateRestoredTab).toHaveBeenCalledTimes(1);
  });

  it("activates after the tab set when there was nothing to resume", async () => {
    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    expect(m.paintBootSnapshot).toHaveBeenCalledWith(null);
    expect(m.activateRestoredTab).toHaveBeenCalledTimes(1);
  });

  it("mints no starter chat over rows a snapshot painted", async () => {
    // The chat list could not be read, but the screen is not blank: the snapshot's
    // rows are in the store, so there is nothing for a fallback chat to fix.
    m.loadList.mockResolvedValue(false);
    m.paintBootSnapshot.mockReturnValue(true);
    m.getSessions.mockReturnValue([{ id: "c1" }]);

    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    expect(m.toastError).toHaveBeenCalledWith(
      "Couldn't load your chats.",
      expect.objectContaining({ label: "Reload" }),
    );
    expect(m.createSession).not.toHaveBeenCalled();
  });
});

describe("the tab strip's pending state", () => {
  it("drops the authored skeleton once the tab set has been answered", async () => {
    const skeleton = document.createElement("div");
    skeleton.id = "tab-strip-skeleton";
    document.body.appendChild(skeleton);

    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    expect(document.getElementById("tab-strip-skeleton")).toBeNull();
  });

  it("drops it even when the tab set cannot be read", async () => {
    const skeleton = document.createElement("div");
    skeleton.id = "tab-strip-skeleton";
    document.body.appendChild(skeleton);
    m.listTabs.mockResolvedValue(false);

    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    expect(document.getElementById("tab-strip-skeleton")).toBeNull();
    expect(m.toastError).toHaveBeenCalledWith(
      "Couldn't restore your tabs.",
      expect.objectContaining({ label: "Reload" }),
    );
  });
});
