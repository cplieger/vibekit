// ---------------------------------------------------------------------------
// Shared helpers used by both the main file browser and the picker modal.
// ---------------------------------------------------------------------------

import { apiGet } from "./api-client.js";

export interface FileEntry {
  name: string;
  isDir: boolean;
  size: number;
  mode: string;
  modTime: number;
}

export interface DirListing {
  files: FileEntry[];
  writable: boolean;
  error?: string;
}

/** AbortController for fetchDir — aborted on each new call to cancel
 *  stale in-flight requests. */
let fetchDirController: AbortController | null = null;

/** Fetch a directory listing from the server. Returns an empty listing
 *  with `error` set on failure. Stale requests are cancelled via
 *  AbortController. */
export async function fetchDir(path: string): Promise<DirListing> {
  fetchDirController?.abort();
  fetchDirController = new AbortController();
  const { signal } = fetchDirController;
  try {
    const d = await apiGet<{ files?: FileEntry[]; writable?: boolean; error?: string }>(
      `/api/files?path=${encodeURIComponent(path)}`, signal,
    );
    if (signal.aborted) return { files: [], writable: false, error: "stale" };
    if (d === null) return { files: [], writable: false, error: "fetch failed" };
    if (d.error !== undefined) return { files: [], writable: false, error: d.error };
    return { files: d.files ?? [], writable: d.writable ?? false };
  } catch {
    if (signal.aborted) return { files: [], writable: false, error: "stale" };
    return { files: [], writable: false, error: "fetch failed" };
  }
}

/** Sort directory entries: directories first, then alphabetical by name. */
export function sortEntries<T extends { name: string; isDir: boolean }>(entries: T[]): T[] {
  return [...entries].sort((a, b) => {
    if (a.isDir !== b.isDir) return a.isDir ? -1 : 1;
    return a.name.localeCompare(b.name);
  });
}

/** URL safety predicate: blocks javascript:, vbscript:, data:, file: schemes.
 *  Strips internal whitespace before checking to prevent bypass via embedded
 *  tabs/newlines (e.g. "java\tscript:alert(1)"). */
export function isSafeUrl(url: string): boolean {
  const lower = url.trim().replace(/[\t\n\r\x00]/g, "").toLowerCase();
  return !(lower.startsWith("javascript:") || lower.startsWith("vbscript:") || lower.startsWith("data:") || lower.startsWith("file:"));
}

/** Wire an editable path input with click-to-edit, Enter/Escape/blur handling.
 *  `onNavigate` is called with the cleaned path on Enter. `getDisplayPath`
 *  returns the display string to restore on Escape/blur. */
export function initEditablePath(input: HTMLInputElement, opts: {
  onNavigate: (path: string) => void;
  getDisplayPath: () => string;
}): void {
  input.addEventListener("click", () => {
    if (!input.readOnly) return;
    input.readOnly = false;
    input.select();
  });
  input.addEventListener("keydown", (e: KeyboardEvent) => {
    if (e.key === "Enter") {
      e.preventDefault();
      const raw = input.value.trim().replace(/^\/+/, "").replace(/\/+$/, "");
      const target = raw === "" ? "." : raw;
      input.readOnly = true;
      input.blur();
      opts.onNavigate(target);
    } else if (e.key === "Escape") {
      input.readOnly = true;
      input.value = opts.getDisplayPath();
      input.blur();
    }
  });
  input.addEventListener("blur", () => {
    input.readOnly = true;
    input.value = opts.getDisplayPath();
  });
}

export function formatSize(bytes: number): string {
  if (bytes < 1024) return `${String(bytes)} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
  return `${(bytes / (1024 * 1024 * 1024)).toFixed(1)} GB`;
}

export function formatDate(ms: number): string {
  const d = new Date(ms);
  return d.toLocaleDateString(undefined, { month: "short", day: "numeric", year: "numeric" })
    + " " + d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
}

export function joinPath(base: string, name: string): string {
  if (base === ".") return name;
  return `${base.replace(/\/+$/, "")}/${name}`;
}

export function parentPath(p: string): string {
  if (p === "." || p === "") return ".";
  const parts = p.split("/").filter((s) => s !== "");
  parts.pop();
  return parts.length === 0 ? "." : parts.join("/");
}

/** Convert a raw path (where "." means root) to a user-facing display string. */
export function displayPath(currentPath: string): string {
  return currentPath === "." ? "/" : `/${currentPath}`;
}

/** Build an error row element safely (no innerHTML with user content). */
export function errorRow(msg: string): HTMLDivElement {
  const row = document.createElement("div");
  row.className = "fb-row";
  const span = document.createElement("span");
  span.className = "fb-meta";
  span.textContent = msg;
  row.appendChild(span);
  return row;
}

/** Human-friendly relative time string from a millisecond timestamp. */
export function relativeTime(ms: number): string {
  const seconds = (Date.now() - ms) / 1000;
  if (seconds < 60) return "just now";
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`;
  if (seconds < 30 * 86400) return `${Math.floor(seconds / 86400)}d ago`;
  if (seconds < 365 * 86400) return `${Math.floor(seconds / (30 * 86400))}mo ago`;
  return `${Math.floor(seconds / (365 * 86400))}y ago`;
}
