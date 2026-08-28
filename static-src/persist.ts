// ---------------------------------------------------------------------------
// Settings persistence. Debounces PATCHes to coalesce rapid writes (e.g.
// theme toggle + notification toggle changes into one round-trip).
//
// Fetch calls go through api-client.ts for consistent error handling.
// ---------------------------------------------------------------------------

import { showSaving, showSaved, showError } from "./save-indicator.js";
import { patchAppSettings, loadSettings as loadSettingsAction } from "./actions/settings.js";
import { registerCleanup } from "./actions/index.js";
import type { EffectiveSettings } from "./wire/types.gen.js";

// The GET /api/settings payload. GENERATED from the Go struct
// vibekit.EffectiveSettings (see internal/wirespec), so it is not maintained
// here and cannot drift from what the server sends.
//
// Every field is REQUIRED, which is the whole point. The hand-written interface
// this replaced had all 15 fields optional, so each read site had to decide what
// an absent key meant and five of them answered by restating a server-side
// default — one of which (agent_ignore_files, whose fallback was the empty list
// against a server default of two patterns) rendered an empty chip row while the
// agent read filter was applying both, and then persisted that emptiness on the
// first edit. A required field is one a reader cannot supply a fallback for.
//
// The server guarantees it: GET resolves defaults underneath the stored file and
// validates each stored value against its field's TYPE, dropping a mismatch in
// favour of the default. Presence alone would not license deleting the guards —
// a required field is a compile-time claim with no runtime force — which is why
// the validation and this type landed together.
//
// A PATCH body is Partial<EffectiveSettings>: the full shape is what you read, a
// partial is what you write.
export type { EffectiveSettings } from "./wire/types.gen.js";

let patchTimer: ReturnType<typeof setTimeout> | undefined;
let patchQueue: Partial<EffectiveSettings> = {};
let patchSnapshot: Partial<EffectiveSettings> = {};
let patchInputs: HTMLInputElement[] = [];
let patchResolvers: ((r: Record<string, unknown> | null) => void)[] = [];

// The indicator belongs to the NEWEST write of each key.
//
// `patchAppSettings` is scope-serialized, so the second PATCH does not reach the
// network until the first answers — but the debounce timer does not wait for the
// network, so a second `executePatch` has already stamped its keys by then.
// Reporting the first response would say "Saved" about a value the server has
// not been asked about yet, and "failed" about one still in the air.
//
// Per KEY rather than one counter, because the indicators are per setting: a key
// the newer write does not carry has not been overtaken, and with a single
// counter its slot was left spinning forever.
let writeSeq = 0;
const keyGen = new Map<string, number>();

/** Last-known value per settings key. Seeded by initSettingsTracking()
 *  on app boot from /api/settings; updated by patchSettings() as we
 *  send writes. Used to filter no-op writes (same-value PATCHes that
 *  trigger the saving animation for nothing — e.g. the bootstrap
 *  fire of repo-picker's onSelectionChange persisting the already-
 *  saved git_repo on every page load). */
let lastSentPatch: Partial<EffectiveSettings> = {};

/** Seed the dedup tracker from the loaded settings. Called once at
 *  app boot before any patchSettings() can fire. Without this, the
 *  first patch for any key after page load is treated as a change
 *  even when the value matches the server. */
export function initSettingsTracking(s: EffectiveSettings): void {
  lastSentPatch = { ...s };
}

/** @internal Reset module state for tests. */
export function __testResetTracking(): void {
  lastSentPatch = {};
  patchQueue = {};
  patchSnapshot = {};
  patchInputs = [];
  patchResolvers = [];
  keyGen.clear();
  if (patchTimer !== undefined) {
    clearTimeout(patchTimer);
    patchTimer = undefined;
  }
}

/** Flush any pending debounced PATCH immediately (fire-and-forget). */
function flushPendingPatch(): void {
  if (Object.keys(patchQueue).length === 0) {
    return;
  }
  executePatch();
}

/** Shared dispatch body: drains the queue, dispatches the PATCH, and
 *  resolves all pending promises. Used by both flushPendingPatch (sync
 *  flush on beforeunload) and the debounce timer callback. */
