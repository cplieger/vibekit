// ---------------------------------------------------------------------------
// Shared helpers used by both the main file browser and the picker modal.
// ---------------------------------------------------------------------------

import { apiGet } from "./api-client.js";
import { el } from "@cplieger/reactive";

// --- CSS class constants for the file browser UI ---
export const FB_ROW = "fb-row";
export const FB_NAME = "fb-name";
export const FB_NAME_LINK = "fb-name-link";
export const FB_CHECK = "fb-check";
export const FB_META = "fb-meta";

export interface FileEntry {
  name: string;
  isDir: boolean;
  size: number;
  mode: string;
  modTime: number;
}

interface DirListing {
  files: FileEntry[];
  writable: boolean;
  error?: string;
}

/** Per-caller abort state for fetchDir. Each caller (browser, picker) must
 *  pass its own holder so they don't abort each other's requests. */
export interface FetchDirOpts {
  controllerHolder: { current: AbortController | null };
}

/** Fetch a directory listing from the server. Returns an empty listing
 *  with `error` set on failure. Stale requests are cancelled via
 *  AbortController scoped to the caller's controllerHolder. */
export async function fetchDir(path: string, opts: FetchDirOpts): Promise<DirListing> {
  const holder = opts.controllerHolder;
  holder.current?.abort();
  holder.current = new AbortController();
  const { signal } = holder.current;
  try {
    const d = await apiGet<{ files?: FileEntry[]; writable?: boolean; error?: string }>(
      `/api/files?path=${encodeURIComponent(path)}`,
      signal,
    );
    if (signal.aborted) {
      return { files: [], writable: false, error: "stale" };
    }
    if (d === null) {
      return { files: [], writable: false, error: "fetch failed" };
    }
    if (d.error !== undefined) {
      return { files: [], writable: false, error: d.error };
    }
    return { files: d.files ?? [], writable: d.writable ?? false };
  } catch {
    if (signal.aborted) {
      return { files: [], writable: false, error: "stale" };
    }
    return { files: [], writable: false, error: "fetch failed" };
  }
}

/** Sort directory entries: directories first, then alphabetical by name. */
export function sortEntries<T extends { name: string; isDir: boolean }>(entries: T[]): T[] {
  return [...entries].sort((a, b) => {
    if (a.isDir !== b.isDir) {
      return a.isDir ? -1 : 1;
    }
    return a.name.localeCompare(b.name);
  });
}

/** Wire an editable path input with click-to-edit, Enter/Escape/blur handling.
 *  `onNavigate` is called with the normalised path on Enter — this is one of the
 *  three doors `normalizeDirPath` exists for, since the text is whatever the user
 *  typed. `getCurrentPath` returns the path to restore on Escape/blur, which is
 *  the path itself: the browser's space IS the user-facing spelling now, so there
 *  is no display form to convert to. */
export function initEditablePath(
  input: HTMLInputElement,
  opts: {
    onNavigate: (path: string) => void;
    getCurrentPath: () => string;
  },
): void {
  input.addEventListener("click", () => {
    if (!input.readOnly) {
      return;
    }
    input.readOnly = false;
    input.select();
  });
  input.addEventListener("keydown", (e: KeyboardEvent) => {
    if (e.key === "Enter") {
      e.preventDefault();
      input.readOnly = true;
      opts.onNavigate(normalizeDirPath(input.value));
      input.blur();
    } else if (e.key === "Escape") {
      input.readOnly = true;
      input.value = opts.getCurrentPath();
      input.blur();
    }
  });
  input.addEventListener("blur", () => {
    input.readOnly = true;
    input.value = opts.getCurrentPath();
  });
}

export function formatSize(bytes: number): string {
  if (bytes < 1024) {
    return `${String(bytes)} B`;
  }
  if (bytes < 1024 * 1024) {
    return `${(bytes / 1024).toFixed(1)} KB`;
  }
  if (bytes < 1024 * 1024 * 1024) {
    return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
  }
  return `${(bytes / (1024 * 1024 * 1024)).toFixed(1)} GB`;
}

export function formatDate(ms: number): string {
  const d = new Date(ms);
  return (
    d.toLocaleDateString(undefined, { month: "short", day: "numeric", year: "numeric" }) +
    " " +
    d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })
  );
}

