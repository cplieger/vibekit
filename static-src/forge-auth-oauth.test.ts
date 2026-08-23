// Tests for renderDevicePrompt's CSP-safe el()-built DOM: the user_code
// lands in a <code>, a verification_uri is only ever rendered as an
// anchor when it is http(s), and an attacker-controlled uri is inert
// text (never parsed as HTML).
import { describe, it, expect, vi } from "vitest";
import { renderDevicePrompt } from "./forge-auth-oauth.js";
import type { DeviceFlowResponse } from "./wire/types.gen.js";

function start(over: Partial<DeviceFlowResponse> = {}): DeviceFlowResponse {
  return {
    user_code: "WXYZ-1234",
    verification_uri: "https://github.com/login/device",
    device_code: "dev-code",
    interval: 5,
    expires_in: 900,
    ...over,
  };
}

describe("renderDevicePrompt", () => {
  it("renders the user_code in a code element and an http(s) anchor for a valid uri", () => {
    const host = document.createElement("div");
    renderDevicePrompt(host, start());

    const code = host.querySelector(".forge-device-code");
    expect(code?.tagName).toBe("CODE");
    expect(code?.textContent).toBe("WXYZ-1234");

    const link = host.querySelector<HTMLAnchorElement>("a.forge-device-link");
    expect(link).not.toBeNull();
    expect(link?.getAttribute("href")).toBe("https://github.com/login/device");
    expect(link?.textContent).toBe("https://github.com/login/device");
  });

  it("renders inert text for a verification_uri containing a markup-injection payload", () => {
    const evil = `"><img src=x onerror=alert(1)>`;
    const host = document.createElement("div");
    renderDevicePrompt(host, start({ verification_uri: evil }));

    // Non-http(s) "uri" => no anchor at all.
    expect(host.querySelector("a")).toBeNull();
    // The payload is never parsed as HTML — no injected element leaks in.
    expect(host.querySelector("img")).toBeNull();
    // It appears verbatim as text inside the intro paragraph.
    const p = host.querySelector("p");
    expect(p?.textContent).toContain(evil);
  });

  it("emits no anchor when the uri is non-http(s) (e.g. javascript:)", () => {
    const host = document.createElement("div");
    renderDevicePrompt(host, start({ verification_uri: "javascript:alert(1)" }));

    expect(host.querySelector("a")).toBeNull();
    // The would-be uri is rendered as plain text, not a clickable link.
    expect(host.querySelector("p")?.textContent).toContain("javascript:alert(1)");
  });

  it("copy button writes the user_code to the clipboard and shows transient feedback", () => {
    const writeText = vi.fn(() => Promise.resolve());
    vi.stubGlobal("navigator", { clipboard: { writeText } });
    vi.useFakeTimers();

    const host = document.createElement("div");
    renderDevicePrompt(host, start({ user_code: "ABCD-9999" }));

    const btn = host.querySelector<HTMLButtonElement>(".forge-copy-btn");
    expect(btn).not.toBeNull();
    btn?.click();

    expect(writeText).toHaveBeenCalledWith("ABCD-9999");
    expect(btn?.textContent).toBe("Copied");
    vi.advanceTimersByTime(2000);
    expect(btn?.textContent).toBe("Copy");

    vi.useRealTimers();
    vi.unstubAllGlobals();
  });
});
