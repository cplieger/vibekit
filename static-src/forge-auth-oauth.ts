// ---------------------------------------------------------------------------
// GitHub OAuth device-flow: polling, device-prompt rendering, and
// signal-based cancellation. Extracted from forge-auth.ts.
// ---------------------------------------------------------------------------

// signal.aborted defensive guards: the @typescript-eslint/no-unnecessary-condition
// rule sees AbortSignal.aborted as boolean (always defined) and flags the
// re-checks inside async loops as redundant, but the value flips between
// awaited microtasks — the re-check IS necessary to bail before mutating
// shared state. Suppressed file-wide.
/* eslint-disable @typescript-eslint/no-unnecessary-condition */

import { el } from "@cplieger/reactive";
import { apiPostTyped } from "./api-client.js";
import type { DeviceFlowResponse } from "./wire/types.gen.js";
import { decodePollResult } from "./wire/decoders.gen.js";
import { startDeviceFlow } from "./actions/forge.js";

export interface OAuthFlowDeps {
  /** Set status text in the host element. */
  setStatus: (host: HTMLElement, text: string, kind?: "ok" | "err" | "") => void;
  /** Mark a forge ID for expansion on next paint. */
  expandOnNextPaint: (id: string) => void;
  /** Trigger a full panel re-render. */
  renderForgesPanel: () => void;
}

let pollController: AbortController | null = null;

/** Abort any in-flight polling. Called from cleanup. */
export function abortPoll(): void {
  pollController?.abort();
  pollController = null;
}

const POLL_MAX_ATTEMPTS = 60;
const POLL_BACKOFF_CAP_SEC = 60;
const POLL_MIN_INTERVAL_SEC = 5;

export async function startGitHubDeviceFlow(host: HTMLElement, deps: OAuthFlowDeps): Promise<void> {
  // Abort any prior polling lifecycle before starting a new one.
  pollController?.abort();
  pollController = new AbortController();
  const signal = pollController.signal;

  deps.setStatus(host, "Contacting GitHub…");
  const start = await startDeviceFlow.dispatch(undefined);
  if (signal.aborted) {
    return;
  }
  if (start === null) {
    deps.setStatus(host, "Failed to start device flow.", "err");
    return;
  }
  renderDevicePrompt(host, start);
  void pollGitHubDevice(
    host,
    start.device_code,
    Math.max(start.interval, POLL_MIN_INTERVAL_SEC),
    signal,
    deps,
  );
}

/** Render the device-flow prompt (verification link, user code, copy
 *  button, status line) into `host`. Built with the `el()` factory so
 *  no untrusted value is ever parsed as HTML. Exported for unit tests
 *  of the inert-text / non-http(s)-link invariants. */
export function renderDevicePrompt(host: HTMLElement, start: DeviceFlowResponse): void {
  // Only render an anchor for http(s) URIs; any other scheme (or a
  // markup-injection payload) is shown as inert text. el() turns
  // strings into text nodes, never markup, so there is no XSS surface.
  const safeLink = /^https?:\/\//i.test(start.verification_uri);
  const uriNode: HTMLElement | string = safeLink
    ? el(
        "a",
        {
          className: "forge-device-link",
          target: "_blank",
          rel: "noreferrer",
          href: start.verification_uri,
        },
        start.verification_uri,
      )
    : start.verification_uri;
  const intro = el("p", null, "Open ", uriNode, " and enter:");

  const copyBtn = el(
    "button",
    { type: "button", className: "btn-small forge-copy-btn" },
    "Copy",
  ) as HTMLButtonElement;
  copyBtn.addEventListener("click", () => {
    void navigator.clipboard.writeText(start.user_code);
    copyBtn.textContent = "Copied";
    setTimeout(() => {
      copyBtn.textContent = "Copy";
    }, 2000);
  });
  const codeRow = el(
    "div",
    { className: "forge-device-code-row" },
    el("code", { className: "forge-device-code" }, start.user_code),
    copyBtn,
  );

  const status = el("div", { className: "forge-device-status" }, "Waiting for approval…");

  host.replaceChildren(el("div", { className: "forge-device-prompt" }, intro, codeRow, status));
}

async function pollGitHubDevice(
  host: HTMLElement,
  deviceCode: string,
  intervalSec: number,
  signal: AbortSignal,
  deps: OAuthFlowDeps,
): Promise<void> {
  const statusEl = host.querySelector<HTMLDivElement>(".forge-device-status");
  let attempts = 0;
  let backoff = intervalSec;

  while (!signal.aborted && host.isConnected) {
    await sleepWithSignal(backoff * 1000, signal);
    if (signal.aborted || !host.isConnected) {
      return;
    }
    attempts++;
    if (attempts > POLL_MAX_ATTEMPTS) {
      if (statusEl !== null) {
        statusEl.textContent = "Timed out waiting for approval. Try again.";
      }
      return;
    }
    const res = await apiPostTyped(
      "/api/forges/oauth/github/poll",
      {
        device_code: deviceCode,
      },
      decodePollResult,
      signal,
    );
    if (signal.aborted) {
      return;
    }
    if (res === null) {
      if (statusEl !== null) {
        statusEl.textContent = "Network error. Retrying…";
      }
      backoff = Math.min(backoff * 2, POLL_BACKOFF_CAP_SEC);
      continue;
    }
    // Reset backoff on successful network response.
    backoff = intervalSec;
    if (res.status === "complete") {
      if (statusEl !== null) {
        statusEl.textContent = "Connected.";
      }
      deps.expandOnNextPaint("github:github.com");
      deps.renderForgesPanel();
      return;
    }
    if (res.status === "expired") {
      if (statusEl !== null) {
        statusEl.textContent = "Device code expired. Try again.";
      }
      return;
    }
    if (res.status === "error") {
      if (statusEl !== null) {
        statusEl.textContent = `Error: ${res.error ?? "unknown"}`;
      }
      return;
    }
  }
}

/** Sleep for `ms` milliseconds, aborting early if the signal fires. */
function sleepWithSignal(ms: number, signal: AbortSignal): Promise<void> {
  if (signal.aborted) {
    return Promise.resolve();
  }
  return new Promise<void>((resolve) => {
    const ac = new AbortController();
    const t = setTimeout(() => {
      ac.abort();
      resolve();
    }, ms);
    signal.addEventListener(
      "abort",
      () => {
        clearTimeout(t);
        ac.abort();
        resolve();
      },
      { signal: ac.signal },
    );
  });
}
