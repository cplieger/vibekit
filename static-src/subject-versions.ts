// The digest subjects' version map: one epoch-bound entry per subject this client holds
// (`chat:<id>`, `chats`, `live_turn:<id>`, `pending`, `runs`, `tabs`, `catalog`,
// `status`), fed by server-minted stamps and read by the wake digest.
//
// A LEAF. `store.ts` and `store-load.ts` both import it, so it may import neither; the
// stream that binds it to the hub's epoch lives in `sse-adapter.ts`.
import { type Subject, type VersionMap, createVersionMap } from "@cplieger/sse";

import type { SubjectStamp } from "./wire/types.gen.js";

/** Where a recorded stamp is reported beyond this map. Under the worker host the tab
 *  forwards it, so the host's map holds every version the profile's tabs hold and one
 *  digest serves them all. */
type ObserveSink = (subject: Subject, version: string, epoch: string) => void;

let map: VersionMap = createVersionMap();
let sink: ObserveSink | null = null;

/** The live map, for the stream that binds it and the digest that reads it. */
export function versionMap(): VersionMap {
  return map;
}

/** Install (or with `null` remove) the reporter every recorded stamp reaches. */
export function setObserveSink(fn: ObserveSink | null): void {
  sink = fn;
}

/** Record a stamp AFTER the state it certifies has been applied, never before and never
 *  from a digest answer. A REST stamp carries the epoch and binds an unbound map to it; a
 *  frame stamp carries none and rides the stream's own binding. A stamp from a foreign
 *  epoch is ignored by the map and reported through the stream's lifecycle feed. */
export function observeStamp(stamp: SubjectStamp | undefined): void {
  if (stamp === undefined) {
    return;
  }
  const subject: Subject = { kind: stamp.kind, ref: stamp.ref };
  const epoch = stamp.epoch === undefined || stamp.epoch === "" ? undefined : stamp.epoch;
  map.observe(subject, stamp.version, epoch);
  // Only what the map recorded travels: a foreign-epoch stamp recorded nothing, and an
  // entry in an unbound map has no epoch to present.
  const bound = map.epoch();
  if (sink !== null && bound !== null && (epoch === undefined || epoch === bound)) {
    sink(subject, stamp.version, bound);
  }
}

export function hasSubject(kind: string, ref: string): boolean {
  return map.has({ kind, ref });
}

export function forgetSubject(kind: string, ref: string): void {
  map.forget({ kind, ref });
}

/** A fresh, unbound map with no reporter. Test isolation only; production never resets. */
export function _resetForTest(): void {
  map = createVersionMap();
  sink = null;
}
