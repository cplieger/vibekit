// @vitest-environment happy-dom
import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("./api-client.js", () => ({
  apiGetOrError: vi.fn(),
}));

vi.mock("./banner-stack.js", () => ({
  GLOBAL_BANNER: "*",
  showBanner: vi.fn(),
  clearBannerCodes: vi.fn(),
}));

const { mockOpenSetting } = vi.hoisted(() => ({ mockOpenSetting: vi.fn() }));
vi.mock("./settings-highlight.js", () => ({ openSetting: mockOpenSetting }));

// The sign-in CTA's destination. Mocked like the two above: a call into it is a
// command at this module's boundary, and the real one reads #login-modal out of
// the DOM registry.
const { mockShowLoginModal } = vi.hoisted(() => ({ mockShowLoginModal: vi.fn() }));
vi.mock("./modals.js", () => ({ showLoginModal: mockShowLoginModal }));

import { checkRuntimeHealth, runtimeStatusLine } from "./runtime-health.js";
import { apiGetOrError } from "./api-client.js";
import { showBanner, clearBannerCodes } from "./banner-stack.js";

const mockedGet = vi.mocked(apiGetOrError);
const mockedShow = vi.mocked(showBanner);
const mockedClear = vi.mocked(clearBannerCodes);

describe("runtime-health: degraded banner reconciliation", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("shows the global banner when health reports kiro-cli unavailable", async () => {
    mockedGet.mockResolvedValueOnce({
      ok: false,
      status: 503,
      data: null,
      error: "HTTP 503",
      body: { status: "unready", reason: "kiro-cli unavailable" },
    });
    await checkRuntimeHealth();
    expect(mockedShow).toHaveBeenCalledTimes(1);
    const [chatID, code, message, level, dismissible] = mockedShow.mock.calls[0]!;
    expect(chatID).toBe("*");
    expect(code).toBe("runtime_degraded");
    expect(message).toContain("kiro-cli");
    expect(message).toContain("restart the container");
    expect(level).toBe("error");
    expect(dismissible).toBe(false);
    // The sibling family is retired rather than left stacked: the health
    // envelope reports ONE reason, so the other family's banner is stale the
    // moment this one is true.
    expect(mockedClear).toHaveBeenCalledWith("*", ["runtime_signed_out"]);
  });

  // D115: every one of these states tells the reader to go and look at
  // something, and Run Diagnostics is where the version pair and the log
  // pointer live — so the banner jumps there instead of naming a panel and
  // leaving the reader to find it.
  it("carries an in-app jump to Run diagnostics", async () => {
    mockedGet.mockResolvedValueOnce({
      ok: false,
      status: 503,
      data: null,
      error: "HTTP 503",
      body: { status: "unready", reason: "kiro-cli installing" },
    });
    await checkRuntimeHealth();

    const link = mockedShow.mock.calls[0]?.[5];
    expect(link?.label).toBe("Run diagnostics");
    // An href would be dropped by isSafeURL (relative URLs throw in new URL),
    // so an in-app jump must be a callback, not a link.
    expect(link?.href).toBeUndefined();
    link?.onClick?.();
    expect(mockOpenSetting).toHaveBeenCalledWith("general", "diagnostics-run");
  });

  // The reasons are a LIFECYCLE, and a first boot spends minutes in the first
  // one. Matching a single literal would leave that window with no banner while
  // every chat fails, which is the regression this table exists to prevent.
  it.each([
    ["kiro-cli installing", "info", "downloading"],
    ["kiro-cli install retrying", "info", "retried"],
    ["kiro-cli required settings not enforced", "error", "auto-update"],
    ["kiro-cli some future state", "error", "not installed"],
  ])("banners the %s state", async (reason, wantLevel, wantText) => {
    mockedGet.mockResolvedValueOnce({
      ok: false,
      status: 503,
      data: null,
      error: "HTTP 503",
      body: { status: "unready", reason },
    });
    await checkRuntimeHealth();
    expect(mockedShow).toHaveBeenCalledTimes(1);
    const [, , message, level, dismissible] = mockedShow.mock.calls[0]!;
    expect(message).toContain(wantText);
    expect(level).toBe(wantLevel);
    expect(dismissible).toBe(false);
    expect(mockedClear).toHaveBeenCalledWith("*", ["runtime_signed_out"]);
  });

  it("clears the banner when health is ok", async () => {
    mockedGet.mockResolvedValueOnce({
      ok: true,
      status: 200,
      data: { status: "ok" },
      error: "",
    });
    await checkRuntimeHealth();
    expect(mockedShow).not.toHaveBeenCalled();
    expect(mockedClear).toHaveBeenCalledWith("*", ["runtime_degraded", "runtime_signed_out"]);
  });

  it("does not show the banner for a startup/shutdown 503", async () => {
    mockedGet.mockResolvedValueOnce({
      ok: false,
      status: 503,
      data: null,
      error: "HTTP 503",
      body: { status: "unready", reason: "starting up or shutting down" },
    });
    await checkRuntimeHealth();
    expect(mockedShow).not.toHaveBeenCalled();
    expect(mockedClear).toHaveBeenCalledWith("*", ["runtime_degraded", "runtime_signed_out"]);
  });

  it("does not show the banner on a plain network failure (no body)", async () => {
    mockedGet.mockResolvedValueOnce({
      ok: false,
      status: 0,
      data: null,
      error: "network error",
    });
    await checkRuntimeHealth();
    expect(mockedShow).not.toHaveBeenCalled();
    expect(mockedClear).toHaveBeenCalled();
  });
});

