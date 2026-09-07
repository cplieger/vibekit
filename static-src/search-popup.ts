// ---------------------------------------------------------------------------
// The page-level search box, as a popup.
//
// FOUR PAGES HAD FOUR ANSWERS to the same question — History, the configuration
// browser, the git Changes tab and the git Pull-requests tab — and none of them
// was the transcript's. Two were permanent in-flow boxes built through the
// shared shell; two were hand-authored `<input type="search">` elements in
// index.html with a magnifier SVG beside them and no shell at all. So a reader
// who learned Ctrl-F on a chat found a floating box with a ×, then found a
// full-width field on /history, then found nothing at all on the git view, where
// the toolbar's magnifier was a dead door.
//
// This module is the one answer: the transcript's reveal, over the shared shell,
// for every page whose search narrows or re-scopes a LIST. `search-shell.ts`
// deliberately does not own reveal, and that reasoning still holds — it declined
// to share reveal because the surfaces genuinely differed, and reveal is
// placement's consequence. What changed is that four of them now WANT the same
// placement, so the sharing has a subject.
//
// WHAT IS SHARED HERE, on top of the shell: the popup lifecycle (outside click,
// document-level Escape, the single-open group, the trigger's ARIA), the
// hidden-before-first-open normalization, focus save and restore, the toolbar
// magnifier as the trigger, and the ONE rule a hidden filter needs that a
// permanent one did not:
//
//   CLOSING CLEARS THE QUERY. A permanent box is its own explanation for a
//   narrowed list; a hidden one is not. A popup that closed holding `redis`
//   would leave the page showing three of forty rows with nothing on screen
//   saying why, and the only way back would be to reopen a box the reader has
//   no reason to think is still armed. So close() empties the field and re-runs,
//   which is what repaints the full list.
//
// WHAT STAYS PER SURFACE: the placeholder (it is where the scope is stated — a
// cross-chat search and a metadata filter ask different questions), the query
// and render functions, and whether there is a note. Not the classes: every page
// popup carries the same ones, because looking the same is the point.
//
// NOT A HOME FOR THE TRANSCRIPT'S OWN BOX. find-in-chat.ts keeps its own
// createPopup call: it has a cursor (marks, prev/next, a counter, scroll-into-
// view), its teardown unwraps DOM it wrote into the page, and its Escape must
// not clear — reopening on the same chat remembering the query is what the
// browser's own find does. Folding those into this module would put its
// differences inside branches here.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { createPopup } from "@cplieger/ui-primitives/popup";
import type { PopupController } from "@cplieger/ui-primitives/popup";
import { createSearchShell } from "./search-shell.js";
import type { SearchShell, SearchShellSpec } from "./search-shell.js";
import { findGlyph } from "./icons.js";
import { iconEl } from "./icon-el.js";
import type { FindKind, PageFind } from "./find-registry.js";

/** The toolbar magnifier. One button, one popup group, so opening a page's
 *  search closes whatever else was open — including the transcript's, which
 *  shares this group. */
const TRIGGER_ID = "find-btn";
const GROUP = "app-search";

/** The leading glyph's size, matching the × beside it. */

export interface SearchPopupSpec<R> {
  /** Element id prefix. The input becomes `<id>-input`, the note `<id>-note`. */
  id: string;
  /** Search or filter. Decides the leading glyph and the control wording, and
   *  nothing structural — one component, two readings.
   *
   *  A MAGNIFIER for a box that reaches past what is on screen (History reads
   *  every chat file on disk); a FUNNEL for one that narrows rows already loaded
   *  (the configuration browser, the git panels). The distinction was already in
   *  this codebase as prose and the boxes disagreed with each other about it; it
   *  is a parameter now, sharing one glyph producer with the toolbar button, so a
   *  page cannot promise a search and open a filter. */
  kind: FindKind;
  /** The region's accessible name. */
  label: string;
  /** The one string these boxes genuinely differ on beyond their kind: it states
   *  the SCOPE — which conversations, which rows — where the glyph states only the
   *  reach. */
  placeholder: string;
  /** Offer the status note.
   *
   *  It reads differently per kind, and both readings are load-bearing: a filter's
   *  note says how much of the list is showing, while a search's says what it did
   *  NOT read, because "no matches" from a truncated scan implies the text is
   *  nowhere. */
  note?: boolean;
  /** Typing pause before a run. The shell's default unless the query costs a
   *  request per pause. */
  debounceMs?: number;
  /** Where the region mounts. Resolved on the first open, not at import: every
   *  page here is lazily loaded, so its host may not exist yet. */
  host: () => HTMLElement | null;
  query: SearchShellSpec<R>["query"];
  render: SearchShellSpec<R>["render"];
  /** Enter. Defaults to re-running the query. */
  onSubmit?: (shift: boolean) => void;
}

/** A page popup satisfies `PageFind` by construction, so a page hands the object
 *  itself to the find registry rather than adapting it. */
export interface SearchPopup extends PageFind {
  /** The shell, for a caller that needs the field itself (a test, a page that
   *  reads the current value). Null until the first open builds it. */
  readonly shell: SearchShell | null;
  isOpen: () => boolean;
  /** Open, or refocus an already-open box. False means the page declined —
   *  there was no host to build into — so the caller leaves Ctrl-F to the
   *  browser's native find. */
  open: () => boolean;
  /** Close, clearing the query so the page repaints unfiltered. */
  close: () => void;
  /** Close and clear WITHOUT the repaint — for a page tearing its view down,
   *  where the render the clear exists for is the next mount's job and running
   *  it here would be a fetch for a page that just went away. */
  reset: () => void;
  toggle: () => void;
  /** Whether the caret is in this box. The second-press escape hatch: Ctrl-F
   *  again from inside an open find belongs to the browser. */
  focused: () => boolean;
}

