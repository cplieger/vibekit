// ---------------------------------------------------------------------------
// The search box every surface shares.
//
// Six surfaces ask a reader to type a query: the transcript's Ctrl-F, the file
// browser's recursive grep, the editor's in-buffer find, the History page's
// cross-chat search, the configuration browser's metadata filter, and the git
// view's two panel filters. They had four hand-authored copies of the same box
// between them — the input's attribute set alone was duplicated in three places
// — and the copies had already drifted: two spellings of the match-case toggle's
// size, two debounce constants with the same value, and one box with
// `role="search"` and one without.
//
// WHAT IS SHARED IS MECHANICAL, NOT VISUAL. This module owns the box shell, the
// input's attributes, the debounce, the supersession guard, the `Aa` latched
// toggle and its `?case=1` convention, the status note, and the Escape/Enter key
// contract. Each consumer supplies a placeholder, a query function and a
// renderer.
//
// PLACEMENT AND REVEAL ARE STILL NOT HERE, and that is unchanged — but the
// population underneath it has changed. Two groups own it now instead of six
// one-offs:
//
//   - search-popup.ts is the FLOATING form: the four page boxes (History, the
//     configuration browser, the git view's two panels), which share one popup
//     lifecycle, one position and one clear-on-close rule. It sits ON TOP of this
//     module rather than inside it.
//   - The transcript's box is its own popup call (find-in-chat.ts), because it
//     has a cursor and a teardown that unwraps DOM it wrote into the page; the
//     file browser's and the editor's stay IN-FLOW, because each changes the
//     layout of the thing it searches — the browser's results REPLACE the
//     listing, and a docked editor bar shrinks the scroller instead of covering
//     the first lines of the file. 19-files.css and 20-editor.css record that.
//
// THE COUNTER VERSUS THE NOTE stays a real difference: a cursor reports "3 of
// 17"; a ranked list reports how much it read. Those answer different questions.
// And THE CURSOR HALF — marks, prev/next, scroll-into-view — belongs to the two
// surfaces that have a position in a document, arriving through `compose` as
// ordinary controls.
//
// There is NO mode flag, and that is a decision this codebase has already taken
// once: settings-highlight.ts refused a registry for the same reason. A flag
// would put the surfaces' differences inside one function's branches, where the
// next surface adds another value and every existing branch has to be re-read to
// know whether it applies.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { iconEl } from "./icon-el.js";

/** The default typing debounce. Small enough to feel instant, large enough to
 *  coalesce a burst of keystrokes; it was authored twice with this value, in two
 *  files, each with a comment saying it matched the other.
 *
 *  Overridable per box, because one surface has a real reason to differ: the
 *  cross-chat search reads up to 500 files per query, so its pause is longer.
 *  What is shared is the MECHANISM — one timer, one abort, one supersession
 *  guard — not the number. */
export const SEARCH_DEBOUNCE_MS = 90;

/* THERE IS NO IN-FIELD MAGNIFIER HERE ANY MORE, and its removal retired a
   question rather than answering it. This module used to export one, with a rule
   attached: a magnifier on a box that reaches past what is on screen, nothing on
   a box that only narrows it. Two consumers disagreed under that rule — History
   carried the glyph, the docs filter carried none — and the git panels carried a
   third spelling of their own. Every page box is opened BY a magnifier now (the
   toolbar's, through find-dispatch), so a second one inside the field is the same
   glyph twice; what a box reaches is stated in its placeholder instead. */

/** The close ×, local for the same reason the magnifier is: it is this
 *  component's own furniture, and importing it from icons.ts would make every
 *  consumer's test extend an icons mock to build a search box. */
const CLOSE_GLYPH =
  '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" ' +
  'stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' +
  '<line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>';

/** The `?case=1` convention, in one place.
 *
 *  Every server that takes it reads an ABSENT parameter as insensitive, so the
 *  flag is only ever sent when asked. Two boxes spelled this inline and a third
 *  endpoint does not accept it at all; keeping the spelling here is what stops
 *  the two that do from disagreeing. */
export function caseParam(caseSensitive: boolean): string {
  return caseSensitive ? "1" : "";
}

