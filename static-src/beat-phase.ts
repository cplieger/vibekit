/** Aligns every dot beat to one origin, so N dots that start at N different moments
 *  still breathe together. Mechanism and the measurements: `03-base.css` "THE DOT
 *  BEAT" and `vibekit-ui.md` "Entry motion is a GPU budget". */

const NAME = "vk-dot-beat";

/** Must equal `--dot-beat-dur` (01-tokens.css). Read from the document rather than
 *  restated, so a retuned token cannot leave the phase grid on the old period. */
function periodMs(): number {
  const raw = getComputedStyle(document.documentElement).getPropertyValue("--dot-beat-dur");
  const s = raw.trim();
  const n = Number.parseFloat(s);
  if (!Number.isFinite(n) || n <= 0) {
    return 0;
  }
  return s.endsWith("ms") ? n : n * 1000;
}

/** The beat `startTime` this element was last stamped FOR, keyed weakly so a dot
 *  leaving the DOM takes its entry with it.
 *
 *  NOT a has-been-stamped flag, and that distinction is the whole of it: re-inserting
 *  an attached node destroys and recreates its animation, so a dot stamped once can
 *  need stamping again, and a permanent flag left it off the shared grid forever with
 *  nothing able to put it back. `tabs-drag.ts` reaches that on every completed drag.
 *
 *  `startTime` is what tells a real restart from our own write echoing back. Measured
 *  in Chromium 151: writing the delay on a running beat fires NO `animationstart` and
 *  leaves `startTime` untouched (101 on both sides) while `currentTime` advances, and a
 *  re-seat fires exactly one event and mints a new `startTime` (101 to 851) with
 *  `currentTime` back at 100. So the flag this replaced was guarding an event that does
 *  not arrive. It is keyed on the animation instance rather than on that measurement
 *  because being wrong the other way is a permanent visual loop: an engine that DID
 *  re-fire on the write still cannot make this re-stamp, because the echo carries the
 *  `startTime` already recorded. */
const stampedFor = new WeakMap<Element, number>();

/** This element's beat, BY NAME: `getAnimations({subtree:true})` also returns the
 *  pseudo-element's animation, which is where the beat lives, and any other animation
 *  the element happens to carry. `undefined` while the animation is PENDING, which only
 *  a new one can be, so that case stamps. */
function beatStartTime(el: Element): number | undefined {
  for (const a of el.getAnimations({ subtree: true })) {
    if ((a as CSSAnimation).animationName !== NAME) {
      continue;
    }
    return a.startTime === null ? undefined : Number(a.startTime);
  }
  return undefined;
}

function stamp(el: Element): void {
  if (!(el instanceof HTMLElement)) {
    return;
  }
  const period = periodMs();
  if (period === 0) {
    return;
  }
  const start = beatStartTime(el);
  if (start !== undefined) {
    if (stampedFor.get(el) === start) {
      return;
    }
    stampedFor.set(el, start);
  }
  // Negative, so an animation created now behaves as though it began at the last
  // boundary of a grid anchored at the performance origin — the same grid for every
  // dot, whenever it starts, and the same grid again after a restart.
  el.style.setProperty("--beat-phase", `${String(-(performance.now() % period))}ms`);
}

let attached: AbortController | undefined;

/** Idempotent. */
export function initBeatPhase(): void {
  if (attached !== undefined) {
    return;
  }
  attached = new AbortController();
  // Delegated, in the capture phase, so ONE listener serves every dot and the eight
  // places that write dot state need to know nothing about phase. The event fires for
  // a pseudo-element's animation too, targeting the originating element, which is
  // where the delay has to be set — a pseudo cannot be styled from script.
  document.addEventListener(
    "animationstart",
    (e: AnimationEvent) => {
      if (e.animationName !== NAME || e.target === null) {
        return;
      }
      stamp(e.target as Element);
    },
    { capture: true, passive: true, signal: attached.signal },
  );
}

/** Test seam. Detaches rather than only clearing the flag: a reset that left the
 *  listener up would add a second one on the next init, and two stamps racing one
 *  element is the shape this module's per-instance record exists to make harmless — a
 *  seam should not depend on that guard to stay correct. */
export function resetBeatPhaseForTest(): void {
  attached?.abort();
  attached = undefined;
}
