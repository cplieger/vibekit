// ---------------------------------------------------------------------------
// Editor: Conflict-mode rendering and AI merge suggestion handling.
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";
import { el } from "@cplieger/reactive";
import { join } from "@cplieger/keyenc";
import { reconcile } from "./reconcile.js";
import { sigChanged } from "./paint-sig.js";
import {
  parseConflicts,
  resolveHunk,
  type ConflictFile,
  type ConflictHunk,
  type Resolution,
} from "./conflict.js";
import { suggestResolution } from "./actions/editor.js";
import type { FileState } from "./editor-types.js";
import { getActiveFilePath, fileStates } from "./editor-types.js";
import { updateGutter, renderEditModeUI, showEditMode } from "./editor-ui.js";
import { registerCleanup } from "./actions/index.js";

registerCleanup(() => {
  suggestResolution.cancel();
});

/** Per-file generation counter so superseded dispatches can detect
 *  they were cancelled WITHOUT invalidating other files' in-flight
 *  suggestions. Closing file A's tab bumps only A's counter; file B's
 *  in-flight suggestion uses B's counter and resolves correctly. */
const suggestionGenByPath = new Map<string, number>();

function bumpSuggestionGen(path: string): number {
  const next = (suggestionGenByPath.get(path) ?? 0) + 1;
  suggestionGenByPath.set(path, next);
  return next;
}

function currentSuggestionGen(path: string): number {
  return suggestionGenByPath.get(path) ?? 0;
}

/** Clean up per-file generation tracking when a file is closed. Called
 *  from closeEditorFile so the suggestionGenByPath Map doesn't grow
 *  unbounded over a long session with many files opened/closed. */
export function clearSuggestionState(path: string): void {
  suggestionGenByPath.delete(path);
}

/** Abort any in-flight suggestion request. If path is provided, reset
 *  loading state on THAT file; otherwise default to the active file.
 *  Called on tab close (with path) and on broader teardown (without).
 *
 *  Per-file generation: bumps only the target path's counter so other
 *  files' in-flight suggestions remain valid. */
export function abortSuggestion(path?: string): void {
  // Reset any entries with loading: true so the UI doesn't show stale spinners.
  const targetPath = path ?? getActiveFilePath();
  const state = fileStates.get(targetPath);
  if (state !== undefined) {
    for (const [key, entry] of state.suggestions) {
      if (entry.loading) {
        state.suggestions.set(key, { loading: false, preview: null, error: "cancelled" });
      }
    }
  }
  // Bump only this path's counter so this file's in-flight requests
  // discard their results on resolution.
  bumpSuggestionGen(targetPath);
}

export function renderConflictModeUI(state: FileState): void {
  if (state.mode.value.kind !== "conflict") {
    state.mode.value = {
      kind: "conflict",
      conflict: parseConflicts(state.current.value),
      editing: true,
    };
  }
  $.editorContent.value = state.current.value;
  showEditMode();
  updateGutter(state.current.value);
  $.editorEditBtn.classList.add("hidden");
  $.editorCancelBtn.classList.remove("hidden");
  $.editorSaveBtn.classList.remove("hidden");
  $.editorDiffBtn.classList.add("hidden");
  renderConflictOverlay(state);
}

/** One element of the overlay: the live status line, one hunk's control row, or the
 *  `<pre>` preview that follows a hunk carrying a suggestion. */
type OverlayEntry =
  | { readonly kind: "status"; readonly text: string }
  | {
      readonly kind: "hunk";
      readonly index: number;
      readonly hunk: ConflictHunk;
      readonly loading: boolean;
      readonly preview: string | null;
      readonly error: string;
    }
  | { readonly kind: "preview"; readonly line: number; readonly text: string };

