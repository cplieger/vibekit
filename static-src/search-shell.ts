// The search box every surface shares: the field's attributes, the debounce, the
// supersession guard, the `Aa` toggle, the status note and the Escape/Enter
// contract. A caller supplies a placeholder, a query and a renderer, and owns
// PLACEMENT and REVEAL — a surface that differs arranges the built parts through
// `compose` rather than adding a mode flag here.

import { el } from "@cplieger/reactive";
import { iconEl } from "./icon-el.js";
import { ICON_CLOSE_UI } from "./icons.js";

/** The default typing pause. Overridable per box: the cross-chat search reads up
 *  to 500 files per query, so its pause is longer. */
export const SEARCH_DEBOUNCE_MS = 90;

/** The `?case=1` convention. Every server that takes it reads an ABSENT parameter
 *  as insensitive, so the flag is only ever sent when asked. */
export function caseParam(caseSensitive: boolean): string {
  return caseSensitive ? "1" : "";
}

/** An icon button for a search bar. The glyph must be an SVG: a text glyph is an
 *  anonymous flex item whose LINE BOX gets centred rather than its ink, and no
 *  authored offset corrects that across fonts. An SVG's box IS its ink box, and
 *  the button's `line-height: 0` (in CSS) collapses the strut around it. */
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
    "data-tooltip": hint,
  }) as HTMLButtonElement;
  btn.appendChild(iconEl(icon));
  btn.addEventListener("click", onClick);
  return btn;
}

/** The `Aa` toggle, the one search-bar button that keeps its text: the letters ARE
 *  the affordance. Its centring is CSS's `text-box: trim-both cap alphabetic`,
 *  which addresses the cap band and so works only on a letterform. Latched
 *  through `aria-pressed`; 70-selection.css owns the fill. */
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
      "data-tooltip": "Match case",
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

/** The query field's attribute set. `type` is the caller's: a `search` input draws
 *  the platform's own clear affordance, which belongs on a permanent box and not
 *  on one that carries its own ×. */
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

/** The "what wasn't read" line: a polite live region, so an empty answer cannot
 *  claim the text is nowhere when the scan simply stopped short. */
function statusNote(id: string, className: string): HTMLElement {
  return el("div", {
    id,
    className,
    role: "status",
    "aria-live": "polite",
    "aria-atomic": "true",
  });
}

/** The `role="search"` landmark, so every box is reachable by landmark
 *  navigation. */
function searchRegion(opts: { id: string; className: string; label: string }): HTMLElement {
  return el("div", {
    id: opts.id,
    className: opts.className,
    role: "search",
    "aria-label": opts.label,
  });
}

/** Escape and Enter on a search field. Escape is CONSUMED here
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
  /** Offer the `Aa` toggle. FALSE where the endpoint ignores the parameter, or the
   *  toggle silently does nothing. */
  matchCase?: boolean;
  /** Offer the status note. */
  note?: boolean;
  /** Offer a × that calls `onDismiss`. */
  closeButton?: boolean;
  /** What the × closes, for its accessible name, so the word a reader hears
   *  matches the glyph they see. "find" by default. */
  closeNoun?: string;
  /** Typing pause before a run. Defaults to SEARCH_DEBOUNCE_MS. */
  debounceMs?: number;
  /** Arrange the built parts into the region. Extra controls (a glob row, a
   *  match counter, prev/next) go in here. */
  compose: (parts: SearchShellParts) => (Node | null)[];
  /** Run one query. Null means "nothing to render"; a failed fetch is already
   *  logged by the api client. MAY BE SYNCHRONOUS, and four of the six boxes are:
   *  such an answer renders in the same tick, so a keystroke cannot overtake a
   *  counter that would otherwise appear a microtask late. */
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

/** Duck-typed rather than `instanceof Promise`: an api-client helper may return a
 *  thenable from another realm. */
function isThenable<R>(v: R | null | Promise<R | null>): v is Promise<R | null> {
  return typeof (v as { then?: unknown } | null)?.then === "function";
}

/** Build one search box and own its query lifecycle. */
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
            // Forced, not scheduled: the query string did not change, so every
            // guard that compares it would read this as a no-op.
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
          ICON_CLOSE_UI,
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
      // Rendered in this tick: a resolved promise here would let a keystroke land
      // between the run and its own result.
      spec.render(result, issued);
      return;
    }
    void result
      .then((res) => {
        // The value check is not redundant with the abort: a fetch that already
        // resolved cannot be cancelled, and would paint over a newer query.
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
