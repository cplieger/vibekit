// ---------------------------------------------------------------------------
// Degraded-runtime banner (degraded-not-dead start).
//
// The server installs kiro-cli itself, in the background, after the listener
// binds; until a version is active /api/health answers 503 with a reason naming
// the state (`kiro-cli installing`, `kiro-cli install retrying`,
// `kiro-cli unavailable`, `kiro-cli required settings not enforced`). This
// module surfaces those states as an app-global banner: the page works (files,
// git, settings, shell), only chats wait.
//
// Those four literals are the contract, and they have exactly one producer:
// `kiroReasonText` in internal/server/kirocli.go, which renders the install
// library's typed reason into vibekit's own wording. A rename there degrades
// every named state below to FALLBACK and nothing else notices, so
// TestKiroReasonTextIsTheClientContract pins the strings on the Go side —
// change them in both places or neither.
//
// Matching is by PREFIX, not by one literal. The reasons are a lifecycle, and a
// first boot legitimately spends minutes in `installing` — an equality check
// against a single reason would leave that window with no banner at all, so a
// user would see chats fail with the UI claiming everything is fine.
//
// The check runs at boot and again on every transport gap — a gap is exactly
// when the server may have restarted, i.e. when the state can have flipped in
// either direction, so the banner self-heals once the install completes (health
// re-reads the verdict per request).
// ---------------------------------------------------------------------------

import { apiGetOrError } from "./api-client.js";
import { showBanner, clearBannerCodes, GLOBAL_BANNER } from "./banner-stack.js";
import { onBus, BUS_TRANSPORT_GAP } from "./bus.js";
import { openSetting } from "./settings-highlight.js";
import type { BannerLevel } from "./types.js";

const CODE = "runtime_degraded";

/**
 * The reason prefix every kiro-cli readiness verdict shares. It is what
 * separates a kiro-cli verdict from the server's own startup/shutdown 503
 * (`starting up or shutting down`), which must NOT raise this banner.
 */
const KIRO_REASON_PREFIX = "kiro-cli";

interface RuntimeState {
  readonly message: string;
  readonly level: BannerLevel;
}

// Per-reason copy. Installing and retrying are expected states with nothing for
// the user to do, so they are informational; the two dead ends are errors.
const STATES: Record<string, RuntimeState> = {
  "kiro-cli installing": {
    message:
      "Agent runtime (kiro-cli) is downloading — chats can't start yet. " +
      "This is normal on a first boot; it's a few hundred megabytes and this banner clears itself.",
    level: "info",
  },
  "kiro-cli install retrying": {
    message:
      "Agent runtime (kiro-cli) install failed and is being retried — chats can't start yet. " +
      "No action needed unless it keeps failing (see container logs).",
    level: "info",
  },
  "kiro-cli required settings not enforced": {
    message:
      "Agent runtime (kiro-cli) is installed but its auto-update could not be switched off, " +
      "so chats stay blocked — a self-replacing binary would break the pinned version. See container logs.",
    level: "error",
  },
};

const FALLBACK: RuntimeState = {
  message:
    "Agent runtime (kiro-cli) is not installed — chats can't start. " +
    "The install failed and its retries are exhausted (see container logs); restart the container to try again.",
  level: "error",
};

/** Shape of /api/health's JSON envelope (both 200 and 503 bodies). */
interface HealthBody {
  status?: string;
  reason?: string;
}

function reasonOf(body: unknown): string {
  if (typeof body === "object" && body !== null) {
    const r = (body as HealthBody).reason;
    if (typeof r === "string") {
      return r;
    }
  }
  return "";
}

/** Probe /api/health once and reconcile the degraded banner. */
export async function checkRuntimeHealth(): Promise<void> {
  const res = await apiGetOrError<HealthBody>("/api/health");
  const reason = reasonOf(res.body);
  if (!res.ok && reason.startsWith(KIRO_REASON_PREFIX)) {
    // An unknown kiro-cli reason falls back to the terminal wording rather than
    // being ignored: a state the server can report and the client cannot name
    // still blocks chats, and saying so is better than silence.
    const state = STATES[reason] ?? FALLBACK;
    // Not dismissible: the condition blocks the product's core function, and it
    // clears itself on the next check after recovery.
    //
    // Every one of these states tells the reader to go and look at something —
    // the version pair, or the container logs — and Run Diagnostics is where
    // both live, so the banner links straight at that button instead of naming
    // a panel and leaving the reader to find it.
    showBanner(GLOBAL_BANNER, CODE, state.message, state.level, false, {
      label: "Run diagnostics",
      onClick: () => {
        openSetting("general", "diagnostics-run");
      },
    });
    return;
  }
  // Healthy, network failure, or a startup/shutdown 503: clear. The
  // transient cases re-assert on the next gap check if still degraded;
  // a stale banner over a working runtime is the worse failure mode.
  clearBannerCodes(GLOBAL_BANNER, [CODE]);
}

/** Wire the boot probe + transport-gap re-probes. Called once from app.ts. */
export function initRuntimeHealth(): void {
  void checkRuntimeHealth();
  onBus(BUS_TRANSPORT_GAP, () => {
    void checkRuntimeHealth();
  });
}
