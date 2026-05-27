// @vitest-environment happy-dom
import { describe, it, expect, vi, beforeEach } from "vitest";

import {
  initStatusBanner,
  setBanner,
  clearBanner,
  _resetBannerState,
} from "./git-status-banner.js";

function setupDOM(): void {
  document.body.innerHTML = `<div id="git-status-banner" class="hidden"></div>`;
}

function bannerEl(): HTMLElement {
  return document.getElementById("git-status-banner") as HTMLElement;
}

function visibleState(): string | null {
  const row = bannerEl().querySelector<HTMLElement>(".git-status-banner-row");
  return row?.dataset["state"] ?? null;
}

describe("git-status-banner", () => {
  let onConnectForge: () => void;
  let onAuthenticateGh: () => void;

  beforeEach(() => {
    setupDOM();
    onConnectForge = vi.fn();
    onAuthenticateGh = vi.fn();
    _resetBannerState();
    initStatusBanner({ onConnectForge, onAuthenticateGh });
  });

  it("starts hidden when no state is set", () => {
    expect(bannerEl().classList.contains("hidden")).toBe(true);
    expect(visibleState()).toBeNull();
  });

  it("renders forge-auth-failed when set", () => {
    setBanner("forge-auth-failed");
    expect(bannerEl().classList.contains("hidden")).toBe(false);
    expect(visibleState()).toBe("forge-auth-failed");
    expect(bannerEl().textContent).toContain("Forge authentication issue");
    expect(bannerEl().querySelector<HTMLButtonElement>("[data-banner-cta]")?.textContent).toBe(
      "Open Settings → Git",
    );
  });

  it("renders gh-cli-missing when set", () => {
    setBanner("gh-cli-missing");
    expect(visibleState()).toBe("gh-cli-missing");
    expect(bannerEl().textContent).toContain("GitHub CLI not authenticated");
    expect(bannerEl().querySelector<HTMLButtonElement>("[data-banner-cta]")?.textContent).toBe(
      "Authenticate with gh",
    );
  });

  it("renders forges-not-connected when set", () => {
    setBanner("forges-not-connected");
    expect(visibleState()).toBe("forges-not-connected");
    expect(bannerEl().textContent).toContain("No forge connected");
  });

  it("forge-auth-failed wins over lower-priority states", () => {
    setBanner("gh-cli-missing");
    setBanner("forges-not-connected");
    setBanner("forge-auth-failed");
    expect(visibleState()).toBe("forge-auth-failed");
  });

  it("gh-cli-missing wins over forges-not-connected", () => {
    setBanner("forges-not-connected");
    setBanner("gh-cli-missing");
    expect(visibleState()).toBe("gh-cli-missing");
  });

  it("clearing the top state reveals the next-priority one", () => {
    setBanner("forge-auth-failed");
    setBanner("gh-cli-missing");
    expect(visibleState()).toBe("forge-auth-failed");

    clearBanner("forge-auth-failed");
    expect(visibleState()).toBe("gh-cli-missing");

    clearBanner("gh-cli-missing");
    expect(visibleState()).toBeNull();
    expect(bannerEl().classList.contains("hidden")).toBe(true);
  });

  it("dismiss hides the active state but keeps it logically active", () => {
    setBanner("forge-auth-failed");
    bannerEl().querySelector<HTMLButtonElement>("[data-banner-dismiss]")?.click();
    expect(visibleState()).toBeNull();
    expect(bannerEl().classList.contains("hidden")).toBe(true);
  });

  it("a higher-priority state arriving overrides a prior dismissal", () => {
    setBanner("forges-not-connected");
    bannerEl().querySelector<HTMLButtonElement>("[data-banner-dismiss]")?.click();
    expect(visibleState()).toBeNull();

    // Now a more urgent state appears: it should display, ignoring the
    // earlier dismissal of the lower-priority key.
    setBanner("forge-auth-failed");
    expect(visibleState()).toBe("forge-auth-failed");
  });

  it("re-setting a previously-cleared key shows the banner again", () => {
    setBanner("forge-auth-failed");
    bannerEl().querySelector<HTMLButtonElement>("[data-banner-dismiss]")?.click();
    expect(visibleState()).toBeNull();

    // Issue resolved, key cleared.
    clearBanner("forge-auth-failed");
    expect(visibleState()).toBeNull();

    // Issue happens again — dismissal is per-state-cycle, so the banner
    // should re-appear on a new occurrence.
    setBanner("forge-auth-failed");
    expect(visibleState()).toBe("forge-auth-failed");
  });

  it("set is idempotent and doesn't clobber dismissal of the same key", () => {
    setBanner("forge-auth-failed");
    bannerEl().querySelector<HTMLButtonElement>("[data-banner-dismiss]")?.click();
    expect(visibleState()).toBeNull();

    setBanner("forge-auth-failed"); // duplicate set
    expect(visibleState()).toBeNull(); // still dismissed
  });

  it("wires CTA: forge-auth-failed → onConnectForge", () => {
    setBanner("forge-auth-failed");
    bannerEl().querySelector<HTMLButtonElement>("[data-banner-cta]")?.click();
    expect(onConnectForge).toHaveBeenCalledTimes(1);
    expect(onAuthenticateGh).not.toHaveBeenCalled();
  });

  it("wires CTA: forges-not-connected → onConnectForge", () => {
    setBanner("forges-not-connected");
    bannerEl().querySelector<HTMLButtonElement>("[data-banner-cta]")?.click();
    expect(onConnectForge).toHaveBeenCalledTimes(1);
    expect(onAuthenticateGh).not.toHaveBeenCalled();
  });

  it("wires CTA: gh-cli-missing → onAuthenticateGh", () => {
    setBanner("gh-cli-missing");
    bannerEl().querySelector<HTMLButtonElement>("[data-banner-cta]")?.click();
    expect(onAuthenticateGh).toHaveBeenCalledTimes(1);
    expect(onConnectForge).not.toHaveBeenCalled();
  });

  it("clear is a no-op when the key isn't active", () => {
    expect(() => clearBanner("forge-auth-failed")).not.toThrow();
    expect(visibleState()).toBeNull();
  });

  it("renders only one row at a time even when multiple states active", () => {
    setBanner("forge-auth-failed");
    setBanner("gh-cli-missing");
    setBanner("forges-not-connected");
    expect(bannerEl().querySelectorAll(".git-status-banner-row").length).toBe(1);
    expect(visibleState()).toBe("forge-auth-failed");
  });
});
