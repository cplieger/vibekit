// ---------------------------------------------------------------------------
// Constant-latency reveal buffer for streamed text.
//
// Without it, render cadence IS network cadence. `markdown.ts` parses whatever
// accumulated in its 200ms window, so the transcript grows five times a second
// in whatever-size lumps KAS happened to send, and freezes in the gaps between
// bursts. The eye reads that freeze-then-surge cycle as "chunks".
//
// This sits between the growing text and the renderer as a jitter buffer, the
// shape an audio pipeline uses for the same problem: the reveal aims to trail
// the live edge by a fixed time lag, so
//
//     rate = backlog / LAG_SECS
//
// which self-regulates in every case.
//   - A steady model at R chars/sec settles a standing backlog of R·LAG and
//     reveals at exactly R: smooth, with a constant LAG_SECS delay.
//   - A burst raises the backlog, so the rate ramps up (slew-limited below) and
//     the excess drains exponentially with time constant LAG_SECS.
//   - A gap between bursts is absorbed by the standing backlog, so the reveal
//     keeps flowing for up to ~LAG_SECS before it can starve.
//
// Ported from KiroCrew's `useSmoothStream` (website/src/hooks/useSmoothStream.ts),
// constants included. What deliberately did NOT come across is its per-character
// opacity plugin: that exists because react-markdown re-parses the whole tail
// every frame and remounts spans over text already on screen, which a
// mount-triggered fade would re-fire at exactly the active edge. vibekit's
// parser appends and never re-parses, so that flash cannot happen here, and
// per-character spans would be permanent DOM for a defence against nothing. The
// `[data-vk-chunk-enter]` fade already in `13-messages.css` does the same job:
// under this buffer a "write" is one frame rather than one 200ms window.
//
// The buffer holds only WHERE the reveal is, never how the text is rendered, so
// it is DOM-free and its whole surface is chunks in and slices out.
// ---------------------------------------------------------------------------

/** Floor reveal speed (chars/sec) so text keeps flowing while the model idles
 *  or streams very slowly. */
const MIN_CPS = 50;

/** Target time lag (seconds) behind the live edge. This one constant is the
 *  whole smoothness/latency tradeoff: simultaneously the standing cushion that
 *  bridges inter-burst gaps, the drain time constant for a burst, and the
 *  perceived delay behind the raw stream. */
const LAG_SECS = 0.4;

/** Ceiling on the reveal rate (chars/sec), which is the smoothness guarantee.
 *  Bounds how much text can mount per frame (~10 chars at 60fps, roughly 1.7
 *  words), so a fat burst reads as a fast per-word cascade rather than a blur.
 *  It matters most at the START of a turn: the controller has no standing state
 *  yet, and a large first chunk (typical after a long tool phase) would
 *  otherwise ask for backlog/lag = thousands of chars per second. */
const MAX_CPS = 600;

/** Bounded-drain escape hatch. The rate ceiling is
 *  max(MAX_CPS, backlog/MAX_DRAIN_SECS), which makes a large backlog drain with
 *  a time constant of MAX_DRAIN_SECS rather than LAG_SECS, so the clearing time
 *  grows sub-linearly with the size of the dump. Measured from cold, against
 *  what the flat MAX_CPS cap alone would cost: 4 KB in 5.3s (6.7s), 20 KB in
 *  9.1s (33s), 60 KB in 11.7s (100s). An ordinary burst, up to
 *  MAX_CPS × MAX_DRAIN_SECS or about 1.5K characters, never engages it.
 *
 *  It is also what keeps the lag finite on a model faster than MAX_CPS: the
 *  backlog settles where backlog/MAX_DRAIN_SECS equals the model's own rate, so
 *  the trailing distance stops growing instead of running away. The cost of that
 *  is a lag of up to MAX_DRAIN_SECS on such a model rather than LAG_SECS. */
const MAX_DRAIN_SECS = 2.5;

/** Slew time constant (seconds) for the APPLIED rate. The desired rate steps
 *  discontinuously when a burst lands; low-pass filtering what is applied turns
 *  those steps into accelerations, so the reveal speeds up and coasts down
 *  instead of jerking. */
const RATE_SLEW_TAU = 0.15;

/** Smallest slice handed downstream, unless it is the last one. Every write is
 *  a permanent DOM node here (`smd-renderer`'s `add_text_dom` appends one text
 *  node, or one faded span while streaming), and at the MIN_CPS floor a
 *  per-frame write is ONE character, which would spend a node per character for
 *  a message nobody is reading quickly. Three characters at the floor is a write
 *  every ~60ms, still well above the 200ms this replaces. */
const MIN_EMIT_CHARS = 3;

/** Clamp on the measured frame interval (seconds). A backgrounded tab hands the
 *  first frame after refocus a gap of whole seconds, which would otherwise
 *  discharge the entire backlog in one write. */
const MAX_DT_SECS = 0.1;

/** Assumed interval for the first frame of a run, which has nothing to measure
 *  against. One 60fps frame. */
const FIRST_FRAME_SECS = 1 / 60;

