// ---------------------------------------------------------------------------
// Shared helpers used by both the main file browser and the picker modal.
// ---------------------------------------------------------------------------

import { apiGet } from "./api-client.js";

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
 *  `onNavigate` is called with the cleaned path on Enter. `getDisplayPath`
 *  returns the display string to restore on Escape/blur. */
export function initEditablePath(
  input: HTMLInputElement,
  opts: {
    onNavigate: (path: string) => void;
    getDisplayPath: () => string;
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
      const raw = input.value.trim().replace(/^\/+/, "").replace(/\/+$/, "");
      const target = raw === "" ? "." : raw;
      input.readOnly = true;
      opts.onNavigate(target);
      input.blur();
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

export function joinPath(base: string, name: string): string {
  if (base === ".") {
    return name;
  }
  return `${base.replace(/\/+$/, "")}/${name}`;
}

export function parentPath(p: string): string {
  if (p === "." || p === "") {
    return ".";
  }
  const parts = p.split("/").filter((s) => s !== "");
  parts.pop();
  return parts.length === 0 ? "." : parts.join("/");
}

/** Convert a raw path (where "." means root) to a user-facing display string. */
export function displayPath(currentPath: string): string {
  return currentPath === "." ? "/" : `/${currentPath}`;
}

/** Build an error row element safely (no innerHTML with user content). */
export function errorRow(msg: string, onRetry?: () => void): HTMLDivElement {
  const row = document.createElement("div");
  row.className = FB_ROW;
  const span = document.createElement("span");
  span.className = FB_META;
  span.textContent = msg;
  row.appendChild(span);
  if (onRetry !== undefined) {
    const btn = document.createElement("button");
    btn.type = "button";
    btn.className = "btn-small";
    btn.textContent = "Retry";
    btn.addEventListener("click", onRetry);
    row.appendChild(btn);
  }
  return row;
}
