// The keepalive acknowledgement and the presence row it feeds, against the REAL vibekit
// binary (design 12.8, the browser-mode list's two presence bullets). A connected client
// that acknowledges reads present; one whose acknowledgements stop while its socket
// keeps reading keepalives reads gone at the alive window, with its socket still
// counted, which is the whole reason the acknowledgement exists: the socket alone
// cannot see a suspended page.
//
// The acknowledgement is switched off by a fixture flag on the INJECTED fetch, which
// answers the alive POST locally instead of sending it, so the stream keeps reading
// keepalives exactly as a locked phone's kernel keeps receiving them. The presence row
// is read through the test-only probe. Real time: the fixture runs at the production
// keepalive, so the gone half of this file waits the alive window out.

import { afterEach, beforeEach, describe, expect, inject, it } from "vitest";

import {
  type LifecycleEvent,
  type Stream,
  createOnlineManager,
  createStream,
  createVersionMap,
  createVisibilityManager,
} from "@cplieger/sse";

const SKIP_REASON =
  "vibekit fixture not started: set SSE_FIXTURE to a vibekit binary built with -tags vibekit_test";
const FIXTURE = process.env["SSE_FIXTURE"];
if (FIXTURE === undefined || FIXTURE === "") {
  console.warn(`[vitest] ${SKIP_REASON}`);
}

/** Missed keepalives before a connected client reads gone (liveness.AliveWindow is two
 *  of them). The test derives the expected flip from the hello's keepalive_ms so no
 *  millisecond literal is duplicated here. */
const ABSENCE_BEATS = 2;

interface ProbeRow {
  tag: string;
  connected: number;
  lastAliveAt: string;
  gone: boolean;
}

interface Probe {
  presence: ProbeRow[];
  presence_alive: number;
  presence_expired: number;
}

function until(predicate: () => boolean, what: string, timeoutMs: number): Promise<void> {
  return new Promise((resolve, reject) => {
    const deadline = Date.now() + timeoutMs;
    const tick = (): void => {
      if (predicate()) {
        resolve();
        return;
      }
      if (Date.now() > deadline) {
        reject(new Error(`timed out waiting for ${what}`));
        return;
      }
      setTimeout(tick, 50);
    };
    tick();
  });
}

describe.skipIf(FIXTURE === undefined || FIXTURE === "")("the alive acknowledgement", () => {
  const base = (): string => inject("vibekitURL");
  const tag = `e2e_alive_${Math.random().toString(36).slice(2, 12)}`;
  let stream: Stream | null = null;
  const lifecycle: LifecycleEvent[] = [];
  /** The fixture flag: while false the alive POST is answered locally and never sent. */
  let acknowledging = true;
  let sentAcks = 0;

  const unlisten = (): (() => void) => () => undefined;
  const visibility = createVisibilityManager({ visible: () => true, listen: unlisten });
  const online = createOnlineManager({ online: () => true, listen: unlisten });

  const fixtureFetch: typeof fetch = (input, init) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    if (url.endsWith("/api/events/alive")) {
      if (!acknowledging) {
        return Promise.resolve(new Response(null, { status: 204 }));
      }
      sentAcks++;
    }
    return fetch(input, init);
  };

  async function probeRow(): Promise<{ row: ProbeRow | undefined; probe: Probe }> {
    const res = await fetch(`${base()}/api/test/sse`);
    const probe = (await res.json()) as Probe;
    return { row: probe.presence.find((r) => r.tag === tag), probe };
  }

  /** Re-reads the probe every half second until `done` holds, failing closed. */
  async function pollProbe(
    done: (r: { row: ProbeRow | undefined; probe: Probe }) => boolean,
    what: string,
    timeoutMs: number,
  ): Promise<{ row: ProbeRow | undefined; probe: Probe }> {
    const deadline = Date.now() + timeoutMs;
    for (;;) {
      const r = await probeRow();
      if (done(r)) {
        return r;
      }
      if (Date.now() > deadline) {
        throw new Error(`timed out waiting for ${what}: ${JSON.stringify(r.row)}`);
      }
      await new Promise((resolve) => setTimeout(resolve, 500));
    }
  }

  function hello(): Extract<LifecycleEvent, { kind: "hello" }> | undefined {
    return lifecycle.find((e) => e.kind === "hello");
  }

  /** The hello's keepalive_ms, held by the stream's open state; 0 before the hello. */
  function keepaliveMs(): number {
    const st = stream?.state();
    return st?.kind === "open" ? st.keepaliveMs : 0;
  }

  function acks(): Extract<LifecycleEvent, { kind: "alive_ack" }>[] {
    return lifecycle.filter((e) => e.kind === "alive_ack");
  }

  beforeEach(() => {
    lifecycle.length = 0;
    acknowledging = true;
    sentAcks = 0;
    stream = createStream({
      url: `${base()}/api/events`,
      headers: { "SSE-Client": tag },
      versions: createVersionMap(),
      visibility,
      online,
      fetch: fixtureFetch,
      alive: { url: `${base()}/api/events/alive` },
      onFrame() {
        /* the frames are not this file's subject */
      },
      onLifecycle(ev) {
        lifecycle.push(ev);
      },
      revalidate: () => Promise.resolve(),
    });
    stream.start();
  });

  afterEach(() => {
    stream?.stop();
    stream = null;
  });

  it("reads present while acknowledging, and gone at the alive window once the acknowledgements stop with the socket still connected", async () => {
    await until(() => hello() !== undefined, "the hello", 10_000);
    const beatMs = keepaliveMs();
    expect(beatMs).toBeGreaterThan(0);
    const windowMs = ABSENCE_BEATS * beatMs;

    // Present from the hello on: the connect seeds the acknowledgement.
    const first = await probeRow();
    expect(first.row).toMatchObject({ connected: 1, gone: false });
    const seededAt = Date.parse(first.row?.lastAliveAt ?? "");
    expect(Number.isNaN(seededAt)).toBe(false);

    // One acknowledgement per keepalive, answered 204, and the row's receipt moves.
    await until(() => acks().length >= 1, "the first alive acknowledgement", beatMs + 5_000);
    expect(acks()[0]).toMatchObject({ ok: true, status: 204 });
    expect(sentAcks).toBeGreaterThanOrEqual(1);
    const acked = await probeRow();
    expect(acked.row?.connected).toBe(1);
    expect(Date.parse(acked.row?.lastAliveAt ?? "")).toBeGreaterThan(seededAt);
    expect(acked.row?.gone).toBe(false);

    // The fixture flag: the page stops acknowledging while the socket keeps reading.
    const { probe: beforeStop } = await probeRow();
    acknowledging = false;
    const sentAtStop = sentAcks;
    const stoppedAt = Date.now();
    await until(
      () => acks().length >= 2 && sentAcks === sentAtStop,
      "a keepalive read after the stop, acknowledged locally and not sent",
      beatMs + 5_000,
    );

    const final = await pollProbe(
      (r) => r.row?.gone === true,
      "the row to read gone",
      windowMs + beatMs + 5_000,
    );
    const flippedAfterMs = Date.now() - stoppedAt;
    expect(final.row?.connected, "the socket is still counted when the row flips").toBe(1);
    expect(flippedAfterMs).toBeGreaterThanOrEqual(windowMs - beatMs);
    expect(flippedAfterMs).toBeLessThanOrEqual(windowMs + beatMs + 5_000);
    expect(final.probe.presence_expired).toBe(beforeStop.presence_expired + 1);
  }, 120_000);
});
