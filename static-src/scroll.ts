// ---------------------------------------------------------------------------
// Scroll management for the message list.
//
// Auto-scroll during streaming: MutationObserver on the messages
// container fires on every chunk mutation; a ResizeObserver catches
// images loading / code blocks expanding. When the user is at the
// bottom (within 100px tolerance) and hasn't scrolled upward in the
// last 150ms, we call scrollTo({behavior: "instant"}) inside a RAF.
// "instant" — not "smooth" — is the correct choice here: each chunk's
// small scroll delta compounds into perceptually continuous motion,
// while "smooth" schedules a 250ms animation that's cancelled by the
// next chunk and produces visible stutter.
//
// Reference: vercel/ai-chatbot hooks/use-scroll-to-bottom.tsx.
//
// Imperative scroll() is kept as a public no-op shim so existing
// callers (messages.ts, tool-card.ts, permission.ts) continue to
// compile unchanged. The observer does the real work.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { loadMoreSkeleton } from "./skeleton.js";
import { $ } from "./dom.js";

// Tuning constants for the scroll controller.
// LOAD_MORE_THRESHOLD_PX: distance from the top at which we trigger
// loading older messages into the DOM.
const LOAD_MORE_THRESHOLD_PX = 100;
// BOTTOM_TOLERANCE_PX: distance from the bottom within which we
// consider the user "at the bottom" for auto-scroll purposes.
// Same numeric value as LOAD_MORE_THRESHOLD_PX by coincidence — they
// are semantically independent.
const BOTTOM_TOLERANCE_PX = 100;
// USER_SCROLL_DEBOUNCE_MS: debounce interval for user scroll events
// before re-evaluating the "scrolled up" state.
const USER_SCROLL_DEBOUNCE_MS = 150;

// ---------------------------------------------------------------------------
// ScrollController: owns all scroll/pagination state as instance fields.
// ---------------------------------------------------------------------------

class ScrollController {
  private readonly messagesEl: HTMLElement;
  readonly scrollEl: HTMLElement;

  private userScrolledUp = false;
  private userScrollingUntil = 0;
  private suppressUntil = 0;

  private hasMoreMessages = false;
  private loadingMore = false;
  private onLoadMore: (() => void) | null = null;

  constructor(messagesEl: HTMLElement, scrollEl: HTMLElement) {
    this.messagesEl = messagesEl;
    this.scrollEl = scrollEl;
  }

  init(): void {
    const scrollBtn = $.scrollBottom;
    this.scrollEl.addEventListener(
      "scroll",
      () => {
        if (Date.now() < this.suppressUntil) {
          return;
        }
        this.userScrollingUntil = Date.now() + USER_SCROLL_DEBOUNCE_MS;
        const atBottom = this.isAtBottom();
        this.userScrolledUp = !atBottom;
        scrollBtn.classList.toggle("hidden", !this.userScrolledUp);
        this.maybeLoadMore();
      },
      { passive: true },
    );

    scrollBtn.addEventListener("click", () => {
      this.scrollEl.scrollTo({ top: this.scrollEl.scrollHeight, behavior: "smooth" });
      this.userScrolledUp = false;
      scrollBtn.classList.add("hidden");
    });

    const mutationObserver = new MutationObserver(() => {
      this.autoScrollIfAnchored();
    });
    mutationObserver.observe(this.messagesEl, {
      childList: true,
      subtree: true,
      characterData: true,
    });

    const resizeObserver = new ResizeObserver(() => {
      this.autoScrollIfAnchored();
    });
    resizeObserver.observe(this.scrollEl);
    const observed = new Set<Element>();
    const reobserveChildren = (): void => {
      const current = new Set<Element>(this.messagesEl.children);
      // Unobserve removed children.
      for (const child of observed) {
        if (!current.has(child)) {
          resizeObserver.unobserve(child);
          observed.delete(child);
        }
      }
      // Observe new children.
      for (const child of current) {
        if (!observed.has(child)) {
          resizeObserver.observe(child);
          observed.add(child);
        }
      }
    };
    reobserveChildren();
    const childObserver = new MutationObserver(reobserveChildren);
    childObserver.observe(this.messagesEl, { childList: true });
  }

  // --- Public API ---

  suppressScroll(ms: number): void {
    this.suppressUntil = Date.now() + ms;
  }

  setUserScrolledUp(v: boolean): void {
    this.userScrolledUp = v;
  }