/**
 * An icon button for a search bar: an SVG glyph, vertically centred by
 * construction.
 *
 * THE CENTRING IS THE POINT, and it is why this factory exists rather than each
 * bar spelling its own. A TEXT glyph becomes an anonymous flex item whose LINE
 * BOX `align-items: center` centres — not its ink — and the two are only the
 * same when the ink happens to fill the line box symmetrically. `×` is a math
 * operator drawn about the math axis, an arrow follows neither the cap nor the
 * x-height band, and each font answers differently, so the offset is
 * platform-dependent by construction and no authored value can correct it.
 *
 * An SVG is a REPLACED element: its box IS its ink box, so centring the box
 * centres the glyph in every font. `line-height: 0` (on the button, in CSS)
 * collapses the strut that would otherwise oversize the line box around it.
 * That pairing is this app's convention for every correctly-centred icon button
 * — `.tab-close` and `.shell-header-btn` both carry it — and label-centring.test.ts
 * already records why the strut, not a line-height value, is the lever for a
 * replaced element.
 */
export function searchIconButton(
  className: string,
  label: string,
  hint: string,
  icon: string,
  onClick: () => void,
): HTMLButtonElement {
  const btn = el("button", {
    type: "button",
    className,
    "aria-label": label,
    title: hint,
  }) as HTMLButtonElement;
  btn.appendChild(iconEl(icon));
  btn.addEventListener("click", onClick);
  return btn;
}

/**
 * The `Aa` match-case toggle.
 *
 * The ONE button in a search bar that keeps its text, because the letters ARE
 * the affordance: an icon for "match case" would have to be learned, while `Aa`
 * shows a capital and a lowercase beside each other. So it does not take the SVG
 * answer above, and it needs the other one — `text-box: trim-both cap alphabetic`
 * in CSS, which makes the box edges the cap band so centring the box centres the
 * letterforms. That works here and would NOT work for `×` or an arrow: the trim
 * addresses the cap-to-baseline band, and only a letterform fills it.
 *
 * Latched, so it carries `aria-pressed` rather than relying on a tint —
 * 70-selection.css owns the fill for both bars' toggles and there is no local
 * selected state.
 */
export function matchCaseButton(
  className: string,
  initial: boolean,
  onToggle: (on: boolean) => void,
): HTMLButtonElement {
  const btn = el(
    "button",
    {
      type: "button",
      className,
      "aria-label": "Match case",
      title: "Match case",
    },
    "Aa",
  ) as HTMLButtonElement;
  btn.setAttribute("aria-pressed", initial ? "true" : "false");
  btn.addEventListener("click", () => {
    const on = btn.getAttribute("aria-pressed") !== "true";
    btn.setAttribute("aria-pressed", on ? "true" : "false");
    onToggle(on);
  });
  return btn;
}

/** The query field's attribute set, which was duplicated in three places.
 *
 *  `autocapitalize` and `spellcheck` off because a query is not prose, and
 *  `enterkeyhint="search"` so a phone's return key says what it does. `type`
 *  is the caller's: a `search` input draws the platform's clear affordance,
 *  which belongs on a permanent box and not on one that has its own × . */
export function searchField(opts: {
  id: string;
  className: string;
  label: string;
  placeholder: string;
  title?: string;
  type?: "text" | "search";
}): HTMLInputElement {
  const input = el("input", {
    id: opts.id,
    type: opts.type ?? "text",
    className: opts.className,
    placeholder: opts.placeholder,
    "aria-label": opts.label,
    autocomplete: "off",
    autocapitalize: "off",
    spellcheck: "false",
    enterkeyhint: "search",
  }) as HTMLInputElement;
  if (opts.title !== undefined) {
    input.title = opts.title;
  }
  return input;
}

/** The "what wasn't read" line. A polite live region, because it lands after the
 *  results and a reader who cannot see them needs to hear that the scan stopped
 *  — otherwise an empty answer claims the text is nowhere. */
function statusNote(id: string, className: string): HTMLElement {
  return el("div", {
    id,
    className,
    role: "status",
    "aria-live": "polite",
    "aria-atomic": "true",
  });
}

/** The `role="search"` region. Named so every box is one landmark of the same
 *  kind; the History box was a bare `<div>` and so was not reachable by landmark
 *  navigation at all. */
function searchRegion(opts: { id: string; className: string; label: string }): HTMLElement {
  return el("div", {
    id: opts.id,
    className: opts.className,
    role: "search",
    "aria-label": opts.label,
  });
}

/** Escape and Enter on a search field.
 *
 *  Both actions are the caller's: Escape closes a revealable box, and Enter means
 *  step to the next match on a cursor and re-run on a list. What is shared is that Escape is CONSUMED here
 *  (`stopPropagation`) so it does not also reach a modal or a global handler
 *  behind the box. */