/** A live reveal cursor over a growing string. */
export interface Reveal {
  /** Extend the target by `delta` — the live path's per-chunk feed. An empty
   *  delta is ignored. */
  append(delta: string): void;
  /** Move the target to the full text now available — the resync path for a
   *  caller holding the whole text rather than the growth. `full` must extend
   *  what was previously given; a string no longer than the current target is
   *  ignored, so a caller may re-publish freely. */
  setText(full: string): void;
  /** True when everything given has been handed to `onWrite`. */
  readonly idle: boolean;
  /** Write everything still held back, now, and stop the loop. `onIdle` runs
   *  once at the end, so a caller that finalizes there needs no second path. */
  finishNow(): void;
}

export interface RevealOptions {
  /** Receives each newly revealed slice, in order. Concatenating every slice
   *  reproduces the target exactly. */
  onWrite: (delta: string) => void;
  /** Runs each time the reveal catches up with the target. */
  onIdle?: () => void;
  /** Frame scheduler. The default pair is requestAnimationFrame; tests inject a
   *  clock they drive, because the controller's whole subject is time. */
  schedule?: (fn: (t: number) => void) => number;
  cancel?: (handle: number) => void;
}

/**
 * Build a reveal cursor. `initial` is treated as already on screen: the cursor
 * starts at its end and `onWrite` is never called for it. That is what makes a
 * mid-turn connect (which arrives holding the transcript so far) and a replay
 * paint in one pass, while only genuine growth is revealed at a regulated rate.
 */
export function createReveal(initial: string, opts: RevealOptions): Reveal {
  const schedule = opts.schedule ?? ((fn: (t: number) => void) => requestAnimationFrame(fn));
  const cancel =
    opts.cancel ??
    ((handle: number) => {
      cancelAnimationFrame(handle);
    });

  // The text past `initial`, kept as the chunks it arrived in, with a cursor
  // (part index + offset) marking the next unsent character. The chunks stay
  // separate rather than concatenated into one growing string so an append
  // costs its own chunk and a frame's emit walks only the characters it
  // reveals — per-frame cost stops scaling with the turn's total length.
  const parts: string[] = [];
  let part = 0; // parts[part] holds the next unsent character…
  let offset = 0; // …at this offset
  let known = initial.length; // characters known, revealed or not
  let sent = initial.length; // characters handed to onWrite
  let progress = initial.length; // float reveal cursor
  let rate = 0; // slew-limited applied rate, chars/sec
  let handle = 0; // 0 = no frame pending
  let last = 0; // timestamp of the previous frame, 0 = none

  /** Consume exactly `n` characters at the cursor. Callers keep
   *  `n <= known - sent`, so the cursor cannot run off the end. */
  const take = (n: number): string => {
    let out = "";
    let remaining = n;
    while (remaining > 0) {
      const p = parts[part];
      if (p === undefined) {
        break; // unreachable under the caller bound
      }
      const avail = p.length - offset;
      if (avail > remaining) {
        out += p.slice(offset, offset + remaining);
        offset += remaining;
        remaining = 0;
      } else {
        out += offset === 0 ? p : p.slice(offset);
        parts[part] = ""; // consumed — release the chunk, keep the slot
        part += 1;
        offset = 0;
        remaining -= avail;
      }
    }
    return out;
  };

  /** Hand over the whole characters revealed since the last write. */
  const emit = (): void => {
    const to = Math.min(known, Math.floor(progress));
    const pending = to - sent;
    if (pending <= 0) {
      return;
    }
    // Hold a sliver back rather than spending a node on it, unless this is the
    // tail: there is no later write to join it to.
    if (pending < MIN_EMIT_CHARS && to < known) {
      return;
    }
    const delta = take(pending);
    sent = to;
    opts.onWrite(delta);
  };

  const tick = (t: number): void => {
    handle = 0;
    const dt = last === 0 ? FIRST_FRAME_SECS : Math.min(MAX_DT_SECS, (t - last) / 1000);
    last = t;

    const backlog = known - progress;
    let desired = backlog > 0 ? Math.max(MIN_CPS, backlog / LAG_SECS) : 0;
    const ceiling = Math.max(MAX_CPS, backlog / MAX_DRAIN_SECS);
    if (desired > ceiling) {
      desired = ceiling;
    }
    rate += (1 - Math.exp(-dt / RATE_SLEW_TAU)) * (desired - rate);
    if (backlog > 0) {
      progress = Math.min(known, progress + rate * dt);
    }
    emit();

    // Keep running while anything is unwritten, which covers both a cursor
    // short of the end and a sliver held back below MIN_EMIT_CHARS.
    if (sent < known) {
      handle = schedule(tick);
      return;
    }
    last = 0;
    rate = 0;
    opts.onIdle?.();
  };

  const append = (delta: string): void => {
    if (delta === "") {
      return;
    }
    parts.push(delta);
    known += delta.length;
    if (handle === 0) {
      // Restarting from rest also restarts the slew from zero, which is the
      // gentle acceleration a burst after a pause should have.
      last = 0;
      handle = schedule(tick);
    }
  };

  return {
    append,
    setText(full: string): void {
      if (full.length <= known) {
        return;
      }
      // `full` extends what was already given (the caller's stream is
      // append-only), so the tail past `known` is the whole growth.
      append(full.slice(known));
    },
    get idle(): boolean {
      return sent >= known;
    },
    finishNow(): void {
      if (handle !== 0) {
        cancel(handle);
        handle = 0;
      }
      last = 0;
      rate = 0;
      progress = known;
      if (sent < known) {
        const delta = take(known - sent);
        sent = known;
        opts.onWrite(delta);
      }
      opts.onIdle?.();
    },
  };
}