function executePatch(): void {
  const body = patchQueue;
  const allInputs = patchInputs;
  const resolvers = patchResolvers;
  const rollback = patchSnapshot;
  patchQueue = {};
  patchSnapshot = {};
  patchInputs = [];
  patchResolvers = [];
  const keys = Object.keys(body);
  const gen = ++writeSeq;
  for (const k of keys) {
    keyGen.set(k, gen);
  }
  /** The keys of this write that no later write has claimed since. */
  const stillOurs = (): string[] => keys.filter((k) => keyGen.get(k) === gen);
  let result: Record<string, unknown> | null = null;
  void patchAppSettings.dispatch(
    {
      body: body,
      ...(allInputs.length > 0 ? { inputs: allInputs } : {}),
    },
    {
      silent: true,
      onSuccess: (r) => {
        result = r as Record<string, unknown>;
        const mine = stillOurs();
        if (mine.length > 0) {
          showSaved(mine);
        }
      },
      onError: () => {
        Object.assign(lastSentPatch, rollback);
        const mine = stillOurs();
        if (mine.length > 0) {
          showError(mine);
        }
      },
      onSettled: () => {
        for (const resolve of resolvers) {
          resolve(result);
        }
      },
    },
  );
}

registerCleanup(() => {
  if (patchTimer !== undefined) {
    clearTimeout(patchTimer);
    patchTimer = undefined;
    flushPendingPatch();
  }
});

export function patchSettings(
  patch: Partial<EffectiveSettings>,
  ...inputs: HTMLInputElement[]
): Promise<Record<string, unknown> | null> {
  // Filter out keys whose value matches the last-sent value. JSON
  // equality is good enough for the EffectiveSettings shape (primitives +
  // arrays of strings); avoids reflecting no-op writes back to the
  // server and prevents the "Saving..." animation from firing on
  // bootstrap subscriptions like onSelectionChange's immediate fire.
  const changed: Partial<EffectiveSettings> = {};
  for (const k of Object.keys(patch) as (keyof EffectiveSettings)[]) {
    if (JSON.stringify(patch[k]) !== JSON.stringify(lastSentPatch[k])) {
      if (!(k in patchSnapshot)) {
        Object.assign(patchSnapshot, { [k]: lastSentPatch[k] });
      }
      Object.assign(changed, { [k]: patch[k] });
      Object.assign(lastSentPatch, { [k]: patch[k] });
    }
  }
  if (Object.keys(changed).length === 0) {
    // No-op: nothing to send. Resolve immediately with null so callers
    // that await the promise don't hang.
    return Promise.resolve(null);
  }
  Object.assign(patchQueue, changed);
  for (const input of inputs) {
    if (!patchInputs.includes(input)) {
      patchInputs.push(input);
    }
  }
  const p = new Promise<Record<string, unknown> | null>((resolve) => {
    patchResolvers.push(resolve);
  });
  // Announced per call rather than per batch: each changed key has its own slot,
  // so a setting flipped while an earlier one is still inside the debounce
  // window has to raise its own spinner.
  showSaving(Object.keys(changed));
  if (patchTimer !== undefined) {
    return p;
  }
  patchTimer = setTimeout(() => {
    patchTimer = undefined;
    executePatch();
  }, 300);
  return p;
}

/** Load the effective settings, or null when the fetch itself failed.
 *
 *  Null is "I do not know what the settings are", which is a different state
 *  from any particular value and is why this is not `?? {}` any more. That
 *  fallback handed every caller an empty object indistinguishable from a real
 *  payload, so a network failure rendered as a full set of client-invented
 *  defaults — and it needed an eslint suppression to write, because the declared
 *  type promised what the wire did not. Both are gone.
 *
 *  A caller that gets null leaves its UI at its current state. `retention.ts`
 *  was doing that already, deliberately, and is the precedent the other two now
 *  follow.
 *
 *  Every field of a non-null result is present, and the action's generated
 *  decoder is what enforces it at this boundary: the server resolves defaults
 *  underneath the stored document and type-checks each stored value, and the
 *  decode rejects a payload that does not match. A decode failure, an empty
 *  body and a non-2xx all arrive here as null. */
export async function loadSettings(): Promise<EffectiveSettings | null> {
  return await loadSettingsAction.dispatch(undefined);
}
