// ---------------------------------------------------------------------------
// Source Control status banner: a single-element, priority-routed banner
// that surfaces forge auth issues, gh-CLI install/login needs, and the
// "no forges connected" prompt.
//
// Replaces three previous mechanisms that could overlap on screen and
// each had its own inconsistencies:
//
//   - #git-forges-banner   — static element toggled by repo-picker
//   - #git-gh-section      — static button toggled by has_gh
//   - showForgeAuthBanner  — git.ts function that prepended a dynamic
//                            #forge-auth-banner to #git-panel /
//                            #git-section, neither of which exist in
//                            the DOM (silent no-op for forge auth
//                            failures since the migration).
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
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";

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

class StatusBanner {
  private root: HTMLElement | null = null;
  private active = new Set<BannerKey>();
  private dismissed: BannerKey | null = null;
  private callbacks: Callbacks | null = null;

  init(callbacks: Callbacks): void {
    this.root = document.getElementById("git-status-banner");
    this.callbacks = callbacks;
    this.render();
  }

  set(key: BannerKey): void {
    if (this.active.has(key)) {
      return;
    }
    this.active.add(key);
    // If a higher-priority key just became top-active, drop any prior
    // dismissal so the user sees the more urgent state.
    if (this.dismissed !== null && this.dismissed !== this.topKey()) {
      this.dismissed = null;
    }
    this.render();
  }

  clear(key: BannerKey): void {
    if (!this.active.has(key)) {
      return;
    }
    this.active.delete(key);
    if (this.dismissed === key) {
      this.dismissed = null;
    }
    this.render();
  }

  /** Test seam: drop all state. */
  reset(): void {
    this.active.clear();
    this.dismissed = null;
    if (this.root !== null) {
      this.root.replaceChildren();
      this.root.classList.add("hidden");
    }
  }

  private topKey(): BannerKey | null {
    for (const k of PRIORITY) {
      if (this.active.has(k)) {
        return k;
      }
    }
    return null;
  }

  private render(): void {
    if (this.root === null) {
      return;
    }
    const top = this.topKey();
    if (top === null || top === this.dismissed) {
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
      this.dismissed = key;
      this.render();
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
