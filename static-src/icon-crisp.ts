/** Snaps icon boxes onto the pixel grid so a 1px stroke paints one whole pixel.
 *  Rule, measurement and the rejected alternatives: `vibekit-ui.md` "Icon crispness". */

const TIERS = ".ic-inline, .ic-ui, .ic-lg, .ic-hero";

/** `s` is the screen offset in force, `t` the local translate that produces it. The two
 *  differ whenever an ancestor rotates the icon, and the rect is measured in screen space. */
const applied = new WeakMap<SVGSVGElement, { sx: number; sy: number; tx: number; ty: number }>();

const frac = (v: number): number => ((v % 1) + 1) % 1;

/** Box phase that puts a multiple-of-3 coordinate on a half pixel. Scale-dependent:
 *  0.5 at `ic-ui`, 0 at `ic-inline`, where such a coordinate already sits on a half. */
function targetPhase(scale: number): number {
  return frac(0.5 - frac(3 * scale));
}

function toPhase(v: number, target: number): number {
  let delta = target - frac(v);
  if (delta > 0.5) {
    delta -= 1;
  }
  if (delta <= -0.5) {
    delta += 1;
  }
  return delta;
}

const q = (v: number): number => Math.round(v * 1000) / 1000;

/** The icon's own space is not the screen's: `.disclosure-chevron` rotates its child, and
 *  `translate` composes inside that rotation, so a screen-space correction written straight
 *  onto the box lands on the wrong axis. Maps a screen delta into the element's own space.
 *
 *  `getScreenCTM` carries the whole ancestor chain; dividing by the viewBox scale leaves the
 *  rotation alone, which is ORTHOGONAL for the axis-aligned cases — so the inverse is the
 *  transpose. Anything else answers null: a non-right-angle rotation cannot be crisp at all,
 *  and the box's screen AABB is not its box, so the phase read would be meaningless too. */
function toLocal(
  el: SVGSVGElement,
  scale: number,
  dx: number,
  dy: number,
): [number, number] | null {
  const m = el.getScreenCTM();
  if (m === null || scale === 0) {
    return null;
  }
  const a = m.a / scale;
  const b = m.b / scale;
  const c = m.c / scale;
  const d = m.d / scale;
  const unit = (v: number): boolean => Math.abs(Math.abs(v) - 1) < 0.01;
  const zero = (v: number): boolean => Math.abs(v) < 0.01;
  const aligned =
    (unit(a) && unit(d) && zero(b) && zero(c)) || (zero(a) && zero(d) && unit(b) && unit(c));
  if (!aligned) {
    return null;
  }
  return [q(a * dx + b * dy), q(c * dx + d * dy)];
}

/** Returns how many boxes moved. */
export function snapIcons(root: ParentNode = document): number {
  // Every rect read before any style write: interleaving forces a layout per icon.
  const work: { el: SVGSVGElement; sx: number; sy: number; tx: number; ty: number }[] = [];
  for (const el of root.querySelectorAll<SVGSVGElement>(TIERS)) {
    const rect = el.getBoundingClientRect();
    const side = el.viewBox.baseVal.width;
    if (!rect.width || !rect.height || !side) {
      continue;
    }
    const scale = rect.width / side;
    const target = targetPhase(scale);
    // `rect` includes the offset already applied, so measure the original box. The offset is
    // subtracted in SCREEN space, which is the space the rect is in.
    const prev = applied.get(el) ?? { sx: 0, sy: 0, tx: 0, ty: 0 };
    const sx = q(toPhase(rect.x - prev.sx, target));
    const sy = q(toPhase(rect.y - prev.sy, target));
    const local = toLocal(el, scale, sx, sy);
    if (local === null) {
      continue;
    }
    work.push({ el, sx, sy, tx: local[0], ty: local[1] });
  }

  let changed = 0;
  for (const { el, sx, sy, tx, ty } of work) {
    const prev = applied.get(el);
    if (prev?.tx === tx && prev.ty === ty) {
      continue;
    }
    applied.set(el, { sx, sy, tx, ty });
    // `translate`, not `transform`: a running animation's transform outranks inline.
    el.style.translate = tx === 0 && ty === 0 ? "" : `${String(tx)}px ${String(ty)}px`;
    changed++;
  }
  return changed;
}

