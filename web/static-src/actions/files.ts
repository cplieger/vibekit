// Actions for the file browser: create, delete, rename, upload.
// ---------------------------------------------------------------------------

import { apiAction, defineAction, ActionError } from "./index.js";
import { joinPath } from "../files-shared.js";
import type { FileEntry } from "../files-shared.js";
import { uploadFiles } from "../upload.js";
import { withTimeout, API_TIMEOUT_MS } from "../api-client.js";

// --- Shared types for create actions ---

export interface CreateArgs {
  dir: string;
  name: string;
  /** DOM list element + state for optimistic insert. */
  listEl?: HTMLElement;
  entries?: FileEntry[];
  renderRow?: (entry: FileEntry) => HTMLDivElement;
}

// --- files.create_file ---

export const createFile = defineAction<CreateArgs, unknown>({
  name: "files.create_file",
  run: async (args, signal) => {
    const r = await fetch("/api/files/action", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ action: "touch", path: joinPath(args.dir, args.name) }),
      signal: withTimeout(signal, API_TIMEOUT_MS),
    });
    if (!r.ok) {
      let msg = `HTTP ${String(r.status)}`;
      try { const b = (await r.json()) as { error?: string }; if (b.error) msg = b.error; } catch { /* */ }
      throw new ActionError(msg, { status: r.status });
    }
    return undefined;
  },
  optimistic: (args) => insertPlaceholder(args, false),
  rollback: (args, op) => removePlaceholder(args, op as string | undefined),
  retryable: "network",
  error: "Couldn't create file",
});

// --- files.create_folder ---

export const createFolder = defineAction<CreateArgs, unknown>({
  name: "files.create_folder",
  run: async (args, signal) => {
    const r = await fetch("/api/files/action", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ action: "mkdir", path: joinPath(args.dir, args.name) }),
      signal: withTimeout(signal, API_TIMEOUT_MS),
    });
    if (!r.ok) {
      let msg = `HTTP ${String(r.status)}`;
      try { const b = (await r.json()) as { error?: string }; if (b.error) msg = b.error; } catch { /* */ }
      throw new ActionError(msg, { status: r.status });
    }
    return undefined;
  },
  optimistic: (args) => insertPlaceholder(args, true),
  rollback: (args, op) => removePlaceholder(args, op as string | undefined),
  retryable: "network",
  error: "Couldn't create folder",
});

/** Insert a placeholder FileEntry into state.entries and render a row. */
function insertPlaceholder(args: CreateArgs, isDir: boolean): string | undefined {
  if (!args.listEl || !args.entries || !args.renderRow) return undefined;
  const entry: FileEntry = {
    name: args.name,
    isDir,
    size: 0,
    mode: isDir ? "drwxr-xr-x" : "-rw-r--r--",
    modTime: Date.now(),
  };
  args.entries.push(entry);
  const row = args.renderRow(entry);
  row.dataset["newPlaceholder"] = "true";
  // Insert at sorted position in DOM (dirs first, then alpha).
  const children = [...args.listEl.children] as HTMLDivElement[];
  let inserted = false;
  for (const child of children) {
    const childName = child.dataset["name"];
    if (childName === undefined) continue; // skip ".." row
    const childIsDir = child.querySelector(".fb-icon")?.innerHTML.includes("folder") ?? false;
    if (isDir && !childIsDir) { args.listEl.insertBefore(row, child); inserted = true; break; }
    if (isDir === childIsDir && args.name.localeCompare(childName) < 0) {
      args.listEl.insertBefore(row, child); inserted = true; break;
    }
  }
  if (!inserted) args.listEl.appendChild(row);
  return args.name;
}

/** Remove the placeholder row + entry on rollback. */
function removePlaceholder(args: CreateArgs, name: string | undefined): void {
  if (!name || !args.listEl || !args.entries) return;
  const idx = args.entries.findIndex((e) => e.name === name);
  if (idx !== -1) args.entries.splice(idx, 1);
  const row = [...args.listEl.children].find(
    (el) => (el as HTMLDivElement).dataset["name"] === name && (el as HTMLDivElement).dataset["newPlaceholder"] === "true",
  );
  row?.remove();
}

// --- files.rename ---

export const renameFile = apiAction<{ dir: string; original: string; newName: string }, unknown>({
  name: "files.rename",
  request: ({ dir, original, newName }) => ({
    method: "POST",
    path: "/api/files/action",
    body: { action: "rename", path: joinPath(dir, original), name: newName },
  }),
  retryable: "network",
  error: "Couldn't rename",
});

// --- files.delete ---

export interface DeleteArgs {
  dir: string;
  names: string[];
  listEl: HTMLElement;
}

export const deleteFilesBatch = defineAction<DeleteArgs, void>({
  name: "files.delete",
  // CAUTION: retry re-attempts the entire batch. Acceptable for network
  // failures (server never received the request) but not for partial
  // server-side failures. "network" mode only retries on status 0 /
  // timeout, which implies the server didn't process anything.
  retryable: "network",
  run: async (args, signal) => {
    const timedSignal = withTimeout(signal, API_TIMEOUT_MS);
    const results = await Promise.all(
      args.names.map((name) => {
        const init: RequestInit = {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ action: "delete", path: joinPath(args.dir, name) }),
          signal: timedSignal,
        };
        return fetch("/api/files/action", init).then(async (r) => {
          if (!r.ok) {
            let serverError = "";
            try {
              const body = (await r.json()) as { error?: string };
              if (typeof body.error === "string") serverError = body.error;
            } catch { /* ignore */ }
            return { ok: false as const, name, error: serverError || `HTTP ${String(r.status)}`, status: r.status };
          }
          return { ok: true as const, name };
        });
      }),
    );
    const failed = results.filter((r) => !r.ok) as { ok: false; name: string; error: string; status: number }[];
    if (failed.length > 0) {
      const names = failed.map((f) => f.name).join(", ");
      const word = failed.length === 1 ? "Couldn't delete" : `Couldn't delete ${String(failed.length)} items`;
      throw new ActionError(`${word} (${names}): ${failed[0]!.error}`, { status: failed[0]!.status });
    }
  },
  optimistic: (args) => {
    for (const row of [...args.listEl.children]) {
      const el = row as HTMLDivElement;
      if (args.names.includes(el.dataset["name"] ?? "")) {
        el.classList.add("fb-row-exiting");
      }
    }
    return undefined;
  },
  rollback: (args) => {
    for (const row of [...args.listEl.children]) {
      (row as HTMLDivElement).classList.remove("fb-row-exiting");
    }
  },
  error: (_args, err) => err.message,
});

// --- files.upload ---

export interface UploadArgs {
  files: FileList;
  targetDir: string;
}

export const uploadAction = defineAction<UploadArgs, string[]>({
  name: "files.upload",
  retryable: "network",
  run: (args, signal) => {
    return new Promise<string[]>((resolve, reject) => {
      uploadFiles({
        files: args.files,
        targetDir: args.targetDir,
        signal,
        onComplete: (paths) => resolve(paths),
        onError: (msg) => reject(new ActionError(msg)),
      });
    });
  },
  error: "Upload failed",
});
