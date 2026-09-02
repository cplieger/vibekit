// The status card names WHICH build it is talking to, on both of its first two
// lines: the vibekit version on the connected line, the kiro-cli version on the
// agent-runtime line.
//
// The pair arrives after the card is painted (the server spawns a `--version`
// subprocess to answer it, and the transport reports `connected` within
// milliseconds of boot), so the interesting behaviour is entirely about the LATE
// arrival — a version that lands after the last status change still has to reach
// both lines, and neither line may claim a version it does not have.
import { describe, it, expect, beforeEach, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  apiGet: vi.fn<(path: string) => Promise<unknown>>(),
  apiGetOrError: vi.fn<(path: string) => Promise<{ ok: boolean; body: unknown }>>(),
}));

vi.mock("./api-client.js", () => ({
  apiGet: (path: string) => mocks.apiGet(path),
  apiGetOrError: (path: string) => mocks.apiGetOrError(path),
}));

// The banner stack and the login modal are the degraded path's collaborators;
// this file only ever drives the healthy one.
vi.mock("./banner-stack.js", () => ({
  showBanner: vi.fn(),
  clearBannerCodes: vi.fn(),
  GLOBAL_BANNER: "global",
}));
vi.mock("./modals.js", () => ({ showLoginModal: vi.fn() }));
vi.mock("./settings-highlight.js", () => ({ openSetting: vi.fn() }));
vi.mock("@cplieger/ui-primitives/announce", () => ({ announce: vi.fn() }));

const { setStatus, initStatusVersions, refreshRuntimeLine } = await import("./status.js");
const { loadVersions, getVersions, _resetVersionsForTest } = await import("./versions.js");

function card(): void {
  document.body.innerHTML = `
    <button id="status-dot"></button>
    <span id="status-card">
      <span id="st-ws">-</span><span id="st-kiro">-</span><span id="st-auth">-</span>
    </span>`;
}

function ws(): string {
  return document.getElementById("st-ws")?.textContent ?? "";
}
function kiro(): string {
  return document.getElementById("st-kiro")?.textContent ?? "";
}

beforeEach(() => {
  card();
  _resetVersionsForTest();
  mocks.apiGet.mockReset();
  mocks.apiGetOrError.mockReset();
  // Healthy runtime, so runtimeStatusLine() is on its READY branch.
  mocks.apiGetOrError.mockResolvedValue({ ok: true, body: {} });
});

/** What `GET /api/version` really answers for `kiro_cli`.
 *
 *  The server hands over the RAW `kiro-cli --version` stdout, and that binary
 *  prints its own name first — measured on the live install, `kiro-cli 2.20.2`.
 *  Every case below uses this rather than a bare number, because a fixture
 *  carrying the tidy value is a premise the backend does not hold: with it, a
 *  consumer that forgets to strip the name still passes here and renders
 *  `kiro-cli kiro-cli 2.20.1 ready` in the app. */
const KIRO_VERSION_STDOUT = "kiro-cli 2.20.1";

describe("the status card's version lines", () => {
  it("says only the status until the pair lands", () => {
    initStatusVersions();
    setStatus("connected");
    expect(ws()).toBe("connected");
    expect(kiro()).toBe("kiro-cli unknown");
  });

  it("names the vibekit build on the connected line once it lands", async () => {
    initStatusVersions();
    setStatus("connected");
    mocks.apiGet.mockResolvedValue({ vibekit: "v0.5.61", kiro_cli: KIRO_VERSION_STDOUT });
    await loadVersions();
    expect(ws()).toBe("connected to vibekit v0.5.61");
  });

  it("names the kiro-cli build on the ready line once it lands", async () => {
    initStatusVersions();
    mocks.apiGet.mockResolvedValue({ vibekit: "v0.5.61", kiro_cli: KIRO_VERSION_STDOUT });
    await loadVersions();
    await refreshRuntimeLine();
    expect(kiro()).toBe("kiro-cli 2.20.1 ready");
  });

  it("keeps a version off the disconnected line", async () => {
    // The line describes THIS page's socket; naming a build beside "disconnected"
    // would claim knowledge of a server it has just lost contact with.
    initStatusVersions();
    mocks.apiGet.mockResolvedValue({ vibekit: "v0.5.61", kiro_cli: KIRO_VERSION_STDOUT });
    await loadVersions();
    setStatus("disconnected");
    expect(ws()).toBe("disconnected");
    setStatus("connecting");
    expect(ws()).toBe("connecting");
  });

  it("falls back to the bare lines when the server answers no versions", async () => {
    initStatusVersions();
    mocks.apiGet.mockResolvedValue({});
    await loadVersions();
    setStatus("connected");
    await refreshRuntimeLine();
    expect(ws()).toBe("connected");
    expect(kiro()).toBe("kiro-cli ready");
  });

  it("leaves a degraded reason exactly as the server worded it", async () => {
    // Every non-ready line is the server's own wording rendered verbatim, and a
    // version appended to `kiro-cli installing` would name the pin rather than
    // anything running.
    initStatusVersions();
    mocks.apiGet.mockResolvedValue({ vibekit: "v0.5.61", kiro_cli: KIRO_VERSION_STDOUT });
    await loadVersions();
    mocks.apiGetOrError.mockResolvedValue({
      ok: false,
      body: { reason: "kiro-cli installing" },
    });
    await refreshRuntimeLine();
    expect(kiro()).toBe("kiro-cli installing");
  });

  it("reads /api/version exactly once per page load", async () => {
    mocks.apiGet.mockResolvedValue({ vibekit: "v0.5.61", kiro_cli: KIRO_VERSION_STDOUT });
    await loadVersions();
    await loadVersions();
    await loadVersions();
    expect(mocks.apiGet.mock.calls.filter((c) => c[0] === "/api/version")).toHaveLength(1);
  });

  it("drops the program name kiro-cli prints beside its version", async () => {
    // Both consumers already supply the word: the ready line reads
    // `kiro-cli <build> ready` and Settings → About labels the row `kiro-cli`.
    mocks.apiGet.mockResolvedValue({ vibekit: "v0.5.61", kiro_cli: KIRO_VERSION_STDOUT });
    await loadVersions();
    expect(getVersions().kiroCli).toBe("2.20.1");
  });

  it("keeps a build string that does not carry the program name", async () => {
    // A fork, or a future --version that prints the bare number. The strip is
    // that one exact token, not a hunt for a version-shaped substring.
    mocks.apiGet.mockResolvedValue({ vibekit: "v0.5.61", kiro_cli: "2.99.0-rc1" });
    await loadVersions();
    expect(getVersions().kiroCli).toBe("2.99.0-rc1");
  });
});
