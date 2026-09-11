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

/** One entry per element that has been stamped, so re-stamping cannot loop: writing
 *  the delay shifts a running animation's timeline, which can fire `animationstart`
 *  again. Weak, so a dot leaving the DOM takes its entry with it. */
const stamped = new WeakSet<Element>();

function stamp(el: Element): void {
  if (stamped.has(el) || !(el instanceof HTMLElement)) {
    return;
  }
  const period = periodMs();
  if (period === 0) {
    return;
  }
  stamped.add(el);
  // Negative, so an animation created now behaves as though it began at the last
  // boundary of a grid anchored at the performance origin — the same grid for every
  // dot, whenever it starts.
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
 *  element is the shape this module's WeakSet exists to make harmless — a seam
 *  should not depend on that guard to stay correct. */
export function resetBeatPhaseForTest(): void {
  attached?.abort();
  attached = undefined;
}