function overlayEntries(conflict: ConflictFile, state: FileState): OverlayEntry[] {
  const n = conflict.hunks.length;
  const out: OverlayEntry[] = [
    { kind: "status", text: `${String(n)} unresolved conflict${n === 1 ? "" : "s"}` },
  ];
  for (let i = 0; i < n; i++) {
    const hunk = conflict.hunks[i]!; // eslint-disable-line @typescript-eslint/no-non-null-assertion
    const s = state.suggestions.get(hunk.startLine);
    out.push({
      kind: "hunk",
      index: i,
      hunk,
      loading: s?.loading === true,
      preview: s?.preview ?? null,
      error: s?.error ?? "",
    });
    if (s !== undefined && s.preview !== null) {
      out.push({ kind: "preview", line: hunk.startLine, text: s.preview });
    }
  }
  return out;
}

/** Repaint the per-hunk control overlay.
 *
 *  Keyed on a stable identity with the content repainted in `update`, never on the
 *  rendered state: this re-runs for state belonging to ONE hunk, every row is a set
 *  of real buttons, and the status line is an `aria-live` region a replacement is not
 *  reliably re-announced from. Why the key may not carry content: `web.md` "A KEYED
 *  RECONCILE IS NOT ENOUGH ON ITS OWN". */
export function renderConflictOverlay(state: FileState): void {
  const overlay = $.editorConflictOverlay;
  if (state.mode.value.kind !== "conflict" || state.mode.value.conflict.hunks.length === 0) {
    overlay.replaceChildren();
    overlay.classList.add("hidden");
    return;
  }
  const conflict = state.mode.value.conflict;
  overlay.classList.remove("hidden");
  reconcile(overlay, overlayEntries(conflict, state), {
    key: (e: OverlayEntry) => entryKey(e),
    mount: (e: OverlayEntry) => mountEntry(e, state),
    update: (node: HTMLElement, e: OverlayEntry) => {
      updateEntry(node, e, state);
    },
  });
}

function entryKey(e: OverlayEntry): string {
  switch (e.kind) {
    // STABLE across every count, so the live region survives and re-announces.
    case "status":
      return "status";
    // The hunk's POSITION IN THE FILE, which is what makes it the same hunk. Its
    // rendered state is deliberately not in here — see the re-seat note above.
    case "hunk":
      return join("hunk", String(e.hunk.startLine));
    case "preview":
      return join("preview", String(e.line));
  }
}

/** Repaint a kept element. Guarded per element, so a repaint that changes nothing
 *  touches nothing: the hunk rows are the ones that matter, since replacing a row's
 *  children is what takes `:hover` and the keyboard's place off its buttons. */
function updateEntry(node: HTMLElement, e: OverlayEntry, state: FileState): void {
  switch (e.kind) {
    case "status":
      node.textContent = e.text;
      return;
    case "preview":
      if (node.textContent !== e.text) {
        node.textContent = e.text;
      }
      return;
    case "hunk": {
      if (!sigChanged(node, hunkSignature(e))) {
        return;
      }
      const fresh = mountHunkRow(e, state);
      node.setAttribute("aria-label", fresh.getAttribute("aria-label") ?? "");
      node.replaceChildren(...Array.from(fresh.childNodes));
      return;
    }
  }
}

function mountEntry(e: OverlayEntry, state: FileState): HTMLElement {
  switch (e.kind) {
    case "status":
      return el(
        "div",
        {
          className: "conflict-status",
          // Live region: this repaints after each resolution, so the changed count
          // ("2 unresolved conflicts" → "1 …" → mode drops to edit) is announced to
          // screen readers.
          role: "status",
          "aria-live": "polite",
          "aria-atomic": "true",
        },
        e.text,
      );
    case "preview":
      return el("pre", { className: "conflict-suggest-preview" }, e.text);
    case "hunk": {
      const row = mountHunkRow(e, state);
      // Record it here, or the FIRST update repaints a row that has not moved.
      sigChanged(row, hunkSignature(e));
      return row;
    }
  }
}

/** The signature of everything a hunk row renders BELOW its identity. Read at mount
 *  and at every update from one place, so a first repaint cannot be spent rebuilding a
 *  row that has not moved. */
function hunkSignature(e: Extract<OverlayEntry, { kind: "hunk" }>): string[] {
  return [e.hunk.ourLabel, e.hunk.theirLabel, e.loading ? "1" : "", e.preview ?? "", e.error];
}

