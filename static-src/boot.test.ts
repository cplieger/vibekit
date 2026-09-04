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

const m = vi.hoisted(() => ({
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
  activateRestoredTab: vi.fn(),
  markHydrated: vi.fn(),
  markBootDone: vi.fn(),
  applyShareTarget: vi.fn(),
  toastError: vi.fn(),
  getSessions: vi.fn(),
  setStatus: vi.fn(),
  applyRoute: vi.fn(),
}));

vi.mock("./store-load.js", () => ({ loadList: m.loadList }));
vi.mock("./persist.js", () => ({}));
vi.mock("./store.js", () => ({
  getActive: () => undefined,
  getActiveId: () => "",
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
  getActiveTabRoute: () => null,
}));
vi.mock("./tabs-sync.js", () => ({ listTabs: m.listTabs }));
vi.mock("./router.js", () => ({
  parseRoute: () => ({ kind: "chat", id: "" }),
  replaceRoute: vi.fn(),
  suppressPush: vi.fn(),
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

/** The default happy answers: settings load, one chat, the tab set adopts. */
function arrangeHappy(settings: EffectiveSettings = settingsPayload()): void {
  m.loadSettings.mockResolvedValue(settings);
  m.resolveIdentity.mockResolvedValue(SIGNED_IN);
  m.loadList.mockResolvedValue(true);
  m.refreshRetention.mockResolvedValue(undefined);
  m.listTabs.mockResolvedValue(true);
  m.applyShareTarget.mockResolvedValue(undefined);
  m.createSession.mockResolvedValue(undefined);
  m.getSessions.mockReturnValue([{ id: "c1" }]);
}

beforeEach(() => {
  vi.clearAllMocks();
  arrangeHappy();
});

describe("the boot issues its reads together", () => {
  it("has all four in flight before any of them answers", async () => {
    // Held open, all four, so the only way a call can be recorded below is if the
    // boot issued it WITHOUT waiting for the others.
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

  it("raises the login modal on a sign-out and mints no chat behind it", async () => {
    m.resolveIdentity.mockResolvedValue({ state: "signed_out" });
    m.getSessions.mockReturnValue([]);

    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    expect(m.showLoginModal).toHaveBeenCalledTimes(1);
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
