// The conflict overlay's repaint. Assertions are on element IDENTITY: content cannot
// tell a kept row from a rebuilt one. The overlay's CSS side is
// `conflict-actions-css.test.ts`, which builds its rows by hand because this function
// reaches the `$` registry and a `FileState`.

import { describe, it, expect, vi, beforeEach } from "vitest";
import { signal, computed } from "@cplieger/reactive";
import type { FileState } from "./editor-types.js";

// The `$` registry throws on a missing element, and this one is read on every call.
const overlay = document.createElement("div");
overlay.id = "editor-conflict-overlay";
document.body.appendChild(overlay);

vi.mock("./actions/index.js", () => ({ registerCleanup: vi.fn() }));
// Replaced rather than spied so no request is ever composed: the suggestion STATE is
// what this file drives, and it drives it by writing the map the render reads.
vi.mock("./actions/editor.js", () => ({
  suggestResolution: { cancel: vi.fn(), dispatch: vi.fn(async () => null) },
}));
vi.mock("./editor-ui.js", () => ({
  updateGutter: vi.fn(),
  renderEditModeUI: vi.fn(),
  showEditMode: vi.fn(),
}));

const { renderConflictOverlay } = await import("./editor-conflict.js");
const { parseConflicts } = await import("./conflict.js");

/** A file with `n` conflicts, as git leaves it. */
function conflicted(n: number): string {
  const parts: string[] = [];
  for (let i = 0; i < n; i++) {
    parts.push(
      `line ${String(i)} before`,
      "<<<<<<< HEAD",
      `ours ${String(i)}`,
      "=======",
      `theirs ${String(i)}`,
      ">>>>>>> incoming",
    );
  }
  return parts.join("\n");
}

/** The two fields `renderConflictOverlay` reads, in a shape that satisfies the type.
 *  Every other field is a signal the render never touches, so they are declared
 *  rather than faked — a fake that lied about one of them would be a worse test than
 *  none. */
function fileState(text: string): FileState {
  const current = signal(text);
  const original = signal(text);
  return {
    path: "a.go",
    original,
    current,
    loaded: true,
    loadedHash: "",
    error: signal(""),
    mode: signal({ kind: "conflict", conflict: parseConflicts(text), editing: true }),
    dirty: computed(() => current.value !== original.value),
    suggestions: new Map(),
    returnToGitDiff: null,
    repo: "",
    cachedDiff: computed(() => []),
  } as unknown as FileState;
}

function status(): HTMLElement {
  const el = overlay.querySelector<HTMLElement>(".conflict-status");
  if (el === null) {
    throw new Error("no .conflict-status");
  }
  return el;
}

function hunkRows(): HTMLElement[] {
  return [...overlay.querySelectorAll<HTMLElement>(".conflict-hunk-row")];
}

function buttonLabels(row: Element): string[] {
  return [...row.querySelectorAll("button")].map((b) => b.textContent ?? "");
}

/** Element-by-element IDENTITY. `toEqual` over two arrays of DOM nodes compares them
 *  STRUCTURALLY, so it passes for a rebuilt row holding the same markup — the exact
 *  thing every case here exists to detect. */
function sameElements(after: readonly Element[], before: readonly Element[]): void {
  expect(after).toHaveLength(before.length);
  for (const [i, el] of before.entries()) {
    expect(after[i], `element ${String(i)} was replaced`).toBe(el);
  }
}

beforeEach(() => {
  // RE-ATTACHED, not merely emptied: Browser Mode clears the page body between tests,
  // so a module-scope host is connected for the first case and detached for every
  // later one — and `focus()` is a no-op on a detached tree, which would make the
  // focus case below pass for the wrong reason from the second case onward.
  if (!overlay.isConnected) {
    document.body.appendChild(overlay);
  }
  overlay.replaceChildren();
  overlay.classList.remove("hidden");
});