function mountHunkRow(e: Extract<OverlayEntry, { kind: "hunk" }>, state: FileState): HTMLElement {
  const { hunk, index: i, loading, preview, error } = e;
  const lineNo = String(hunk.startLine + 1);
  const ours = hunk.ourLabel || "HEAD";
  const theirs = hunk.theirLabel || "incoming";
  // role=group + aria-label so a screen reader announces the per-hunk button
  // set with its line + side context (the visible title serves sighted users).
  const row = el("div", {
    className: "conflict-hunk-row",
    role: "group",
    "aria-label": `Conflict at line ${lineNo}: ${ours} vs ${theirs}`,
  });
  row.appendChild(
    el("span", { className: "conflict-hunk-title" }, `Line ${lineNo}: ${ours} vs ${theirs}`),
  );
  // A hunk carrying a suggestion offers Accept/Reject INSTEAD of the three side
  // choices: the preview is what the reader is deciding about, and its `<pre>` is a
  // separate entry immediately after this row rather than a child of it.
  if (preview !== null) {
    row.appendChild(el("span", { className: "conflict-suggest-pill" }, "AI suggestion"));
    row.appendChild(
      resolveBtn(
        "Accept",
        () => {
          acceptSuggestion(state, i);
        },
        `Accept the suggested merge for the conflict at line ${lineNo}`,
      ),
    );
    row.appendChild(
      resolveBtn(
        "Reject",
        () => {
          rejectSuggestion(state, hunk.startLine);
        },
        `Reject the suggested merge for the conflict at line ${lineNo}`,
      ),
    );
    return row;
  }
  row.appendChild(
    resolveBtn(
      "Ours",
      () => {
        applyResolution(state, i, "ours");
      },
      `Accept ours (${ours}) for the conflict at line ${lineNo}`,
    ),
  );
  row.appendChild(
    resolveBtn(
      "Theirs",
      () => {
        applyResolution(state, i, "theirs");
      },
      `Accept theirs (${theirs}) for the conflict at line ${lineNo}`,
    ),
  );
  row.appendChild(
    resolveBtn(
      "Both",
      () => {
        applyResolution(state, i, "both");
      },
      `Keep both sides for the conflict at line ${lineNo}`,
    ),
  );
  const suggestBtn = el(
    "button",
    {
      className: "btn-small conflict-btn conflict-btn-suggest",
      "data-tooltip": "Propose a merged version using the utility AI bridge",
      "aria-label": `Suggest a merged resolution for the conflict at line ${lineNo}`,
      disabled: loading,
      // BUSY, not unavailable: this button disables itself for the length of
      // its own request and its label becomes "Suggesting…". Without the
      // attribute it took the refusal face, dimming that very label. Declared
      // rather than set through `setControlBusy` because the row's key carries this
      // state, so a change of it remounts the row.
      ...(loading ? { "aria-busy": "true" } : {}),
    },
    loading ? "Suggesting..." : "Suggest",
  );
  suggestBtn.addEventListener("click", () => {
    void requestSuggestion(state, i);
  });
  row.appendChild(suggestBtn);
  if (error !== "") {
    row.appendChild(el("span", { className: "conflict-suggest-error" }, error));
  }
  return row;
}

function resolveBtn(label: string, onClick: () => void, ariaLabel?: string): HTMLElement {
  const b = el("button", { className: "btn-small conflict-btn" }, label);
  if (ariaLabel !== undefined) {
    b.setAttribute("aria-label", ariaLabel);
  }
  b.addEventListener("click", onClick);
  return b;
}

function applyResolution(state: FileState, hunkIndex: number, resolution: Resolution): void {
  if (state.mode.value.kind !== "conflict") {
    return;
  }
  const newContent = resolveHunk(state.mode.value.conflict, hunkIndex, resolution);
  state.current.value = newContent;
  const parsed = parseConflicts(newContent);
  state.suggestions.clear();
  $.editorContent.value = newContent;
  updateGutter(newContent);
  if (parsed.hunks.length === 0) {
    state.mode.value = { kind: "edit", editing: false };
    renderEditModeUI(state);
    return;
  }
  state.mode.value = { kind: "conflict", conflict: parsed, editing: true };
  renderConflictOverlay(state);
}