export function wireSearchKeys(
  field: HTMLElement,
  opts: { onDismiss: () => void; onSubmit?: (shift: boolean) => void },
): void {
  field.addEventListener("keydown", (e: KeyboardEvent) => {
    if (e.key === "Escape") {
      e.preventDefault();
      e.stopPropagation();
      opts.onDismiss();
      return;
    }
    if (e.key === "Enter" && opts.onSubmit !== undefined) {
      e.preventDefault();
      opts.onSubmit(e.shiftKey);
    }
  });
}

/** What a consumer's `query` receives. */
interface SearchQueryContext {
  caseSensitive: boolean;
  signal: AbortSignal;
}

/** The parts the shell built, handed to `compose` for arrangement. */
interface SearchShellParts {
  input: HTMLInputElement;
  /** Present only when `matchCase` was asked for. */
  caseButton: HTMLButtonElement | null;
  /** Present only when `note` was asked for. */
  note: HTMLElement | null;
  /** Present only when `closeButton` was asked for. */
  closeButton: HTMLButtonElement | null;
}

export interface SearchShellSpec<R> {
  /** Element id prefix. The input becomes `<id>-input`, the note `<id>-note`. */
  id: string;
  /** The region's class, and the base for the field/button/note classes. */
  regionClass: string;
  inputClass: string;
  buttonClass: string;
  caseClass?: string;
  noteClass?: string;
  label: string;
  placeholder: string;
  /** Tooltip on the field. Every box reached by Ctrl-F states the second-press
   *  escape hatch here. */
  inputTitle?: string;
  inputType?: "text" | "search";
  /** Offer the `Aa` toggle. FALSE is a real answer, not a default: the
   *  cross-chat endpoint is case-insensitive by decision, and a toggle wired to
   *  a parameter the server does not read would silently do nothing. */
  matchCase?: boolean;
  /** Offer the status note. */
  note?: boolean;
  /** Offer a × that calls `onDismiss`. */
  closeButton?: boolean;
  /** What the × closes, for its accessible name: "Close find" by default, so a
   *  box that is a FILTER can say so instead. The word a reader hears has to
   *  match the glyph they see. */
  closeNoun?: string;
  /** Typing pause before a run. Defaults to SEARCH_DEBOUNCE_MS. */
  debounceMs?: number;
  /** Arrange the built parts into the region. Extra controls (a glob row, a
   *  match counter, prev/next) go in here. */
  compose: (parts: SearchShellParts) => (Node | null)[];
  /** Run one query. Returning null means "nothing to render" (a failed fetch is
   *  already logged centrally by the api client).
   *
   *  MAY BE SYNCHRONOUS, and four of the six boxes are: the editor's find reads a
   *  string already in memory, and the docs filter and the git view's two filters
   *  narrow an inventory that is already here. A synchronous answer renders in the same tick, which matters
   *  because a counter that appears a microtask late is a counter a keystroke can
   *  overtake — and it keeps the substrate out of the contract, so a box does not
   *  have to pretend to be asynchronous to use this shell. */
  query: (query: string, ctx: SearchQueryContext) => R | null | Promise<R | null>;
  /** Paint a result. Called only when the query it answers is still current. */
  render: (result: R | null, query: string) => void;
  /** Escape, and the × when there is one. */
  onDismiss?: () => void;
  /** Enter. `shift` is true for Shift+Enter (a cursor's "previous"). */
  onSubmit?: (shift: boolean) => void;
}

export interface SearchShell {
  readonly region: HTMLElement;
  readonly input: HTMLInputElement;
  readonly note: HTMLElement | null;
  readonly caseButton: HTMLButtonElement | null;
  readonly caseSensitive: boolean;
  /** The query text, trimmed of nothing — a trailing space is a real query. */
  readonly value: string;
  /** Run now, cancelling any pending debounce. */
  run: () => void;
  /** Run after the debounce, coalescing with a burst. */
  schedule: () => void;
  /** Drop the pending debounce and abort any in-flight query. Every consumer's
   *  close path calls this; nothing else has to remember the two halves. */
  cancel: () => void;
  /** Focus and select, the gesture every open performs. */
  focus: () => void;
  setNote: (text: string) => void;
}