/**
 * One page search popup, built on first open.
 *
 * Lazy because these pages are lazily loaded and their hosts arrive with them;
 * eager construction would also put a `position: fixed` panel in the layout
 * before anything had asked for it, which is the trap `[hidden]` normalization
 * below exists to close.
 */
export function createSearchPopup<R>(spec: SearchPopupSpec<R>): SearchPopup {
  let shell: SearchShell | null = null;
  let popup: PopupController | null = null;
  let lastFocus: HTMLElement | null = null;

  function trigger(): HTMLElement | null {
    return document.getElementById(TRIGGER_ID);
  }

  function close(): void {
    popup?.hide();
  }

  function build(): boolean {
    if (shell !== null) {
      return true;
    }
    const host = spec.host();
    if (host === null) {
      return false;
    }
    // The kind's two visible consequences, resolved once. `verb` is the wording
    // for every control in the box; `glyph` is the same node the toolbar button
    // paints, from the same producer.
    const verb = spec.kind === "search" ? "Search" : "Filter";
    const glyph = iconEl(findGlyph(spec.kind));
    glyph.setAttribute("class", "page-find-icon");
    const built = createSearchShell<R>({
      id: spec.id,
      // `page-find` is placement and layout; `search-pop` is the skin and the
      // reveal states, shared with the transcript's box; `uip-popup` is the
      // primitive's own hook. 24-find.css owns all three.
      regionClass: "page-find search-pop uip-popup",
      inputClass: "page-find-input",
      buttonClass: "page-find-btn",
      noteClass: "page-find-note",
      label: spec.label,
      placeholder: spec.placeholder,
      // The chord's escape hatch, in the words of whichever thing this is.
      inputTitle: `${verb} this page. Press Ctrl+F again to use the browser's find.`,
      // No `type="search"`: the platform's own clear affordance belongs on a
      // permanent box, and this one carries its own ×. Two clear controls a
      // thumb-width apart, doing different things, is worse than one.
      ...(spec.note === true ? { note: true } : {}),
      ...(spec.debounceMs !== undefined ? { debounceMs: spec.debounceMs } : {}),
      closeButton: true,
      closeNoun: verb.toLowerCase(),
      // No `Aa` on either kind, and for two different reasons that happen to
      // agree here: a FILTER folds the query and the row it matches it against, so
      // there is nothing a toggle could change; and the one page SEARCH is
      // case-insensitive at the endpoint by decision (`chat.searchOneChat`), which
      // reads no `case` parameter at all. The surfaces that DO offer it are the
      // ones with a cursor — the transcript and the file browser — and they build
      // it through the shell directly.
      compose: ({ input, note, closeButton }) => [
        el("div", { className: "page-find-row" }, glyph, input, closeButton),
        note,
      ],
      query: spec.query,
      render: spec.render,
      // Escape CLOSES, and the close is what clears. On a permanent box Escape
      // meant "clear", because there was nothing to dismiss; here the two
      // collapse into one gesture with one outcome.
      onDismiss: close,
      onSubmit:
        spec.onSubmit ??
        ((): void => {
          shell?.run();
        }),
    });
    shell = built;

    // HIDDEN BEFORE THE FIRST OPEN. The primitive only writes `[hidden]` at the
    // END of a leave, so a freshly built panel is visible to the layout — and
    // this one is a fixed-position box at `opacity: 0` over the page, so without
    // this it would swallow every click in its rectangle before search had ever
    // been opened. find-in-chat.ts and pill-expand.ts normalize the same way.
    built.region.hidden = true;
    host.appendChild(built.region);

    popup = createPopup(built.region, {
      trigger: trigger(),
      group: GROUP,
      // The app's global Escape coordinator still sees the key, the same
      // contract find-in-chat.ts and pill-expand.ts keep.
      isolateEscape: false,
      haspopup: "dialog",
      onOpen: () => {
        // aria-pressed, not aria-expanded: find is a TOGGLE. 70-selection.css
        // already styles `.icon-btn[aria-pressed="true"]`, so the visual is the
        // app's one selected treatment with no local rule.
        trigger()?.setAttribute("aria-pressed", "true");
      },
      onClose: () => {
        built.cancel();
        // The clear is guarded on there being something to clear, so closing an
        // untouched box is not a refetch of the list already on screen.
        if (built.input.value !== "") {
          built.input.value = "";
          built.run();
        }
        trigger()?.setAttribute("aria-pressed", "false");
        const target = lastFocus;
        lastFocus = null;
        if (target?.isConnected === true) {
          target.focus();
        }
      },
    });
    return true;
  }

  function isOpen(): boolean {
    return popup?.isOpen === true;
  }

  function open(): boolean {
    if (!build() || popup === null || shell === null) {
      return false;
    }
    if (!popup.isOpen) {
      lastFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    }
    // show() on an already-open popup is a no-op reveal, so the focus happens
    // here rather than in onOpen alone: the toolbar button and the chord both
    // reach an open box and both should land the caret in it.
    popup.show();
    shell.focus();
    return true;
  }

  return {
    get shell(): SearchShell | null {
      return shell;
    },
    kind: () => spec.kind,
    isOpen,
    open,
    close,
    reset(): void {
      // Emptied first, so the close's own clear-and-repaint sees nothing to do.
      if (shell !== null) {
        shell.input.value = "";
      }
      close();
    },
    toggle(): void {
      if (isOpen()) {
        close();
        return;
      }
      open();
    },
    focused(): boolean {
      return shell !== null && document.activeElement === shell.input;
    },
  };
}
