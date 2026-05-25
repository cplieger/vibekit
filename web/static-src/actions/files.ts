// Actions for the file browser: create, delete, rename, upload.
// ---------------------------------------------------------------------------

import { apiAction, defineAction, ActionError } from "./index.js";
import { joinPath } from "../files-shared.js";
import { uploadFiles } from "../upload.js";
import { withTimeout, API_TIMEOUT_MS } from "../api-client.js";

// --- Shared types for create actions ---

export interface CreateArgs {
  dir: string;
  name: string;
}

// --- files.create_file ---

export const createFile = defineAction<CreateArgs, unknown>({
  name: "files.create_file",
  scope: (args) => "dir:" + args.dir,
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
  retryable: "network",
  error: "Couldn't create file",
});

// --- files.create_folder ---

export const createFolder = defineAction<CreateArgs, unknown>({
  name: "files.create_folder",
  scope: (args) => "dir:" + args.dir,
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
  retryable: "network",
  error: "Couldn't create folder",
});

// --- files.rename ---

export const renameFile = apiAction<{ dir: string; original: string; newName: string }, unknown>({
  name: "files.rename",
  scope: (args) => "file:" + args.dir + "/" + args.original,
  idempotencyKey: true,
  request: ({ dir, original, newName }) => ({
    method: "POST",
    path: "/api/files/action",
    body: { action: "rename", path: joinPath(dir, original), name: newName },
  }),
  retryable: "network",
  retry: { count: 2, delay: 300 },
  error: (args) => `Couldn't rename \u201c${args.original.length > 40 ? args.original.slice(0, 37) + "\u2026" : args.original}\u201d`,
});

// --- files.delete ---

export interface DeleteArgs {
  dir: string;
  names: string[];
  listEl: HTMLElement;
}

export const deleteFilesBatch = defineAction<DeleteArgs, void>({
  name: "files.delete",
  scope: (args) => "dir:" + args.dir,
  // Batch delete must NOT retry: a timeout/network error may mean some
  // items were already deleted server-side. Retrying would re-attempt
  // those deletions, causing 404s or deleting newly-created files with
  // the same name. Caller handles partial failure via loadDir() refresh.
  retryable: false,
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
    // NOTE: This rollback is a no-op if loadDir() has already replaced
    // the list's children (the exiting rows no longer exist in the DOM).
    // That's fine — the fresh listing from the server is the source of truth.
    for (const row of [...args.listEl.children]) {
      (row as HTMLDivElement).classList.remove("fb-row-exiting");
    }
  },
  error: (_args, err) => err.message,
});

// --- files.download ---

export const downloadFiles = defineAction<{ paths: string[] }, void>({
  name: "files.download",
  retryable: false, // zip downloads aren't great for retry semantics
  run: async (args, signal) => {
    const r = await fetch("/api/files/download", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ paths: args.paths }),
      signal: withTimeout(signal, 60_000),
    });
    if (!r.ok) throw new ActionError("Download failed", { status: r.status });
    const blob = await r.blob();
    if (signal.aborted) return;
    // Trigger browser download via objectURL anchor
    const url = URL.createObjectURL(blob);
    try {
      if (signal.aborted) return;
      const a = document.createElement("a");
      a.href = url;
      a.download = "download.zip";
      document.body.appendChild(a);
      a.click();
      a.remove();
    } finally {
      URL.revokeObjectURL(url);
    }
  },
  error: "Download failed",
});

// --- files.upload ---

export interface UploadArgs {
  files: FileList;
  targetDir: string;
}

export const uploadAction = defineAction<UploadArgs, string[]>({
  name: "files.upload",
  scope: "upload",
  // retryable intentionally omitted: XHR-based multipart upload cannot safely
  // retry after partial byte transmission without data-resend implications.
  // scope: "upload" serializes concurrent dispatches through the framework
  // queue, preventing the race where two rapid drops both-reject or both-pass.
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
