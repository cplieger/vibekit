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
//
// There are TWO reason families, and they are separate because their remedies
// are: the install family above ("kiro-cli ...", nothing for the user to do but
// wait or read logs) and the sign-in family ("sign-in required", from
// internal/server/authready.go, whose only fix is the login modal). A signed-out
// runtime looks nothing like a missing one to the user, which is why the second
// literal deliberately does not carry the first's prefix.
// ---------------------------------------------------------------------------

import { apiGetOrError } from "./api-client.js";
import { showBanner, clearBannerCodes, GLOBAL_BANNER } from "./banner-stack.js";
import { onBus, BUS_TRANSPORT_GAP } from "./bus.js";
import { openSetting } from "./settings-highlight.js";
import { showLoginModal } from "./modals.js";
import type { BannerLevel } from "./types.js";

const CODE = "runtime_degraded";

/** The sign-in family's own banner code, separate from the install family's so
 *  clearing one cannot silently drop the other. */
const AUTH_CODE = "runtime_signed_out";

/**
 * The reason prefix every kiro-cli readiness verdict shares. It is what
 * separates a kiro-cli verdict from the server's own startup/shutdown 503
 * (`starting up or shutting down`), which must NOT raise this banner.
 */
const KIRO_REASON_PREFIX = "kiro-cli";

/**
 * The sign-in family's prefix (internal/server/authready.go `reasonSignIn`).
 *
 * A SECOND family rather than another key under the one above, and the
 * distinction is why the literal deliberately avoids the "kiro-cli" prefix: this
 * is not an install state, it is a live runtime whose login chain has expired,
 * and its only fix is signing in. Routed under the install prefix it would match
 * the family, miss every named state, and render the terminal "restart the
 * container" copy — telling the reader to do the one thing that cannot help.
 */
const AUTH_REASON_PREFIX = "sign-in required";

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

// --- The status popup's agent-runtime line ---
//
// The sidebar status card reads connection / agent runtime / account, and the
// middle line had no writer from the first commit — it showed a literal "-"
// forever, because the readiness verdict it wanted did not exist until the
// install moved into the server. It lives HERE rather than in status.ts for the
// reason the header gives: this module is the only reader of the /api/health
// reason vocabulary, and a second one is exactly the drift that comment warns
// about.
//
// A kiro-cli reason renders VERBATIM. The server already phrases it as a status
// line ("kiro-cli installing", "kiro-cli unavailable"), so restating it here
// would add a translation table with nothing to say. Only the three states that
// carry no reason are named below.

const LINE_READY = "kiro-cli ready";
const LINE_SIGNED_OUT = "kiro-cli signed out";

/** Neither ready nor degraded: the server is starting up or shutting down, or
 *  the probe never reached it. The connection line above carries that story. */
const LINE_UNKNOWN = "kiro-cli unknown";

let runtimeLine = LINE_UNKNOWN;

/** The agent-runtime line as of the last probe. Painted by status.ts. */
export function runtimeStatusLine(): string {
  return runtimeLine;
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

const SIGNED_OUT: RuntimeState = {
  message:
    "The agent runtime is signed out, so chats will open and then fail. " +
    "This usually means the sign-in expired while you were away.",
  level: "error",
};

/** Probe /api/health once and reconcile the degraded banner. */
export async function checkRuntimeHealth(): Promise<void> {
  const res = await apiGetOrError<HealthBody>("/api/health");
  const reason = reasonOf(res.body);
  if (!res.ok && reason.startsWith(AUTH_REASON_PREFIX)) {
    runtimeLine = LINE_SIGNED_OUT;
    // Not dismissible, for the same reason as the install family: it blocks the
    // product's core function and clears itself on the next check once a token
    // vends. The CTA is the login modal rather than Run Diagnostics — the
    // diagnostics panel can only report what this banner already says, and
    // signing in is the whole remedy.
    clearBannerCodes(GLOBAL_BANNER, [CODE]);
    showBanner(GLOBAL_BANNER, AUTH_CODE, SIGNED_OUT.message, SIGNED_OUT.level, false, {
      label: "Sign in",
      onClick: showLoginModal,
    });
    return;
  }
  if (!res.ok && reason.startsWith(KIRO_REASON_PREFIX)) {
    runtimeLine = reason;
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
    clearBannerCodes(GLOBAL_BANNER, [AUTH_CODE]);
    showBanner(GLOBAL_BANNER, CODE, state.message, state.level, false, {
      label: "Run diagnostics",
      onClick: () => {
        openSetting("general", "diagnostics-run");
      },
    });
    return;
  }
  // Healthy, network failure, or a startup/shutdown 503: clear BOTH families.
  // The transient cases re-assert on the next gap check if still degraded;
  // a stale banner over a working runtime is the worse failure mode.
  runtimeLine = res.ok ? LINE_READY : LINE_UNKNOWN;
  clearBannerCodes(GLOBAL_BANNER, [CODE, AUTH_CODE]);
}

/** Wire the boot probe + transport-gap re-probes. Called once from app.ts. */
export function initRuntimeHealth(): void {
  void checkRuntimeHealth();
  onBus(BUS_TRANSPORT_GAP, () => {
    void checkRuntimeHealth();
  });
}
