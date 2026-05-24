// Actions for the file browser: create, delete, rename, upload.
// ---------------------------------------------------------------------------

import { apiAction, defineAction, ActionError } from "./index.js";
import { joinPath } from "../files-shared.js";
import { uploadFiles } from "../upload.js";
import { withTimeout, API_TIMEOUT_MS } from "../api-client.js";

// --- files.create_file ---

export const createFile = apiAction<{ dir: string; name: string }, unknown>({
  name: "files.create_file",
  request: ({ dir, name }) => ({
    method: "POST",
    path: "/api/files/action",
    body: { action: "touch", path: joinPath(dir, name) },
  }),
  error: "Couldn't create file",
});

// --- files.create_folder ---

export const createFolder = apiAction<{ dir: string; name: string }, unknown>({
  name: "files.create_folder",
  request: ({ dir, name }) => ({
    method: "POST",
    path: "/api/files/action",
    body: { action: "mkdir", path: joinPath(dir, name) },
  }),
  error: "Couldn't create folder",
});

// --- files.rename ---

export const renameFile = apiAction<{ dir: string; original: string; newName: string }, unknown>({
  name: "files.rename",
  request: ({ dir, original, newName }) => ({
    method: "POST",
    path: "/api/files/action",
    body: { action: "rename", path: joinPath(dir, original), name: newName },
  }),
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
  // NOTE: optimistic/rollback threads a live DOM element (args.listEl) because
  // the file browser list is not backed by a reactive store — the only way to
  // mark rows as "exiting" is direct DOM manipulation. This is acceptable as
  // long as the action is only dispatched from the file browser UI.
  optimistic: (args) => {
    // Mark rows as exiting.
    for (const row of [...args.listEl.children]) {
      const el = row as HTMLDivElement;
      if (args.names.includes(el.dataset["name"] ?? "")) {
        el.classList.add("fb-row-exiting");
      }
    }
    return undefined;
  },
  rollback: (args) => {
    // Restore rows.
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
