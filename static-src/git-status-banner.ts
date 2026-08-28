// ---------------------------------------------------------------------------
// Source Control status banner: a single-element, priority-routed banner
// that surfaces forge auth issues, gh-CLI install/login needs, and the
// "no forges connected" prompt.
//
// Mount point: <div id="git-status-banner"> at the top of #git-view.
// At most one state renders at a time, prioritized:
//
//   1. forge-auth-failed       (active error after a git operation)
//   2. gh-cli-missing          (gh CLI not installed/authed for github)
//   3. forges-not-connected    (informational; no forge configured at all)
//
// User-dismissable per active key: dismissing forge-auth-failed hides
// only that state until it transitions (cleared and re-set, or a
// higher-priority state arrives).
//
// State is reactive: `active` (the logically-set keys) and `dismissed` are
// signals; `topKey` is a computed deriving the highest-priority *visible*
// banner (the raw top, or null when that top is dismissed). A single
// effect renders `topKey.value`, so every add / clear / dismiss collapses
// to a signal write instead of a manual render() call. The computed dedups
// (Object.is), so a set that doesn't change the visible top — e.g. a
// lower-priority key arriving under an already-shown one — does not
// re-render.
// ---------------------------------------------------------------------------

import {
  el,
  signal,
  computed,
  effect,
  batch,
  type Signal,
  type ReadonlySignal,
} from "@cplieger/reactive";

export type BannerKey = "forge-auth-failed" | "gh-cli-missing" | "forges-not-connected";

interface Callbacks {
  /** Routed CTA for "forge-auth-failed" and "forges-not-connected". */
  onConnectForge: () => void;
  /** Routed CTA for "gh-cli-missing". Should kick off gh install +
   *  `gh auth login` (today: gitGhAuth in git.ts). */
  onAuthenticateGh: () => void;
}

const PRIORITY: readonly BannerKey[] = [
  "forge-auth-failed",
  "gh-cli-missing",
  "forges-not-connected",
];

/** Highest-priority key present in the active set, ignoring dismissal. */
function rawTop(active: ReadonlySet<BannerKey>): BannerKey | null {
  for (const k of PRIORITY) {
    if (active.has(k)) {
      return k;
    }
  }
  return null;
}

class StatusBanner {
  private root: HTMLElement | null = null;
  private callbacks: Callbacks | null = null;
  private disposeEffect: (() => void) | null = null;

  private readonly active: Signal<ReadonlySet<BannerKey>> = signal<ReadonlySet<BannerKey>>(
    new Set<BannerKey>(),
  );
  private readonly dismissed: Signal<BannerKey | null> = signal<BannerKey | null>(null);

  // The visible banner: the highest-priority active key, or null when that
  // top key is the dismissed one. Dismissing the top hides the banner
  // entirely; it does not fall through to a lower-priority active key.
  private readonly topKey: ReadonlySignal<BannerKey | null> = computed<BannerKey | null>(() => {
    const top = rawTop(this.active.value);
    return top === null || top === this.dismissed.value ? null : top;
  });

  init(callbacks: Callbacks): void {
    this.root = document.getElementById("git-status-banner");
    this.callbacks = callbacks;
    // ONE render source. Created once (effects persist for the singleton's
    // lifetime); the immediate run on creation renders against the freshly
    // grabbed root. Later init() calls reuse the existing effect.
    this.disposeEffect ??= effect(() => {
      this.render(this.topKey.value);
    });
  }

  set(key: BannerKey): void {
    const cur = this.active.peek();
    if (cur.has(key)) {
      return;
    }
    const next = new Set(cur);
    next.add(key);
    // If a higher-priority key just became the raw top, drop any prior
    // dismissal so the user sees the more urgent state. Two writes, but
    // batched into a single effect run.
    const dism = this.dismissed.peek();
    const dropDismiss = dism !== null && dism !== rawTop(next);
    batch(() => {
      this.active.value = next;
      if (dropDismiss) {
        this.dismissed.value = null;
      }
    });
  }

  clear(key: BannerKey): void {
    const cur = this.active.peek();
    if (!cur.has(key)) {
      return;
    }
    const next = new Set(cur);
    next.delete(key);
    const dism = this.dismissed.peek();
    batch(() => {
      this.active.value = next;
      if (dism === key) {
        this.dismissed.value = null;
      }
    });
  }

  /** Test seam: drop all state. */
  reset(): void {
    batch(() => {
      this.active.value = new Set<BannerKey>();
      this.dismissed.value = null;
    });
  }

  /** Render the single visible banner row, or hide when `top` is null. */
  private render(top: BannerKey | null): void {
    if (this.root === null) {
      return;
    }
    if (top === null) {
      this.root.replaceChildren();
      this.root.classList.add("hidden");
      return;
    }
    this.root.classList.remove("hidden");
    this.root.replaceChildren(this.buildContent(top));
  }

  private buildContent(key: BannerKey): HTMLElement {
    const cta = el(
      "button",
      {
        type: "button",
        className: "btn-small git-status-banner-cta",
        "data-banner-cta": key,
      },
      ctaLabelFor(key),
    );
    cta.addEventListener("click", () => {
      this.fireCTA(key);
    });

    const dismiss = el(
      "button",
      {
        type: "button",
        className: "icon-btn git-status-banner-dismiss",
        "data-banner-dismiss": key,
        "aria-label": "Dismiss",
      },
      "✕",
    );
    dismiss.addEventListener("click", () => {
      this.dismissed.value = key;
    });

    return el(
      "div",
      {
        className: "git-status-banner-row",
        "data-state": key,
      },
      el(
        "span",
        { className: "git-status-banner-icon", "aria-hidden": "true" },
        key === "forge-auth-failed" ? "⚠" : "ⓘ",
      ),
      el("span", { className: "git-status-banner-msg" }, messageFor(key)),
      cta,
      dismiss,
    );
  }

  private fireCTA(key: BannerKey): void {
    if (this.callbacks === null) {
      return;
    }
    switch (key) {
      case "forge-auth-failed":
      case "forges-not-connected":
        this.callbacks.onConnectForge();
        break;
      case "gh-cli-missing":
        this.callbacks.onAuthenticateGh();
        break;
    }
  }
}

function messageFor(key: BannerKey): string {
  switch (key) {
    case "forge-auth-failed":
      return "Forge authentication issue — push, pull, or PR features may fail.";
    case "gh-cli-missing":
      return "GitHub CLI not authenticated. Sign in with gh to push and create PRs.";
    case "forges-not-connected":
      return "No forge connected — sign in to enable PRs, merging, and CI status.";
  }
}

function ctaLabelFor(key: BannerKey): string {
  switch (key) {
    case "forge-auth-failed":
    case "forges-not-connected":
      return "Open Settings → Git";
    case "gh-cli-missing":
      return "Authenticate with gh";
  }
}

const banner = new StatusBanner();

export function initStatusBanner(callbacks: Callbacks): void {
  banner.init(callbacks);
}
export function setBanner(key: BannerKey): void {
  banner.set(key);
}
export function clearBanner(key: BannerKey): void {
  banner.clear(key);
}

/** Test-only: reset internal state between cases. */
export function _resetBannerState(): void {
  banner.reset();
}
