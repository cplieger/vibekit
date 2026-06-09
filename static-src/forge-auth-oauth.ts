// ---------------------------------------------------------------------------
// GitHub OAuth device-flow: polling, device-prompt rendering, and
// signal-based cancellation. Extracted from forge-auth.ts.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { pollUntil } from "@cplieger/actions";
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
  // pollUntil has no host concept; the caller aborts `signal` on host
  // teardown, but every status write is also guarded by host.isConnected
  // so a detached node is never touched (mirrors the old loop, which
  // bailed without writing once host.isConnected went false).
  const setStatus = (text: string): void => {
    if (host.isConnected && statusEl !== null) {
      statusEl.textContent = text;
    }
  };

  const outcome = await pollUntil(
    (s) =>
      apiPostTyped(
        "/api/forges/oauth/github/poll",
        { device_code: deviceCode },
        decodePollResult,
        s,
      ),
    {
      intervalMs: intervalSec * 1000,
      // complete / expired / error are terminal; "pending" keeps polling.
      until: (r) => r.status !== "pending",
      maxAttempts: POLL_MAX_ATTEMPTS,
      backoff: { factor: 2, maxMs: POLL_BACKOFF_CAP_SEC * 1000 },
      // A null poll result is a network error: surface it, then back off.
      // The non-null "pending" case needs no status change (the
      // "Waiting for approval…" text stays), so onPoll is omitted.
      onTransientError: () => {
        setStatus("Network error. Retrying…");
      },
      signal,
    },
  );

  if (outcome.status === "aborted") {
    return; // caller cancelled / host torn down
  }
  if (outcome.status === "timeout") {
    setStatus("Timed out waiting for approval. Try again.");
    return;
  }
  // outcome.status === "done": inspect the terminal poll result.
  const res = outcome.result;
  if (res.status === "complete") {
    setStatus("Connected.");
    deps.expandOnNextPaint("github:github.com");
    deps.renderForgesPanel();
    return;
  }
  if (res.status === "expired") {
    setStatus("Device code expired. Try again.");
    return;
  }
  if (res.status === "error") {
    setStatus(`Error: ${res.error ?? "unknown"}`);
  }
}
