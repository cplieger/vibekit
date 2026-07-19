// ---------------------------------------------------------------------------
// Degraded-runtime banner (P5, degraded-not-dead start).
//
// The entrypoint starts the server even when the first-boot kiro-cli
// install failed; /api/health then reports 503 {"status":"unready",
// "reason":"kiro-cli unavailable"}. This module surfaces that state as
// an app-global banner: the page works (files, git, settings, shell),
// but chats cannot run until the container restarts and retries the
// install. The check runs at boot and again on every transport gap —
// a gap is exactly when the server may have restarted, i.e. when the
// state can have flipped in either direction, so the banner self-heals
// once a restart recovers the binary (health re-probes per request).
// ---------------------------------------------------------------------------

import { apiGetOrError } from "./api-client.js";
import { showBanner, clearBannerCodes, GLOBAL_BANNER } from "./banner-stack.js";
import { onBus, BUS_TRANSPORT_GAP } from "./bus.js";

const CODE = "runtime_degraded";

const MESSAGE =
  "Agent runtime (kiro-cli) is not installed — chats can't start. " +
  "The first-boot install likely failed (see container logs); restart the container to retry.";

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
  if (!res.ok && reasonOf(res.body) === "kiro-cli unavailable") {
    // Not dismissible: the condition blocks the product's core
    // function, and it clears itself on the next check after recovery.
    showBanner(GLOBAL_BANNER, CODE, MESSAGE, "error", false);
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
