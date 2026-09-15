// The profile's stream owner, hosted in the SharedWorker: `@cplieger/sse`'s
// `createWorkerHost` around vibekit's two routes, plus the two decisions that are
// vibekit's own. `sse-worker.ts` is the classic-script entry that wires `onconnect`.
//
// The host holds the profile's version map (every tab reports each stamp it records)
// and performs ONE digest per run; the verdict rides the run to every tab, whose body
// (`sse-adapter.ts`) is the action column over it.

import {
  type DigestClient,
  type DigestVerdict,
  type LifecycleEvent,
  type RevalidateContext,
  type State,
  type Stream,
  type TabSet,
  type VersionMap,
  type WorkerHost,
  createDigestClient,
  createVersionMap,
  createWorkerHost,
} from "@cplieger/sse";

/** The digest's own budget, matching the server's `RouteTimeout` on `POST /api/sync`. */
const DIGEST_TIMEOUT_MS = 10_000;

/** No subject moved: a verdict the tabs apply as nothing. */
const NOTHING_MOVED: DigestVerdict = { changed: [], removed: [] };

/** Whether a subject's set reaches a client only through a hello's connect hook. */
function hookCarried(subject: State): boolean {
  return subject.kind === "pending" || subject.kind === "status";
}

/** The library's `revalidate` on the host side: one digest over the profile's map, then
 *  one run fanned to every acknowledging tab carrying the verdict, settled when each has
 *  answered, expired or left. A full run and an empty map skip the digest; the run still
 *  reaches the tabs, because a wake refreshes a tab's active view whatever the map holds.
 *  `must_refetch` binds the map to the new epoch and fans a full run at it. Rejects when
 *  the digest failed, so the library ends the connection and the next hello runs again.
 *  `pending` or `status` moved: ONE fresh hello for the profile, after every tab's GET
 *  settled; never on a hello's own run (`sse-adapter.ts` `helloIfMoved` has the loop). */
export async function profileRevalidate(
  ctx: RevalidateContext,
  tabs: TabSet,
  versions: VersionMap,
  digest: DigestClient,
  stream: Stream,
): Promise<void> {
  if (ctx.full) {
    await tabs.run(ctx);
    return;
  }
  const snapshot = versions.snapshot();
  if (snapshot.held.length === 0) {
    await tabs.run(ctx, NOTHING_MOVED);
    return;
  }
  const result = await digest.check(snapshot, ctx.signal);
  if (result.kind === "must_refetch") {
    versions.bind(result.epoch);
    await tabs.run({ ...ctx, epoch: result.epoch, full: true });
    return;
  }
  await tabs.run(ctx, { changed: result.changed, removed: result.removed });
  if (ctx.cause !== "hello" && result.changed.some(hookCarried)) {
    stream.resetCursor();
    stream.reconnect();
  }
}

/** Every tab that attaches to a LIVE stream needs the connect hook's two snapshot
 *  frames (`pending_snapshot`, `status_snapshot`): those sets reach a client only
 *  through the hook, and the hook ran once, for the tabs attached at that connect. So
 *  an attach while the stream is open reconnects it in place (a resumed hello, so the
 *  replay is gap-free); one that started or un-hid the stream adds nothing. Decided
 *  here, not by the joining tab: the record names no tab, so a tab cannot tell its own
 *  attach from another's, and N tabs each asking would open N connections. */
function reconnectForAttach(ev: LifecycleEvent, stream: Stream): void {
  if (ev.kind === "tab_attached" && ev.state === "open") {
    stream.reconnect();
  }
}

/** Build the host over vibekit's routes. */
export function createSSEHost(): WorkerHost {
  const versions = createVersionMap();
  const digest = createDigestClient({ url: "/api/sync", timeoutMs: DIGEST_TIMEOUT_MS });
  const created: WorkerHost = createWorkerHost({
    url: "/api/events",
    // `SSE-Client` is filled by the host from the first attaching tab's tag, before
    // the first connect; an empty header would reach the server as an invalid tag.
    alive: { url: "/api/events/alive" },
    versions,
    revalidate: (ctx, tabs) => profileRevalidate(ctx, tabs, versions, digest, created.stream()),
    onLifecycle(ev) {
      reconnectForAttach(ev, created.stream());
    },
  });
  return created;
}