describe("the overlay's shape", () => {
  it("names the count and offers the three side choices per hunk", () => {
    renderConflictOverlay(fileState(conflicted(2)));

    expect(status().textContent).toBe("2 unresolved conflicts");
    expect(status().getAttribute("aria-live")).toBe("polite");
    const rows = hunkRows();
    expect(rows).toHaveLength(2);
    expect(buttonLabels(rows[0] as Element)).toEqual(["Ours", "Theirs", "Both", "Suggest"]);
    expect(rows[0]?.getAttribute("aria-label")).toContain("Conflict at line 2");
  });

  it("empties itself and hides when there is nothing left to resolve", () => {
    const st = fileState(conflicted(1));
    renderConflictOverlay(st);
    expect(overlay.children.length).toBeGreaterThan(0);

    st.mode.value = { kind: "edit", editing: false };
    renderConflictOverlay(st);

    expect(overlay.children.length).toBe(0);
    expect(overlay.classList.contains("hidden")).toBe(true);
  });

  // A hunk carrying a suggestion offers Accept/Reject INSTEAD of the side choices,
  // and its preview is a sibling `<pre>` rather than a child of the row.
  it("swaps a suggested hunk's actions and puts its preview after the row", () => {
    const st = fileState(conflicted(1));
    const line = st.mode.value.kind === "conflict" ? st.mode.value.conflict.hunks[0]!.startLine : 0;
    st.suggestions.set(line, { loading: false, preview: "merged text", error: "" });

    renderConflictOverlay(st);

    const rows = hunkRows();
    expect(buttonLabels(rows[0] as Element)).toEqual(["Accept", "Reject"]);
    expect(rows[0]?.nextElementSibling?.className).toBe("conflict-suggest-preview");
    expect(rows[0]?.nextElementSibling?.textContent).toBe("merged text");
  });
});

describe("a repaint keeps the hunks it did not change", () => {
  it("keeps the live region, so the count change is announced rather than replaced", () => {
    const st = fileState(conflicted(3));
    renderConflictOverlay(st);
    const region = status();

    // One resolved: a new parse, a new count, the same region element.
    st.mode.value = { kind: "conflict", conflict: parseConflicts(conflicted(2)), editing: true };
    renderConflictOverlay(st);

    expect(status()).toBe(region);
    expect(region.textContent).toBe("2 unresolved conflicts");
  });

  it("keeps an untouched hunk's row when a SIBLING gains a suggestion", () => {
    const st = fileState(conflicted(3));
    renderConflictOverlay(st);
    const before = hunkRows();
    expect(before).toHaveLength(3);
    const second = st.mode.value.kind === "conflict" ? st.mode.value.conflict.hunks[1]! : null;
    expect(second).not.toBeNull();

    st.suggestions.set(second!.startLine, { loading: true, preview: null, error: "" });
    renderConflictOverlay(st);

    // EVERY row survives, the changed one included: its key is its position in the
    // file, so it is repainted in place rather than replaced. Replacing it would
    // re-seat the row BEFORE it (see renderConflictOverlay's note) and take that
    // row's focus with it.
    sameElements(hunkRows(), before);
    expect(before[1]?.querySelector("button:last-of-type")?.textContent).toBe("Suggesting...");
    // The untouched neighbours kept their side choices.
    expect(buttonLabels(before[0] as Element)).toEqual(["Ours", "Theirs", "Both", "Suggest"]);
  });

  it("keeps every row across a repaint with nothing changed at all", () => {
    const st = fileState(conflicted(3));
    renderConflictOverlay(st);
    const before = hunkRows();

    renderConflictOverlay(st);

    sameElements(hunkRows(), before);
  });

  it("keeps focus on a button whose own row was not touched", () => {
    const st = fileState(conflicted(2));
    renderConflictOverlay(st);
    const ours = hunkRows()[0]?.querySelector<HTMLButtonElement>("button");
    ours?.focus();
    expect(document.activeElement).toBe(ours);

    const second = st.mode.value.kind === "conflict" ? st.mode.value.conflict.hunks[1]! : null;
    st.suggestions.set(second!.startLine, { loading: true, preview: null, error: "" });
    renderConflictOverlay(st);

    expect(document.activeElement).toBe(ours);
  });

  // The one state a `<pre>` carries is the reader's own text SELECTION, so its key is
  // its content: an unchanged preview keeps its element.
  it("keeps a preview whose text did not change", () => {
    const st = fileState(conflicted(2));
    const first = st.mode.value.kind === "conflict" ? st.mode.value.conflict.hunks[0]! : null;
    st.suggestions.set(first!.startLine, { loading: false, preview: "merged", error: "" });
    renderConflictOverlay(st);
    const pre = overlay.querySelector(".conflict-suggest-preview");
    expect(pre).not.toBeNull();

    renderConflictOverlay(st);

    expect(overlay.querySelector(".conflict-suggest-preview")).toBe(pre);
  });
});
