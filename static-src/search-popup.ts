// The page-level search box: `search-shell.ts`'s field under the transcript's
// reveal, for every page whose search narrows or re-scopes a LIST. It owns the
// popup lifecycle (outside click, document-level Escape, the single-open group,
// the trigger's ARIA), the hidden-before-first-open normalization, focus save and
// restore, and one rule the shell has no reason to have:
//
//   CLOSING CLEARS THE QUERY, because a hidden box is not its own explanation for
//   a narrowed list. One closed holding `redis` leaves three of forty rows on
//   screen with nothing saying why.
//
// The transcript's box stays in find-in-chat.ts: it has a cursor, its teardown
// unwraps DOM it wrote into the page, and its Escape must not clear.

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

export interface SearchPopupSpec<R> {
  /** Element id prefix. The input becomes `<id>-input`, the note `<id>-note`. */
  id: string;
  /** Search or filter: decides the glyph and the control wording, nothing
   *  structural. `findGlyph` owns which mark means what, and it feeds the toolbar
   *  button too, so a page cannot promise a search and open a filter. */
  kind: FindKind;
  /** The region's accessible name. */
  label: string;
  /** States the SCOPE — which conversations, which rows — where the glyph states
   *  only the reach. */
  placeholder: string;
  /** Offer the status note. */
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
  /** The shell, for a caller that needs the field itself. Null until the first
   *  open builds it. */
  readonly shell: SearchShell | null;
  isOpen: () => boolean;
  /** Open, or refocus an already-open box. False means there was no host to build
   *  into, so the caller leaves Ctrl-F to the browser's native find. */
  open: () => boolean;
  /** Close, clearing the query so the page repaints unfiltered. */
  close: () => void;
  /** Close and clear WITHOUT the repaint, for a page tearing its view down: the
   *  render belongs to the next mount. */
  reset: () => void;
  toggle: () => void;
  /** Whether the caret is in this box. Ctrl-F again from inside an open find
   *  belongs to the browser. */
  focused: () => boolean;
}

/** One page search popup, built on first open: these pages are lazily loaded, so
 *  the host arrives with them. */
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
    const verb = spec.kind === "search" ? "Search" : "Filter";
    const glyph = iconEl(findGlyph(spec.kind));
    // ADD, never `setAttribute("class", …)`: the tier class is this glyph's only
    // source of size, and a flex row may not squeeze an unsized SVG back.
    glyph.classList.add("page-find-icon");
    const built = createSearchShell<R>({
      id: spec.id,
      // Placement, then the reveal skin the transcript's box shares, then the
      // primitive's hook. 24-find.css owns all three.
      regionClass: "page-find search-pop uip-popup",
      inputClass: "page-find-input",
      buttonClass: "page-find-btn",
      noteClass: "page-find-note",
      label: spec.label,
      placeholder: spec.placeholder,
      inputTitle: `${verb} this page. Press Ctrl+F again to use the browser's find.`,
      // No `type="search"`: two clear controls a thumb-width apart, doing
      // different things, is worse than one.
      ...(spec.note === true ? { note: true } : {}),
      ...(spec.debounceMs !== undefined ? { debounceMs: spec.debounceMs } : {}),
      closeButton: true,
      closeNoun: verb.toLowerCase(),
      // No `Aa`: a filter folds both sides of its comparison, and the one page
      // search is case-insensitive at the endpoint.
      compose: ({ input, note, closeButton }) => [
        el("div", { className: "page-find-row" }, glyph, input, closeButton),
        note,
      ],
      query: spec.query,
      render: spec.render,
      // Escape CLOSES, and the close is what clears.
      onDismiss: close,
      onSubmit:
        spec.onSubmit ??
        ((): void => {
          shell?.run();
        }),
    });
    shell = built;

    // Hidden before the first open: the primitive writes `[hidden]` only at the END
    // of a leave, so this `opacity: 0` fixed box would otherwise swallow every
    // click in its rectangle before search had ever been opened.
    built.region.hidden = true;
    host.appendChild(built.region);

    popup = createPopup(built.region, {
      trigger: trigger(),
      group: GROUP,
      // The app's global Escape coordinator still sees the key.
      isolateEscape: false,
      haspopup: "dialog",
      onOpen: () => {
        // aria-pressed, not aria-expanded: find is a TOGGLE, and 70-selection.css
        // already paints `.icon-btn[aria-pressed="true"]`.
        trigger()?.setAttribute("aria-pressed", "true");
      },
      onClose: () => {
        built.cancel();
        // Guarded, so closing an untouched box is not a refetch.
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
    // Focus here rather than in onOpen alone: `show()` on an open popup is a no-op
    // reveal, and both doors have to land the caret in the box.
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