/**
 * Build one search box and own its query lifecycle.
 *
 * THE SUPERSESSION GUARD IS DOUBLE, and both halves are needed. The
 * `AbortSignal` cancels the transport, which is what stops a stale response
 * being decoded; the value comparison on resolve is what stops a response that
 * already arrived from painting over a newer query, since a fetch that completed
 * cannot be aborted. All three hand-written copies re-checked the box's value
 * against the in-flight query, which is the tell that this belonged in one place.
 */
/** Duck-typed rather than `instanceof Promise`: an api-client helper may return a
 *  thenable from a different realm, and the only property this branch needs is
 *  the one that decides whether to await. */
function isThenable<R>(v: R | null | Promise<R | null>): v is Promise<R | null> {
  return typeof (v as { then?: unknown } | null)?.then === "function";
}

export function createSearchShell<R>(spec: SearchShellSpec<R>): SearchShell {
  let caseSensitive = false;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let inFlight: AbortController | null = null;

  const input = searchField({
    id: `${spec.id}-input`,
    className: spec.inputClass,
    label: spec.label,
    placeholder: spec.placeholder,
    ...(spec.inputTitle !== undefined ? { title: spec.inputTitle } : {}),
    ...(spec.inputType !== undefined ? { type: spec.inputType } : {}),
  });

  const note =
    spec.note === true ? statusNote(`${spec.id}-note`, spec.noteClass ?? "search-note") : null;

  const dismiss = (): void => {
    spec.onDismiss?.();
  };

  const caseButton =
    spec.matchCase === true
      ? matchCaseButton(
          `${spec.buttonClass} ${spec.caseClass ?? ""}`.trim(),
          caseSensitive,
          (on) => {
            caseSensitive = on;
            // FORCED, not scheduled. The query STRING did not change, so every
            // guard that compares it would treat this as a no-op — while the
            // match SET changed underneath, which is the whole point of the
            // toggle.
            run();
          },
        )
      : null;

  const closeButton =
    spec.closeButton === true
      ? searchIconButton(
          spec.buttonClass,
          `Close ${spec.closeNoun ?? "find"}`,
          "Close (Esc)",
          CLOSE_GLYPH,
          dismiss,
        )
      : null;

  const region = searchRegion({
    id: spec.id,
    className: spec.regionClass,
    label: spec.label,
  });
  for (const part of spec.compose({ input, caseButton, note, closeButton })) {
    if (part !== null) {
      region.appendChild(part);
    }
  }

  function cancel(): void {
    if (timer !== undefined) {
      clearTimeout(timer);
      timer = undefined;
    }
    inFlight?.abort();
    inFlight = null;
  }

  function run(): void {
    if (timer !== undefined) {
      clearTimeout(timer);
      timer = undefined;
    }
    inFlight?.abort();
    const ctrl = new AbortController();
    inFlight = ctrl;
    const issued = input.value;
    const result = spec.query(issued, { caseSensitive, signal: ctrl.signal });
    if (!isThenable<R>(result)) {
      // Synchronous substrate: render in this tick. Wrapping it in a resolved
      // promise would push the paint a microtask out for no reason and let a fast
      // keystroke land between the run and its own result.
      spec.render(result, issued);
      return;
    }
    void result
      .then((res) => {
        // Superseded: a newer keystroke is in the box, or this query was
        // cancelled while its transport was still open.
        if (ctrl.signal.aborted || input.value !== issued) {
          return;
        }
        spec.render(res, issued);
      })
      .catch((e: unknown) => {
        if (ctrl.signal.aborted) {
          return;
        }
        console.warn(`[${spec.id}] search failed`, e);
      });
  }

  function schedule(): void {
    if (timer !== undefined) {
      clearTimeout(timer);
    }
    timer = setTimeout(() => {
      timer = undefined;
      run();
    }, spec.debounceMs ?? SEARCH_DEBOUNCE_MS);
  }

  input.addEventListener("input", schedule);
  wireSearchKeys(input, {
    onDismiss: dismiss,
    ...(spec.onSubmit !== undefined ? { onSubmit: spec.onSubmit } : {}),
  });

  return {
    region,
    input,
    note,
    caseButton,
    get caseSensitive(): boolean {
      return caseSensitive;
    },
    get value(): string {
      return input.value;
    },
    run,
    schedule,
    cancel,
    focus(): void {
      input.focus();
      input.select();
    },
    setNote(text: string): void {
      if (note !== null) {
        note.textContent = text;
      }
    },
  };
}
