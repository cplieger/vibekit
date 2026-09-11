// The reasoning-effort slider: a caption naming the dimension and the live tier,
// over a bar the knob slides inside. The caption is why the knob carries no text,
// and a textless knob is why the bar can contain one (15-input.css sizes both).
// `model-switcher.ts` owns state and dispatch; `effortLabel` is imported so this
// caption and the model pill resolve a tier through the one function.
// NOT `input[type="range"]`: `text-field-floor-css.test.ts` asserts the served
// markup carries none, and 61-mcp-tools.css's coarse font-size floor would grow
// one. The pointer arithmetic assumes LTR; the app ships no RTL support.
import { el } from "@cplieger/reactive";
import { effortLabel } from "./effort.js";
import type { SessionEffortLevel } from "./types.js";

export interface EffortSliderHandle {
  /** The `.effort-row` element, for the card to append and remove. */
  readonly el: HTMLDivElement;
  /** Rebuild the ARIA range for a new tier vocabulary. */
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

  /** The live tier's word, a separate element from the static "Effort:" beside it
   *  so the writer replaces the value alone. Not an `aria-labelledby` target and not
   *  a live region: the knob's name has to stay stable, and `aria-valuetext` is
   *  where the value reaches assistive tech. */
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

  /** THE ONE WRITER of the position, both ARIA channels and the caption, so they
   *  cannot disagree. Position is a custom property, never a px write, so a card
   *  that grows after this row is appended needs no ResizeObserver.
   *
   *  `frac` may diverge from the index only while a finger is down: the knob paints
   *  where the pointer is while every announced channel names the nearest tier.
   *  Omitted elsewhere, so a settled position is a function of the index. */
  function apply(index: number, frac?: number): void {
    const n = levels.length;
    const i = n === 0 ? 0 : Math.min(n - 1, Math.max(0, index));
    const level = levels[i];
    const text = level === undefined ? "" : effortLabel(level);
    const snapped = n <= 1 ? 0 : i / (n - 1);
    // On the TRACK: the bar is the track's `::before`, which inherits from the track
    // and not from the knob below it, so this is the only element both readers see.
    track.style.setProperty("--effort-frac", String(frac ?? snapped));
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

  /** Where along its travel `clientX` puts the knob, 0..1, continuous. Both rects
   *  come from one call, so the arithmetic survives the card's enter scale, and the
   *  inset is read as `offsetLeft` rather than copied out of CSS. */
  function fracAt(clientX: number): number {
    const t = track.getBoundingClientRect();
    const k = knob.getBoundingClientRect();
    const pad = knob.offsetLeft;
    const travel = t.width - 2 * pad - k.width;
    if (travel <= 0) {
      return 0;
    }
    return Math.min(1, Math.max(0, (clientX - t.left - pad - k.width / 2) / travel));
  }

  function dragging(): boolean {
    return track.dataset["dragging"] !== undefined;
  }

  /** Paint where the finger is, and name the tier that is nearest. */
  function follow(clientX: number): void {
    const frac = fracAt(clientX);
    const n = levels.length;
    apply(n <= 1 ? 0 : Math.round(frac * (n - 1)), frac);
  }

  // One handler on the TRACK serves both gestures. No `preventDefault()`:
  // `touch-action`/`user-select` already stop what it would cancel, and Firefox
  // ties `:active` to the mousedown default, so cancelling kills the press rule.
  track.addEventListener("pointerdown", (e: PointerEvent) => {
    e.stopPropagation();
    track.setPointerCapture(e.pointerId);
    track.dataset["dragging"] = "";
    follow(e.clientX);
  });
  track.addEventListener("pointermove", (e: PointerEvent) => {
    if (dragging()) {
      follow(e.clientX);
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
    // Focus here and not on `pointerdown`: a tap on bare track has no focusable
    // ancestor, so mousedown's default clears to `<body>` and undoes an earlier
    // `focus()`. Only real input shows it — a synthetic pointerdown has no default.
    knob.focus();
    // The snap: `pick` re-applies with no `frac`, and `data-dragging` is already
    // gone, so the knob's transition animates it onto the tier.
    pick(shownIndex());
  });
  track.addEventListener("pointercancel", () => {
    delete track.dataset["dragging"];
    setActive(synced);
  });

  // The six keys are STOPPED: the card's `rovingFocus` reads no target, so an arrow
  // reaching it moves focus into the model list. Everything else propagates, because
  // Escape has to reach the popup's own document handler.
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
    // No per-tier element: the drag is continuous, so a mark per tier would draw a
    // grid the gesture does not follow. `data-tiers` is read by one rule.
  }

  function setActive(id: string): void {
    synced = id;
    const i = levels.findIndex((l) => l.id === id);
    apply(i < 0 ? 0 : i);
  }

  return { el: row, setLevels, setActive };
}
