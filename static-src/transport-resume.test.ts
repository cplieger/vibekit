// The resume kick, and the readyState it has to read to fire.
//
// A tab coming back to the foreground kicks an immediate reconnect when the
// stream is not alive, to pre-empt the browser's own retry timer (3 s in
// Chromium, and the server emits no `retry:` field to shorten it). The check
// read the CONTROLLER's phase, and `onerror` only demotes that phase when
// `readyState` is CLOSED — so the case the kick exists for, a retryable drop the
// browser is handling internally, left the phase at `connected` and the kick
// never fired. Measured downstream: 32 of 40 `SSE connected` lines carried
// `last_event_id=""`, i.e. recovery was a cold stream rather than a resume.
//
// The second half is the trap in fixing it. `pageshow` fires on every cold load,
// AFTER `init` has opened the source, so a bare `readyState !== OPEN` test reports
// a mid-handshake stream as dead and tears down the connection the boot just
// opened, on every single load.

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import type * as TransportModule from "./transport.js";

/** An EventSource whose readyState the test drives. */
class FakeEventSource {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 2;
  /** Every instance ever constructed, so a reconnect is countable. */
  static opened: FakeEventSource[] = [];
  onopen: (() => void) | null = null;
  onmessage: ((e: MessageEvent) => void) | null = null;
  onerror: (() => void) | null = null;
  readyState = FakeEventSource.CONNECTING;
  constructor(readonly url: string) {
    FakeEventSource.opened.push(this);
  }
  close(): void {
    this.readyState = FakeEventSource.CLOSED;
  }
  /** The handshake completing. */
  open(): void {
    this.readyState = FakeEventSource.OPEN;
    this.onopen?.();
  }
  /** A drop the BROWSER will retry on its own: `readyState` goes back to
   *  CONNECTING and `onerror` fires without ever reaching CLOSED, which is why the
   *  controller's phase stays `connected`. */
  retryableDrop(): void {
    this.readyState = FakeEventSource.CONNECTING;
    this.onerror?.();
  }
}

const OriginalES = globalThis.EventSource;

describe("the resume kick", () => {
  let transport: typeof TransportModule;

  beforeEach(async () => {
    vi.resetModules();
    vi.useFakeTimers();
    FakeEventSource.opened = [];
    (globalThis as { EventSource: unknown }).EventSource = FakeEventSource;
    transport = await import("./transport.js");
  });

  afterEach(() => {
    (globalThis as { EventSource: unknown }).EventSource = OriginalES;
    vi.useRealTimers();
  });

  function boot(): FakeEventSource {
    transport.init(
      () => {
        /* frames unobserved */
      },
      () => {
        /* status unobserved */
      },
    );
    const source = FakeEventSource.opened.at(-1);
    if (source === undefined) {
      throw new Error("init must open a stream");
    }
    return source;
  }

  it("reconnects when the browser is retrying under a still-connected phase", () => {
    const source = boot();
    source.open();
    expect(FakeEventSource.opened).toHaveLength(1);

    source.retryableDrop();
    window.dispatchEvent(new PageTransitionEvent("pageshow", { persisted: false }));
    vi.advanceTimersByTime(0);

    expect(FakeEventSource.opened).toHaveLength(2);
    expect(source.readyState).toBe(FakeEventSource.CLOSED);
  });

  it("leaves a stream that is still open alone", () => {
    const source = boot();
    source.open();

    window.dispatchEvent(new PageTransitionEvent("pageshow", { persisted: false }));
    vi.advanceTimersByTime(0);

    expect(FakeEventSource.opened).toHaveLength(1);
  });

  it("leaves the first handshake alone, because pageshow races it on every load", () => {
    // Not opened yet: readyState is CONNECTING and nothing has gone wrong. A bare
    // `!== OPEN` test would tear this down and reopen it on every cold load.
    boot();

    window.dispatchEvent(new PageTransitionEvent("pageshow", { persisted: false }));
    vi.advanceTimersByTime(0);

    expect(FakeEventSource.opened).toHaveLength(1);
  });

  it("reconnects when the tab returns to a stream the browser gave up on", () => {
    const source = boot();
    source.open();
    source.readyState = FakeEventSource.CLOSED;

    document.dispatchEvent(new Event("visibilitychange"));
    vi.advanceTimersByTime(0);

    expect(FakeEventSource.opened).toHaveLength(2);
  });
});
