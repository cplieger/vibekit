// ---------------------------------------------------------------------------
// Source Control empty-state component.
//
// Two variants, picked by the orchestrator (git.ts) based on the
// repo-picker registry summary:
//
//   forges-needed   — no forge connected anywhere. Single CTA routes
//                     the user to /settings/git to connect one.
//
//   pick-or-clone   — at least one forge connected, but no repo
//                     currently selected. Single CTA opens the picker.
//
// Mounts on <div id="git-empty-state">. When visible, the toolbar +
// repo panel are hidden by git.ts.
// ---------------------------------------------------------------------------

export type EmptyStateVariant = "forges-needed" | "pick-or-clone";

interface Callbacks {
  /** Routed CTA for forges-needed. */
  onConnectForge: () => void;
  /** Routed CTA for pick-or-clone. */
  onChooseRepo: () => void;
}

class GitEmptyState {
  private root: HTMLElement | null = null;
  private callbacks: Callbacks | null = null;
  private current: EmptyStateVariant | null = null;

  init(callbacks: Callbacks): void {
    this.root = document.getElementById("git-empty-state");
    this.callbacks = callbacks;
    this.hide();
  }

  show(variant: EmptyStateVariant): void {
    if (this.root === null) return;
    if (this.current === variant) {
      this.root.classList.remove("hidden");
      return;
    }
    this.current = variant;
    this.root.replaceChildren(this.build(variant));
    this.root.classList.remove("hidden");
  }

  hide(): void {
    if (this.root === null) return;
    this.current = null;
    this.root.replaceChildren();
    this.root.classList.add("hidden");
  }

  /** Test seam: tear down internal state. */
  reset(): void {
    this.current = null;
    if (this.root !== null) {
      this.root.replaceChildren();
      this.root.classList.add("hidden");
    }
  }

  private build(variant: EmptyStateVariant): HTMLElement {
    const card = document.createElement("div");
    card.className = "git-empty-state-card";
    card.dataset["variant"] = variant;

    const iconEl = document.createElement("div");
    iconEl.className = "git-empty-state-icon";
    iconEl.setAttribute("aria-hidden", "true");
    iconEl.innerHTML = iconFor(variant);
    card.appendChild(iconEl);

    const headline = document.createElement("h3");
    headline.className = "git-empty-state-headline";
    headline.textContent = headlineFor(variant);
    card.appendChild(headline);

    const sub = document.createElement("p");
    sub.className = "git-empty-state-sub";
    sub.textContent = subFor(variant);
    card.appendChild(sub);

    const cta = document.createElement("button");
    cta.type = "button";
    cta.className = "btn-small btn-primary git-empty-state-cta";
    cta.dataset["emptyStateCta"] = variant;
    cta.textContent = ctaLabelFor(variant);
    cta.addEventListener("click", () => this.fireCTA(variant));
    card.appendChild(cta);

    return card;
  }

  private fireCTA(variant: EmptyStateVariant): void {
    if (this.callbacks === null) return;
    if (variant === "forges-needed") this.callbacks.onConnectForge();
    else this.callbacks.onChooseRepo();
  }
}

// Inline SVGs sized to match the rest of the UI (24px). Stroke-only
// glyphs so they tint with currentColor.
const ICON_CLOUD_OFF =
  '<svg width="48" height="48" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">' +
  '<path d="M2 2l20 20"/>' +
  '<path d="M5.78 5.78A4 4 0 008 13h7M9 4.22A6 6 0 0117 8h.5a4.5 4.5 0 014 6.5"/>' +
  "</svg>";

const ICON_REPO =
  '<svg width="48" height="48" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">' +
  '<path d="M4 19.5A2.5 2.5 0 016.5 17H20"/>' +
  '<path d="M6.5 2H20v20H6.5A2.5 2.5 0 014 19.5v-15A2.5 2.5 0 016.5 2z"/>' +
  "</svg>";

function iconFor(variant: EmptyStateVariant): string {
  return variant === "forges-needed" ? ICON_CLOUD_OFF : ICON_REPO;
}

function headlineFor(variant: EmptyStateVariant): string {
  return variant === "forges-needed"
    ? "No repositories yet"
    : "Pick a repository to start";
}

function subFor(variant: EmptyStateVariant): string {
  return variant === "forges-needed"
    ? "Connect a forge to bring your GitHub, GitLab, Codeberg or Gitea repositories here."
    : "Choose one from your connected forges.";
}

function ctaLabelFor(variant: EmptyStateVariant): string {
  return variant === "forges-needed" ? "Connect a forge" : "Choose a repository";
}

const emptyState = new GitEmptyState();

export function initGitEmptyState(callbacks: Callbacks): void { emptyState.init(callbacks); }
export function showGitEmptyState(variant: EmptyStateVariant): void { emptyState.show(variant); }
export function hideGitEmptyState(): void { emptyState.hide(); }

/** Test-only: drop internal state between cases. */
export function _resetGitEmptyState(): void { emptyState.reset(); }
