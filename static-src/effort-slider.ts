// ---------------------------------------------------------------------------
// The reasoning-effort slider: the model card's bottom section, a CAPTION naming
// the dimension and the live tier over a stepped rail whose knob carries no text.
//
// The caption is what makes the knob textless, and the knob being textless is
// what makes the rail thin. A knob sized to its widest label measured 61px on the
// fine tier and 65px on the coarse one against a 26.53px tick spacing, so it
// covered the ticks either side of it and a tap on bare track was the only way to
// reach them; at `--hit-floor` it is 24px and 44px against ~45.5px and ~40.5px.
//
// A pure view with an imperative handle — `model-switcher.ts` stays the owner of
// state and dispatch, nothing here reads the store, and `effortLabel` is imported
// rather than reimplemented so the caption and the model pill name a tier the
// same way (effort.ts is the one resolution).
//
// NOT `input[type="range"]`. Two source facts record the absence of one as a
// measured premise: `text-field-floor-css.test.ts` asserts the served markup
// carries no `type="range"`, and 61-mcp-tools.css's coarse-tier font-size floor is
// written as a plain `:is(input, textarea, select)` on that premise, so a range
// input would start growing under it. `web.md` also records that a UA shadow
// pseudo-element's paint is invisible to every JS animation API, so a native
// thumb's position could not be verified from a test at all.
//
// The pointer arithmetic assumes LTR, on the measured premise that
// `static/index.html` carries `<html lang="en">` with no `dir` attribute and the
// app ships no RTL support.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { effortLabel } from "./effort.js";
import type { SessionEffortLevel } from "./types.js";

export interface EffortSliderHandle {
  /** The `.effort-row` element, for the card to append and remove. */
  readonly el: HTMLDivElement;
  /** Rebuild the ticks and the ARIA range for a new tier vocabulary. */
  readonly setLevels: (levels: readonly SessionEffortLevel[]) => void;
  /** Put the knob on `id`. A tier this vocabulary does not offer resolves to the
   *  lowest one: a slider is always somewhere, and this control has no state for
   *  "unchosen" (effort.ts is where that answer is still pinned). */
  readonly setActive: (id: string) => void;
}

/** Build the slider. `onPick` fires on every completed gesture — a released tap
 *  or drag, and each keyboard step — including one that lands where the knob
 *  already sits: `model-switcher.setEffort`'s own guard is what drops a repeat of
 *  the CHAT'S choice, and a chat marked at the model default has chosen nothing. */