async function requestSuggestion(state: FileState, hunkIndex: number): Promise<void> {
  if (state.mode.value.kind !== "conflict") {
    return;
  }
  const hunk = state.mode.value.conflict.hunks[hunkIndex];
  if (hunk === undefined) {
    return;
  }
  const existing = state.suggestions.get(hunk.startLine);
  if (
    existing?.loading === true ||
    (existing?.preview !== undefined && existing.preview !== null)
  ) {
    return;
  }
  // Per-file generation: bump only THIS file's counter. Other files'
  // in-flight suggestions remain valid.
  // Note: we no longer call suggestResolution.cancel() globally — that
  // would abort in-flight suggestions for OTHER files. The per-file
  // gen check below discards stale results from this file only.
  const myDispatchId = bumpSuggestionGen(state.path);
  state.suggestions.set(hunk.startLine, { loading: true, preview: null, error: "" });
  renderConflictOverlay(state);
  const context = buildHunkContext(state.mode.value.conflict, hunk);
  const body = {
    ours: hunk.oursLines.join("\n"),
    theirs: hunk.theirsLines.join("\n"),
    context,
  };
  const resp = await suggestResolution.dispatch(body);
  // Stale-dispatch guard: if abortSuggestion(state.path) or another
  // requestSuggestion on this file ran while we were awaiting, the
  // path's gen has incremented. Bail silently.
  if (myDispatchId !== currentSuggestionGen(state.path)) {
    return;
  }
  // eslint-disable-next-line @typescript-eslint/no-unnecessary-condition -- defensive check
  if (state.mode.value.kind !== "conflict") {
    return;
  }
  if (getActiveFilePath() !== state.path) {
    return;
  } // stale file switch
  const current = state.mode.value.conflict.hunks[hunkIndex];
  if (current?.startLine !== hunk.startLine) {
    return;
  }
  if (resp === null || typeof resp.output !== "string") {
    state.suggestions.set(hunk.startLine, {
      loading: false,
      preview: null,
      error: resp?.error ?? "generation failed",
    });
    renderConflictOverlay(state);
    return;
  }
  state.suggestions.set(hunk.startLine, { loading: false, preview: resp.output, error: "" });
  renderConflictOverlay(state);
}

function acceptSuggestion(state: FileState, hunkIndex: number): void {
  if (state.mode.value.kind !== "conflict") {
    return;
  }
  const hunk = state.mode.value.conflict.hunks[hunkIndex];
  if (hunk === undefined) {
    return;
  }
  const suggestion = state.suggestions.get(hunk.startLine);
  if (suggestion?.preview == null) {
    return;
  }
  const previewLines = suggestion.preview === "" ? [] : suggestion.preview.split("\n");
  const out = [
    ...state.mode.value.conflict.lines.slice(0, hunk.startLine),
    ...previewLines,
    ...state.mode.value.conflict.lines.slice(hunk.endLine + 1),
  ];
  const newContent = out.join("\n") + (state.mode.value.conflict.trailingNewline ? "\n" : "");
  state.current.value = newContent;
  const parsed = parseConflicts(newContent);
  state.suggestions.clear();
  $.editorContent.value = newContent;
  updateGutter(newContent);
  if (parsed.hunks.length === 0) {
    state.mode.value = { kind: "edit", editing: false };
    renderEditModeUI(state);
    return;
  }
  state.mode.value = { kind: "conflict", conflict: parsed, editing: true };
  renderConflictOverlay(state);
}

function rejectSuggestion(state: FileState, startLine: number): void {
  state.suggestions.delete(startLine);
  renderConflictOverlay(state);
}

function buildHunkContext(file: ConflictFile, hunk: ConflictHunk): string {
  const ctxLines = 10;
  const before = file.lines.slice(Math.max(0, hunk.startLine - ctxLines), hunk.startLine);
  const after = file.lines.slice(
    hunk.endLine + 1,
    Math.min(file.lines.length, hunk.endLine + 1 + ctxLines),
  );
  if (before.length === 0 && after.length === 0) {
    return "";
  }
  return [...before, "/* ...conflict hunk... */", ...after].join("\n");
}
