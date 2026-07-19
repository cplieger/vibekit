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
