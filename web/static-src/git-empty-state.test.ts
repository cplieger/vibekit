// @vitest-environment happy-dom
import { describe, it, expect, vi, beforeEach } from "vitest";

import {
  initGitEmptyState, showGitEmptyState, hideGitEmptyState,
  _resetGitEmptyState,
} from "./git-empty-state.js";

function setupDOM(): void {
  document.body.innerHTML = `<div id="git-empty-state" class="hidden"></div>`;
}

function rootEl(): HTMLElement {
  return document.getElementById("git-empty-state") as HTMLElement;
}

function variant(): string | null {
  const card = rootEl().querySelector<HTMLElement>(".git-empty-state-card");
  return card?.dataset["variant"] ?? null;
}

describe("git-empty-state", () => {
  let onConnectForge: ReturnType<typeof vi.fn>;
  let onChooseRepo: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    setupDOM();
    onConnectForge = vi.fn();
    onChooseRepo = vi.fn();
    _resetGitEmptyState();
    initGitEmptyState({ onConnectForge, onChooseRepo });
  });

  it("starts hidden", () => {
    expect(rootEl().classList.contains("hidden")).toBe(true);
    expect(variant()).toBeNull();
  });

  it("shows forges-needed variant with the right copy + CTA", () => {
    showGitEmptyState("forges-needed");
    expect(rootEl().classList.contains("hidden")).toBe(false);
    expect(variant()).toBe("forges-needed");
    expect(rootEl().textContent).toContain("No repositories yet");
    expect(rootEl().textContent).toContain("Connect a forge");
    const cta = rootEl().querySelector<HTMLButtonElement>("[data-empty-state-cta]");
    expect(cta?.textContent).toBe("Connect a forge");
  });

  it("shows pick-or-clone variant with the right copy + CTA", () => {
    showGitEmptyState("pick-or-clone");
    expect(variant()).toBe("pick-or-clone");
    expect(rootEl().textContent).toContain("Pick a repository to start");
    const cta = rootEl().querySelector<HTMLButtonElement>("[data-empty-state-cta]");
    expect(cta?.textContent).toBe("Choose a repository");
  });

  it("switching from one variant to the other replaces content", () => {
    showGitEmptyState("forges-needed");
    expect(variant()).toBe("forges-needed");
    showGitEmptyState("pick-or-clone");
    expect(variant()).toBe("pick-or-clone");
    expect(rootEl().querySelectorAll(".git-empty-state-card").length).toBe(1);
  });

  it("re-showing the same variant is idempotent and stays visible", () => {
    showGitEmptyState("forges-needed");
    showGitEmptyState("forges-needed");
    showGitEmptyState("forges-needed");
    expect(rootEl().classList.contains("hidden")).toBe(false);
    expect(rootEl().querySelectorAll(".git-empty-state-card").length).toBe(1);
  });

  it("hide clears the content and adds the hidden class", () => {
    showGitEmptyState("forges-needed");
    hideGitEmptyState();
    expect(rootEl().classList.contains("hidden")).toBe(true);
    expect(variant()).toBeNull();
  });

  it("forges-needed CTA invokes onConnectForge", () => {
    showGitEmptyState("forges-needed");
    rootEl().querySelector<HTMLButtonElement>("[data-empty-state-cta]")?.click();
    expect(onConnectForge).toHaveBeenCalledTimes(1);
    expect(onChooseRepo).not.toHaveBeenCalled();
  });

  it("pick-or-clone CTA invokes onChooseRepo", () => {
    showGitEmptyState("pick-or-clone");
    rootEl().querySelector<HTMLButtonElement>("[data-empty-state-cta]")?.click();
    expect(onChooseRepo).toHaveBeenCalledTimes(1);
    expect(onConnectForge).not.toHaveBeenCalled();
  });

  it("operations no-op gracefully when init was never called", () => {
    document.body.innerHTML = "";
    _resetGitEmptyState();
    expect(() => showGitEmptyState("forges-needed")).not.toThrow();
    expect(() => hideGitEmptyState()).not.toThrow();
  });
});
