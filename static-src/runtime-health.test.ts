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

import { checkRuntimeHealth } from "./runtime-health.js";
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
    expect(mockedClear).not.toHaveBeenCalled();
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
    expect(mockedClear).not.toHaveBeenCalled();
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
    expect(mockedClear).toHaveBeenCalledWith("*", ["runtime_degraded"]);
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
    expect(mockedClear).toHaveBeenCalledWith("*", ["runtime_degraded"]);
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
