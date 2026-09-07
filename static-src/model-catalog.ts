// ---------------------------------------------------------------------------
// The pre-session catalog's FETCH POLICY: which verdict is usable, how long to
// keep asking, and what to say when the asking stops.
//
// Here rather than in app.ts because a composition root cannot be tested, and
// those three decisions are where both previous rounds' defects were found. No
// DOM and no app.ts import: the reader and the sinks arrive as parameters.
// ---------------------------------------------------------------------------

import { pollUntil } from "./actions/index.js";
// Type-only, so picker.ts stays the one owner of the phase state and its copy.
import type { CatalogPhase } from "./picker.js";
import type { CatalogState } from "./wire/types.gen.js";

/** LONGER than the server's own 45s budget for this endpoint
 *  (configTemplateTimeout, internal/agent/config_template.go), which the
 *  library's 30s default made unreachable: the first call may spawn kiro-cli and
 *  unpack a ~240 MB KAS runtime, so the client used to abort every cold start
 *  before the server had finished. Move the two together. */
export const CATALOG_REQUEST_TIMEOUT_MS = 50_000;

// The four retry numbers, and the TOTAL BUDGET is the bound that actually binds.
// At ~50s per attempt plus the backed-off waits, 180s admits about three
// attempts, so MAX_ATTEMPTS is a ceiling that is never reached — kept as the
// guard against a pathologically fast failure (a 4xx answering in milliseconds)
// turning the budget into hundreds of requests.
const RETRY_INTERVAL_MS = 2_000;
const RETRY_BACKOFF = { factor: 2, maxMs: 30_000 };
const MAX_ATTEMPTS = 6;
const RETRY_BUDGET_MS = 180_000;

/** One catalog answer, narrowed to the field the retry policy reads. */
export interface CatalogAnswer {
  readonly catalog: CatalogState;
}

/** What a read's verdict means to the fetch policy: apply it, or keep asking. */
export type CatalogVerdict = "usable" | "retry";

/** The verdict mapping, total over the wire enum with no default arm — a fourth
 *  value is a compile error here rather than one the policy silently retries.
 *
 *  `empty` is USABLE for the same reason `ready` is: an empty catalog is a real
 *  answer KAS gave, and `_kiro/config/template` is a pure cache read that
 *  triggers no model refresh, so a second call re-reads the same empty cache.
 *  `unavailable` converges — the dominant real-world cause is a first call that
 *  never reached KAS at all. */
export function readVerdict(catalog: CatalogState): CatalogVerdict {
  switch (catalog) {
    case "ready":
    case "empty":
      return "usable";
    case "unavailable":
      return "retry";
  }
}

/** What one refresh needs from its caller. */
export interface CatalogRefresh<T extends CatalogAnswer> {
  /** One read of the endpoint. `null` is a transient failure (network, decode). */
  readonly read: (signal: AbortSignal) => Promise<T | null>;
  /** Apply a USABLE answer. Never called for a `retry` verdict, which is what
   *  keeps a degraded read from replacing an effort vocabulary and a mode list a
   *  successful boot fetch already landed. */
  readonly apply: (answer: T) => void;
  /** Record where the fetch has got to. */
  readonly setPhase: (phase: CatalogPhase) => void;
}

/** The single refresh slot, held for the life of one bounded loop. */
let inFlight: AbortController | undefined;

/** Refresh the catalog: one read, then a bounded retry on the one verdict that
 *  can converge, settling `unavailable` when the budget is spent.
 *
 *  `reset` RESTARTS a loop already running rather than joining or refusing it: a
 *  login is exactly the new information that may have fixed the read, and
 *  refusing it means it contributes nothing until a 180s loop exhausts. Every
 *  other caller (boot, a transport gap) declines, because a live loop is already
 *  asking the same endpoint. */
export async function refreshCatalog<T extends CatalogAnswer>(
  deps: CatalogRefresh<T>,
  opts: { readonly reset?: boolean } = {},
): Promise<void> {
  if (inFlight !== undefined) {
    if (opts.reset !== true) {
      return;
    }
    inFlight.abort();
  }
  const own = new AbortController();
  inFlight = own;
  try {
    await runRefresh(deps, own.signal);
  } finally {
    // Identity-guarded: a reset aborts the previous loop and claims the slot
    // immediately, so the loser's finally must not clear the winner's claim.
    if (inFlight === own) {
      inFlight = undefined;
    }
  }
}

async function runRefresh<T extends CatalogAnswer>(
  deps: CatalogRefresh<T>,
  signal: AbortSignal,
): Promise<void> {
  const first = await deps.read(signal);
  if (signal.aborted) {
    return;
  }
  if (first !== null && readVerdict(first.catalog) === "usable") {
    accept(deps, first);
    return;
  }
  const outcome = await pollUntil(deps.read, {
    intervalMs: RETRY_INTERVAL_MS,
    // Checked BEFORE onPoll, so a terminal result never reaches it — which is why
    // the success is applied from outcome.result below rather than there.
    until: (d) => readVerdict(d.catalog) === "usable",
    maxAttempts: MAX_ATTEMPTS,
    timeoutMs: RETRY_BUDGET_MS,
    backoff: RETRY_BACKOFF,
    signal,
  });
  if (outcome.status === "done") {
    accept(deps, outcome.result);
    return;
  }
  // Exhausted: settling the phase is what turns a permanent "Loading models…"
  // into a line that says the fetch failed. An ABORT reports nothing — a newer
  // loop owns the slot and will answer for itself, where settling here would
  // flash "couldn't load" over a live retry.
  if (outcome.status === "timeout") {
    deps.setPhase("unavailable");
  }
}

function accept<T extends CatalogAnswer>(deps: CatalogRefresh<T>, answer: T): void {
  deps.setPhase("ready");
  deps.apply(answer);
}