  /** No-op shim. The MutationObserver auto-scrolls on every DOM change. */
  scroll(): void {
    /* observer-driven; see init */
  }

  scrollToBottom(): void {
    this.userScrolledUp = false;
    this.userScrollingUntil = 0;
    requestAnimationFrame(() => {
      this.scrollEl.scrollTop = this.scrollEl.scrollHeight;
      $.scrollBottom.classList.add("hidden");
    });
  }

  setLoadMore(fn: (() => void) | null, hasMore: boolean): void {
    this.onLoadMore = fn;
    this.hasMoreMessages = hasMore;
    this.updateLoadMoreIndicator();
  }

  resetScrollState(): void {
    this.onLoadMore = null;
    this.hasMoreMessages = false;
    this.loadingMore = false;
    this.userScrolledUp = false;
    this.userScrollingUntil = 0;
  }

  // --- Internal ---

  private isAtBottom(): boolean {
    return (
      this.scrollEl.scrollTop + this.scrollEl.clientHeight >=
      this.scrollEl.scrollHeight - BOTTOM_TOLERANCE_PX
    );
  }

  private rafPending = false;

  private autoScrollIfAnchored(): void {
    if (this.userScrolledUp) {
      return;
    }
    if (Date.now() < this.userScrollingUntil) {
      return;
    }
    if (Date.now() < this.suppressUntil) {
      return;
    }
    if (this.rafPending) {
      return;
    }
    this.rafPending = true;
    requestAnimationFrame(() => {
      this.rafPending = false;
      this.scrollEl.scrollTo({ top: this.scrollEl.scrollHeight, behavior: "instant" });
      $.scrollBottom.classList.add("hidden");
    });
  }

  private maybeLoadMore(): void {
    if (this.scrollEl.scrollTop >= LOAD_MORE_THRESHOLD_PX) {
      return;
    }
    if (!this.hasMoreMessages || this.loadingMore || this.onLoadMore === null) {
      return;
    }
    this.loadingMore = true;
    const skel = loadMoreSkeleton();
    skel.id = "load-more-skeleton";
    const indicator = document.getElementById("load-more-indicator");
    if (indicator !== null) {
      indicator.replaceWith(skel);
    } else {
      this.messagesEl.prepend(skel);
    }
    const prevHeight = this.scrollEl.scrollHeight;
    this.onLoadMore();
    const observer = new MutationObserver(() => {
      if (document.getElementById("load-more-skeleton") === null) {
        observer.disconnect();
        clearTimeout(safetyTimer);
        const newHeight = this.scrollEl.scrollHeight;
        this.scrollEl.scrollTop += newHeight - prevHeight;
        this.loadingMore = false;
      }
    });
    const safetyTimer = setTimeout(() => {
      observer.disconnect();
      const skel = document.getElementById("load-more-skeleton");
      if (skel !== null) {
        skel.remove();
      }
      this.loadingMore = false;
    }, 15_000);
    observer.observe(this.messagesEl, { childList: true });
  }

  private updateLoadMoreIndicator(): void {
    let indicator = document.getElementById("load-more-indicator");
    if (this.hasMoreMessages && this.onLoadMore !== null) {
      if (indicator === null) {
        indicator = el(
          "div",
          { id: "load-more-indicator", className: "message system" },
          "Scroll up for older messages",
        );
        this.messagesEl.prepend(indicator);
      }
    } else if (indicator !== null) {
      indicator.remove();
    }
  }
}

// ---------------------------------------------------------------------------
// Singleton instance + function exports that form the module's public API.
// ---------------------------------------------------------------------------

let instance: ScrollController | null = null;

function getInstance(): ScrollController {
  if (instance === null) {
    instance = new ScrollController($.messages, $.messagesWrap);
    instance.init();
  }
  return instance;
}

/** Deferred DOM access — safe to import before DOMContentLoaded. */
export function getScrollEl(): HTMLElement {
  return getInstance().scrollEl;
}

export function setUserScrolledUp(v: boolean): void {
  getInstance().setUserScrolledUp(v);
}
export function scroll(): void {
  getInstance().scroll();
}
export function scrollToBottom(): void {
  getInstance().scrollToBottom();
}
export function setLoadMore(fn: (() => void) | null, hasMore: boolean): void {
  getInstance().setLoadMore(fn, hasMore);
}
export function resetScrollState(): void {
  getInstance().resetScrollState();
}

// Init on load.
if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", () => {
    getInstance();
  });
} else {
  getInstance();
}