export function buildEffortSlider(opts: { onPick: (level: string) => void }): EffortSliderHandle {
  let levels: readonly SessionEffortLevel[] = [];
  /** The tier last synced from the store, for a cancelled drag to fall back to. */
  let synced = "";

  /** The live tier's own word, inside the caption. A SEPARATE element from the
   *  static "Effort:" beside it, so the writer replaces the value alone and the
   *  dimension's name is never re-authored per step.
   *
   *  The knob keeps its STABLE `aria-label` and this is not an `aria-labelledby`
   *  target and not a live region: a name that changed on every step is the
   *  anti-pattern this repo already records for its `aria-pressed` toggles, and
   *  `aria-valuetext` is where the value reaches assistive tech. */
  const value = el("span", { className: "effort-value" });
  const knob = el("div", {
    className: "effort-knob",
    role: "slider",
    tabindex: "0",
    "aria-label": "Reasoning effort",
    "aria-valuemin": "0",
    "aria-valuemax": "0",
    "aria-valuenow": "0",
    "aria-valuetext": "",
  }) as HTMLDivElement;
  const track = el("div", { className: "effort-track" }, knob) as HTMLDivElement;
  const row = el(
    "div",
    { className: "effort-row" },
    el("span", { className: "effort-label" }, "Effort: ", value),
    track,
  ) as HTMLDivElement;

  /** THE ONE WRITER of the knob's position, both ARIA channels and the caption's
   *  value, so the tier it paints, the tier it names, the tier it announces and
   *  the index it reports cannot disagree. The caption joins this function rather
   *  than gaining a writer of its own.
   *  Position is CSS arithmetic over `--effort-frac`, never a px write: `web.md`
   *  bans animating a layout property to move something, and a px offset would
   *  need a ResizeObserver to follow a card whose width grows after this row is
   *  appended (the model list reconciles later in `renderCondensedList`). */
  function apply(index: number): void {
    const n = levels.length;
    const i = n === 0 ? 0 : Math.min(n - 1, Math.max(0, index));
    const level = levels[i];
    const text = level === undefined ? "" : effortLabel(level);
    knob.style.setProperty("--effort-frac", String(n <= 1 ? 0 : i / (n - 1)));
    knob.setAttribute("aria-valuenow", String(i));
    knob.setAttribute("aria-valuetext", text);
    knob.dataset["level"] = level?.id ?? "";
    value.textContent = text;
  }

  /** The index the knob is showing. */
  function shownIndex(): number {
    return Number(knob.getAttribute("aria-valuenow") ?? "0");
  }

  /** Move the knob and report the tier it landed on. */
  function pick(index: number): void {
    apply(index);
    const id = knob.dataset["level"] ?? "";
    if (id !== "") {
      opts.onPick(id);
    }
  }

  /** The tier nearest `clientX`.
   *
   *  BOTH rects are read in the same call so the arithmetic is scale-invariant
   *  while the card's enter transition (`scale(0.4)` to `scale(1)`) is mid-flight.
   *  `t.width` is a border box and `100cqi` is a content box, so the two travels
   *  agree exactly only while the track declares no border and no padding — which
   *  15-input.css states at the rule, the rail being a `::before` inside it. */
  function indexAt(clientX: number): number {
    const n = levels.length;
    if (n <= 1) {
      return 0;
    }
    const t = track.getBoundingClientRect();
    const k = knob.getBoundingClientRect();
    const travel = t.width - k.width;
    if (travel <= 0) {
      return 0;
    }
    const frac = Math.min(1, Math.max(0, (clientX - t.left - k.width / 2) / travel));
    return Math.round(frac * (n - 1));
  }

  function dragging(): boolean {
    return track.dataset["dragging"] !== undefined;
  }

  // One handler on the TRACK serves both gestures, because the knob is inside it.
  // No `preventDefault()`: `user-select`/`touch-action` in 15-input.css already
  // stop the selection and the scroll it would be cancelling, and Firefox ties
  // `:active` to the mousedown default action — so cancelling it would leave the
  // knob's press rule (70-selection.css) dead there, invisibly, since the Chromium
  // sidecar applies `:active` regardless (web.md).
  track.addEventListener("pointerdown", (e: PointerEvent) => {
    e.stopPropagation();
    track.setPointerCapture(e.pointerId);
    track.dataset["dragging"] = "";
    apply(indexAt(e.clientX));
  });
  track.addEventListener("pointermove", (e: PointerEvent) => {
    if (dragging()) {
      apply(indexAt(e.clientX));
    }
  });
  track.addEventListener("pointerup", (e: PointerEvent) => {
    if (!dragging()) {
      return;
    }
    delete track.dataset["dragging"];
    if (track.hasPointerCapture(e.pointerId)) {
      track.releasePointerCapture(e.pointerId);
    }
    // Focus lands here rather than on `pointerdown`, because a tick is not
    // focusable and the knob is its SIBLING: `mousedown`'s default focus action
    // resolves to no focusable ancestor and clears to `<body>`, undoing a
    // `focus()` the pointerdown handler already made. Measured with real input in
    // the sidecar — a tap on bare track left `activeElement` off the knob and the
    // next arrow key dead, while a tap that happened to land on the knob itself
    // stuck, because there the knob IS the mousedown target. By pointerup the
    // default has run, so this is not undone. `preventDefault()` on pointerdown
    // would also fix it and costs Firefox's `:active` (above), which the knob's
    // press rule reads. A synthetic pointerdown triggers no default focus action,
    // so no unit test can separate the two orders; real input is the instrument.
    knob.focus();
    pick(shownIndex());
  });
  track.addEventListener("pointercancel", () => {
    delete track.dataset["dragging"];
    setActive(synced);
  });

  // The six keys are stopped rather than merely defaulted: `model-switcher.ts`
  // wires `rovingFocus` over the whole card and its handler reads no target, so
  // ArrowUp/ArrowDown/Home/End reaching the card would move focus into the model
  // list. Everything else propagates — Escape has to keep reaching the popup's
  // document handler (`pill-expand.ts` sets `isolateEscape: false`).
  knob.addEventListener("keydown", (e: KeyboardEvent) => {
    const n = levels.length;
    if (n === 0) {
      return;
    }
    const cur = shownIndex();
    let next: number;
    switch (e.key) {
      case "ArrowLeft":
      case "ArrowDown":
        next = cur - 1;
        break;
      case "ArrowRight":
      case "ArrowUp":
        next = cur + 1;
        break;
      case "Home":
        next = 0;
        break;
      case "End":
        next = n - 1;
        break;
      default:
        return;
    }
    e.preventDefault();
    e.stopPropagation();
    pick(next);
  });

  function setLevels(next: readonly SessionEffortLevel[]): void {
    levels = [...next];
    track.dataset["tiers"] = String(levels.length);
    knob.setAttribute("aria-valuemax", String(Math.max(0, levels.length - 1)));
    const ticks = levels.map((level, i) => {
      const tick = el("span", { className: "effort-tick", "data-level": level.id });
      tick.style.setProperty(
        "--tick-frac",
        String(levels.length <= 1 ? 0 : i / (levels.length - 1)),
      );
      return tick;
    });
    track.replaceChildren(...ticks, knob);
  }

  function setActive(id: string): void {
    synced = id;
    const i = levels.findIndex((l) => l.id === id);
    apply(i < 0 ? 0 : i);
  }

  // NO measure step, and its absence is the point rather than an omission: the
  // knob held foreign text, so its width had to be re-derived from the widest
  // label on every card open (a remove-read-write that also had to force
  // `inline-size: max-content`, because with one tier the fill rule made a read
  // measure the track instead). With no text the size is `var(--hit-floor)`,
  // declared in CSS, so there is nothing to measure and nothing to publish.

  return { el: row, setLevels, setActive };
}