// D106. The second reason family, and the reason it is a family of its own: a
// signed-out runtime is not an install state, so it must not inherit the install
// copy — which would tell the reader to restart the container, the one action
// that cannot help.
describe("runtime-health: the sign-in family", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("banners a signed-out runtime under its own code with a sign-in CTA", async () => {
    mockedGet.mockResolvedValueOnce({
      ok: false,
      status: 503,
      data: null,
      error: "HTTP 503",
      body: { status: "unready", reason: "sign-in required" },
    });
    await checkRuntimeHealth();

    expect(mockedShow).toHaveBeenCalledTimes(1);
    const [chatID, code, message, level, dismissible, link] = mockedShow.mock.calls[0]!;
    expect(chatID).toBe("*");
    // Its own code, so clearing one family cannot silently drop the other.
    expect(code).toBe("runtime_signed_out");
    expect(message).toContain("signed out");
    // The install family's remedy must not appear here.
    expect(message).not.toContain("restart the container");
    expect(level).toBe("error");
    expect(dismissible).toBe(false);
    expect(link?.label).toBe("Sign in");
    // Not Run Diagnostics: the diagnostics panel can only report what the
    // banner already says, and signing in is the whole remedy.
    link?.onClick?.();
    expect(mockShowLoginModal).toHaveBeenCalledTimes(1);
    expect(mockOpenSetting).not.toHaveBeenCalled();
    // The install family's banner is retired, not stacked.
    expect(mockedClear).toHaveBeenCalledWith("*", ["runtime_degraded"]);
  });

  it("does not read a kiro-cli install reason as a sign-in one", async () => {
    mockedGet.mockResolvedValueOnce({
      ok: false,
      status: 503,
      data: null,
      error: "HTTP 503",
      body: { status: "unready", reason: "kiro-cli installing" },
    });
    await checkRuntimeHealth();
    expect(mockedShow.mock.calls[0]?.[1]).toBe("runtime_degraded");
    expect(mockedShow.mock.calls[0]?.[5]?.label).toBe("Run diagnostics");
  });

  it("clears the sign-in banner once a token vends again", async () => {
    mockedGet.mockResolvedValueOnce({
      ok: true,
      status: 200,
      data: { status: "ok" },
      error: "",
    });
    await checkRuntimeHealth();
    expect(mockedShow).not.toHaveBeenCalled();
    // Both families clear together on a healthy probe: the latch is not sticky,
    // so a recovered sign-in reports ok and the banner must not outlive it.
    expect(mockedClear).toHaveBeenCalledWith("*", ["runtime_degraded", "runtime_signed_out"]);
  });
});

// The status card's agent-runtime line. It had no writer from the first commit
// and rendered a literal "-" forever; these pin that it now follows the same
// verdict the banner does, so the popup and the banner cannot disagree about
// whether chats can start.
describe("runtime-health: the status card's agent-runtime line", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it.each([
    ["kiro-cli installing"],
    ["kiro-cli install retrying"],
    ["kiro-cli unavailable"],
    ["kiro-cli required settings not enforced"],
    ["kiro-cli some future state"],
  ])("renders the %s reason verbatim", async (reason) => {
    mockedGet.mockResolvedValueOnce({
      ok: false,
      status: 503,
      data: null,
      error: "HTTP 503",
      body: { status: "unready", reason },
    });
    await checkRuntimeHealth();
    // Verbatim on purpose, including an unknown state: the server already
    // phrases the reason as a status line, so a translation table here would
    // add a second vocabulary to keep in step and would render a future state
    // as the wrong one of today's.
    expect(runtimeStatusLine()).toBe(reason);
  });

  it("reads ready on a healthy probe", async () => {
    mockedGet.mockResolvedValueOnce({ ok: true, status: 200, data: { status: "ok" }, error: "" });
    await checkRuntimeHealth();
    expect(runtimeStatusLine()).toBe("kiro-cli ready");
  });

  it("reads signed out for the sign-in family", async () => {
    mockedGet.mockResolvedValueOnce({
      ok: false,
      status: 503,
      data: null,
      error: "HTTP 503",
      body: { status: "unready", reason: "sign-in required" },
    });
    await checkRuntimeHealth();
    expect(runtimeStatusLine()).toBe("kiro-cli signed out");
  });

  // Neither ready nor degraded, and the distinction matters: claiming "ready"
  // here would assert something the probe never established, and reusing a
  // degraded reason would blame kiro-cli for the server being mid-restart.
  it.each([
    ["a startup/shutdown 503", { status: "unready", reason: "starting up or shutting down" }],
    ["a network failure", undefined],
  ])("reads unknown for %s", async (_label, body) => {
    mockedGet.mockResolvedValueOnce({
      ok: false,
      status: body === undefined ? 0 : 503,
      data: null,
      error: "unreachable",
      ...(body === undefined ? {} : { body }),
    });
    await checkRuntimeHealth();
    expect(runtimeStatusLine()).toBe("kiro-cli unknown");
  });
});
