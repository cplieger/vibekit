// What this connect declares as on screen: the one value `?snapshot=` carries. A LEAF, so
// the four states below are testable without dragging boot.ts's or transport.ts's graph
// into the test. `vibekit.md` records what each state costs.

import { bootMode } from "./reload-guard.js";
import { parseRoute } from "./router.js";
import { getActiveId } from "./store.js";

/** How the server hears "no chat needs its in-flight transcript". Must match `snapshotNone`
 *  in `internal/agent/sse.go`, and a Go test reads THIS FILE to hold the two spellings
 *  together. A WORD rather than an empty value: an empty-valued pair is the load-bearing
 *  character a URL canonicaliser drops silently, in the unsafe direction. */
export const SNAPSHOT_NONE = "none";

/** The chat whose in-flight transcript this connect needs, or the sentinel; `vibekit.md`
 *  records each rung's cost. NEVER `""`: that is the transport's unregistered default,
 *  which the server reads as "never declared" and answers with every open chat's snapshot.
 *  `transport.init` opens the EventSource before `startBoot` runs, so `getActiveId()` is
 *  `""` on a full boot's first connect and only the URL knows which chat is on screen. */
export function snapshotDeclaration(): string {
  // A REDUCED boot declares nothing at all: the bare busy signals are the whole point of
  // it, and this is the biggest byte lever the connect offers.
  if (bootMode() === "reduced") {
    return SNAPSHOT_NONE;
  }
  const active = getActiveId();
  if (active !== "") {
    return active;
  }
  const route = parseRoute(location.pathname);
  if (route.kind === "chat" && route.id !== "") {
    return route.id;
  }
  // BOTH kinds that name a chat: a subagent deep link's page renders the LAUNCHING chat's
  // blocks, so a delegate still streaming reaches this document only through `turn_state`,
  // and only if that chat was declared. It takes the DECLARED-ONE cost (`vibekit.md`).
  if (route.kind === "subagent" && route.chat !== "") {
    return route.chat;
  }
  return SNAPSHOT_NONE;
}