/** The browser's ROOT listing — the synthetic list of granted mounts, which
 *  `/api/files` answers for this exact path. It is not a real directory in the
 *  allow-list model, but its PATH is the one every child is composed from, so it
 *  belongs to the same space as the rest of them.
 *
 *  This used to be ".", and that one character was the whole of a year-long
 *  silent defect: `joinPath(".", "workspace")` returned "workspace", so every
 *  path the listing produced was rootless while the git-status index, the editor
 *  and `/api/files/search` all speak container-absolute. No key could match, so
 *  the status letters and the directory rollups were dead on every file. */
export const FB_ROOT = "/";

/** Join a listing's path with one entry NAME. The base is always in the
 *  container-absolute space (`FB_ROOT` or below), so the result is too. */
export function joinPath(base: string, name: string): string {
  return `${base.replace(/\/+$/, "")}/${name}`;
}

/** The listing one level up. Bottoms out at `FB_ROOT` rather than walking past
 *  it: above the mounts listing there is nothing browsable. */
export function parentPath(p: string): string {
  const parts = p.split("/").filter((s) => s !== "");
  parts.pop();
  return parts.length === 0 ? FB_ROOT : `/${parts.join("/")}`;
}

/** The ONE door into the browser's path space, for a path arriving from outside
 *  the module: the persisted `fb_path`, a `/files/<path>` deep link, or the text
 *  a user typed into the path input.
 *
 *  It exists so `currentPath` is absolute by construction rather than by every
 *  entry point remembering to make it so — and it is what lets a bookmark or a
 *  setting written by an older build resolve instead of quietly reviving the
 *  rootless space. "." is accepted for the same reason `/api/files` accepts it. */
export function normalizeDirPath(raw: string): string {
  const trimmed = raw.trim().replace(/^\/+/, "").replace(/\/+$/, "");
  return trimmed === "" || trimmed === "." ? FB_ROOT : `/${trimmed}`;
}

/** Expand a set of workspace-relative paths with every ancestor directory.
 *
 *  Including the ancestors is what lets ONE matching rule serve a file row and a
 *  folder row alike, without the browser needing to know where the workspace
 *  root is — which it genuinely does not: the listing's paths come from an
 *  allow-list of mounts, so hardcoding `/workspace` would be wrong the moment a
 *  second root is granted. */
export function withAncestors(rels: Iterable<string>): Set<string> {
  const out = new Set<string>();
  for (const rel of rels) {
    out.add(rel);
    let cut = rel.lastIndexOf("/");
    while (cut > 0) {
      const dir = rel.slice(0, cut);
      out.add(dir);
      cut = dir.lastIndexOf("/");
    }
  }
  return out;
}

/** Whether an absolute row path is in a set of workspace-relative paths.
 *
 *  A suffix rule on a `/`-delimited boundary, so `src/a.go` matches
 *  `/workspace/src/a.go` but never `/workspace/other-src/a.go`. The rule is
 *  load-bearing for the multi-mount case: a `/config/...` row has no
 *  workspace-relative form, so it must not match a workspace-relative set.
 *
 *  Generates the row's OWN suffixes and probes the set, rather than walking the
 *  set testing `endsWith`: the set is every path this chat touched plus every
 *  ancestor, which runs to hundreds after a long session, while a row's depth is
 *  about six. Called once per row per render pass, so the difference is
 *  O(rows x depth) against O(rows x |changed|). */
export function matchesRelative(absPath: string, rels: ReadonlySet<string>): boolean {
  if (rels.has(absPath)) {
    return true;
  }
  // Every `/`-boundary suffix of the row's path, shortest-first from each
  // separator. `cut + 1` skips the separator itself, which is what makes
  // "other-src/a.go" fail to match "src/a.go" — the only boundary offered is the
  // one after the `/`, never mid-segment.
  for (let cut = absPath.indexOf("/"); cut !== -1; cut = absPath.indexOf("/", cut + 1)) {
    if (rels.has(absPath.slice(cut + 1))) {
      return true;
    }
  }
  return false;
}

/** Build an error row element safely (no innerHTML with user content). */
export function errorRow(msg: string, onRetry?: () => void): HTMLDivElement {
  const row = el(
    "div",
    { className: FB_ROW },
    el("span", { className: FB_META }, msg),
  ) as HTMLDivElement;
  if (onRetry !== undefined) {
    const btn = el("button", { type: "button", className: "btn-small" }, "Retry");
    btn.addEventListener("click", onRetry);
    row.appendChild(btn);
  }
  return row;
}