let fullPass = false;
let resize: ResizeObserver | undefined;
let added: MutationObserver | undefined;
const pending = new Set<SVGSVGElement>();
let incremental = false;
let settleTimer: ReturnType<typeof setTimeout> | undefined;
const SETTLE_MS = 250;

function scheduleFull(): void {
  if (fullPass) {
    return;
  }
  fullPass = true;
  requestAnimationFrame(() => {
    fullPass = false;
    pending.clear();
    // A pass that MOVED something has itself changed the layout, so re-check once it settles.
    // Self-terminating: an icon already on target computes the same offset and reports no
    // change, so a converged document arms nothing.
    if (snapIcons() > 0) {
      scheduleSettle();
    }
  });
}

/** Re-measure once the DOM stops moving: a reflow after an icon was snapped leaves its
 *  offset stale, and the transcript reflows constantly while a turn streams. Debounced,
 *  so this is one pass per quiet period rather than one per frame. */
function scheduleSettle(): void {
  if (settleTimer !== undefined) {
    clearTimeout(settleTimer);
  }
  settleTimer = setTimeout(() => {
    settleTimer = undefined;
    scheduleFull();
  }, SETTLE_MS);
}

function scheduleAdded(nodes: SVGSVGElement[]): void {
  for (const n of nodes) {
    pending.add(n);
  }
  scheduleSettle();
  if (incremental || fullPass) {
    return;
  }
  incremental = true;
  requestAnimationFrame(() => {
    incremental = false;
    if (fullPass || pending.size === 0) {
      return;
    }
    const batch = [...pending];
    pending.clear();
    // Per arrival, so the cost tracks arrivals rather than the whole document.
    for (const el of batch) {
      if (el.isConnected) {
        snapIcons(scopeOf(el));
      }
    }
  });
}

/** A `ParentNode` yielding exactly `el`, so `snapIcons` serves one element too. */
function scopeOf(el: SVGSVGElement): ParentNode {
  return {
    querySelectorAll: () => [el] as unknown as NodeListOf<Element>,
  } as unknown as ParentNode;
}

function iconsIn(node: Node): SVGSVGElement[] {
  if (!(node instanceof Element)) {
    return [];
  }
  const out = [...node.querySelectorAll<SVGSVGElement>(TIERS)];
  if (node.matches(TIERS)) {
    out.push(node as SVGSVGElement);
  }
  return out;
}

/** Idempotent. */
export function initIconCrisp(): void {
  scheduleFull();
  if (resize !== undefined) {
    return;
  }
  // `document.body`, not `#app`: modals and the login form are body-level siblings.
  const target = document.body;
  // A resize moves existing icons; an addition only needs the new nodes measured, and
  // a ResizeObserver does not fire for content added inside its target.
  resize = new ResizeObserver(scheduleFull);
  resize.observe(target);
  added = new MutationObserver((records) => {
    const fresh: SVGSVGElement[] = [];
    let rotated = false;
    for (const rec of records) {
      if (rec.type === "attributes") {
        rotated = true;
        continue;
      }
      for (const node of rec.addedNodes) {
        fresh.push(...iconsIn(node));
      }
    }
    if (fresh.length) {
      scheduleAdded(fresh);
    }
    // A disclosure toggle re-rotates its chevron, which changes the map a screen correction
    // has to be written through, and it adds no node for the branch above to see. The settle
    // delay outlasts the rotation's own transition; a pass taken mid-rotation reads a map
    // that is not axis-aligned and `toLocal` declines it, so the stale offset simply holds.
    if (rotated) {
      scheduleSettle();
    }
  });
  added.observe(target, {
    childList: true,
    subtree: true,
    attributes: true,
    attributeFilter: ["aria-expanded"],
  });
  // A web font landing changes control heights, moving every icon inside them.
  void document.fonts.ready.then(scheduleFull);
}

export function stopIconCrisp(): void {
  resize?.disconnect();
  resize = undefined;
  added?.disconnect();
  added = undefined;
  if (settleTimer !== undefined) {
    clearTimeout(settleTimer);
    settleTimer = undefined;
  }
  pending.clear();
  for (const el of document.querySelectorAll<SVGSVGElement>(TIERS)) {
    if (applied.has(el)) {
      el.style.translate = "";
      applied.delete(el);
    }
  }
}
