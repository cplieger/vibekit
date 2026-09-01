// Actions for the file browser: create, delete, rename, upload.
// ---------------------------------------------------------------------------

import {
  apiAction,
  defineAction,
  ActionError,
  classifyFetchError,
  hasErrorString,
  retryNetwork,
  RETRY_STANDARD,
  withTimeout,
  API_TIMEOUT_MS,
} from "./index.js";

import { joinPath } from "../files-shared.js";
import { uploadFiles } from "../upload.js";
// Aliased: `joinPath` above joins PATH segments, `joinKey` joins KEY
// components. Every idempotency/dedupe key in this file is built with the
// latter so no field's content can forge a component boundary.
//
// These keys leave the client as an `Idempotency-Key` HTTP header, which
// vibekit's Go middleware (internal/server/idempotency.go) treats as an
// OPAQUE string — it never parses or builds one — so byte parity with a
// Go-side key is not required here, only consistency within this client.
import { join as joinKey } from "@cplieger/keyenc";

const API_FILES_ACTION = "/api/files/action";
const API_FILES_DOWNLOAD = "/api/files/download";
/** 2× standard timeout for large workspace archive downloads. */
const DOWNLOAD_TIMEOUT_MS = 60_000;
import { truncate } from "../strings.js";

// --- Shared types for create actions ---

interface CreateArgs {
  dir: string;
  name: string;
}

// --- files.create_file ---

export const createFile = apiAction<CreateArgs>({
  name: "files.create_file",
  scope: (args) => "dir:" + args.dir,
  retry: RETRY_STANDARD,
  retryable: retryNetwork,
  idempotencyKey: (args) => joinKey("files.create", args.dir, args.name),
  request: (args) => ({
    method: "POST",
    path: API_FILES_ACTION,
    body: { action: "touch", path: joinPath(args.dir, args.name) },
  }),
  error: "Couldn't create file",
});

// --- files.create_folder ---

export const createFolder = apiAction<CreateArgs>({
  name: "files.create_folder",
  scope: (args) => "dir:" + args.dir,
  retry: RETRY_STANDARD,
  retryable: retryNetwork,
  idempotencyKey: (args) => joinKey("files.create_folder", args.dir, args.name),
  request: (args) => ({
    method: "POST",
    path: API_FILES_ACTION,
    body: { action: "mkdir", path: joinPath(args.dir, args.name) },
  }),
  error: "Couldn't create folder",
});

// --- files.rename ---

export const renameFile = apiAction<{ dir: string; original: string; newName: string }>({
  name: "files.rename",
  scope: (args) => "file:" + args.dir + "/" + args.original,
// The one key in this file that was reachably BROKEN: the old form was
// `files.rename:${dir}/${original}->${newName}`, and "->" is a legal
// filename sequence — renaming "a" to "b->c" and "a->b" to "c" in the same
// directory both produced the same key, so the second rename silently
// replayed the first's cached 200 for the idempotency TTL. Joining the
// three fields as components removes the ambiguity.
  idempotencyKey: (args) => joinKey("files.rename", args.dir, args.original, args.newName),
  request: ({ dir, original, newName }) => ({
    method: "POST",
    path: API_FILES_ACTION,
    body: { action: "rename", path: joinPath(dir, original), name: newName },
  }),
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  error: (args) => `Couldn't rename \u201c${truncate(args.original)}\u201d`,
});

// --- files.delete ---

interface DeleteArgs {
  dir: string;
  names: string[];
  listEl: HTMLElement;
}

// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for action with no args/result
export const deleteFilesBatch = defineAction<DeleteArgs, void>({
  name: "files.delete",
  scope: (args) => "dir:" + args.dir,
  // Nested join: the sorted filename LIST gets its own `joinKey`, becoming
  // one component of the outer key — a comma-joined list could not
  // distinguish ["a,b"] from ["a","b"].
  dedupe: (args) => joinKey("files.delete", joinKey(...args.names.slice().sort())),
  // Batch delete must NOT retry: a timeout/network error may mean some
  // items were already deleted server-side. `listEl` (non-serializable) is
  // safe here since retry is off and dedupe reads only `names`.
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
        return fetch(API_FILES_ACTION, init).then(
          async (r) => {
            if (!r.ok) {
              let serverError = "";
              try {
                const body: unknown = await r.json();
                if (hasErrorString(body)) {
                  serverError = body.error;
                }
              } catch {
                /* ignore */
              }
              return {
                ok: false as const,
                name,
                error: serverError || `HTTP ${String(r.status)}`,
                status: r.status,
              };
            }
            return { ok: true as const, name };
          },
          (e: unknown) => {
            if (signal.aborted) {
              return { ok: false as const, name, error: "cancelled", status: 0 };
            }
            if (e instanceof DOMException) {
              return { ok: false as const, name, error: "Request timed out", status: 0 };
            }
            return { ok: false as const, name, error: "network error", status: 0 };
          },
        );
      }),
    );
    const failed = results.filter(
      (r): r is { ok: false; name: string; error: string; status: number } => !r.ok,
    );
    if (failed.length > 0) {
      // If all failures are network/timeout/cancelled, classify the aggregate error
      const allNetwork = failed.every((f) => f.status === 0);
      // eslint-disable-next-line @typescript-eslint/no-non-null-assertion -- guarded by failed.length > 0
      const firstErr = failed[0]!.error;
      if (signal.aborted) {
        throw new ActionError("cancelled", { code: "cancelled" });
      }
      const names = failed.map((f) => f.name).join(", ");
      const word =
        failed.length === 1 ? "Couldn't delete" : `Couldn't delete ${String(failed.length)} items`;
      throw new ActionError(`${word} (${names}): ${firstErr}`, {
        // eslint-disable-next-line @typescript-eslint/no-non-null-assertion -- guarded by failed.length > 0
        status: failed[0]!.status,
        ...(allNetwork ? { code: firstErr === "Request timed out" ? "timeout" : "network" } : {}),
      });
    }
  },
  optimistic: (args) => {
    for (const row of [...args.listEl.children]) {
      if (!(row instanceof HTMLDivElement)) {
        continue;
      }
      if (args.names.includes(row.dataset["name"] ?? "")) {
        row.classList.add("fb-row-exiting");
      }
    }
    return undefined;
  },
  rollback: (args) => {
    // NOTE: This rollback is a no-op if loadDir() has already replaced
    // the list's children (the exiting rows no longer exist in the DOM).
    // That's fine — the fresh listing from the server is the source of truth.
    for (const row of [...args.listEl.children]) {
      if (!(row instanceof HTMLDivElement)) {
        continue;
      }
      row.classList.remove("fb-row-exiting");
    }
  },
  error: (_args, err) => err.message,
});

// --- files.download ---

// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for action with no args/result
export const downloadFiles = defineAction<{ paths: string[] }, void>({
  name: "files.download",
  retryable: retryNetwork,
  run: async (args, signal) => {
    let r: Response;
    try {
      r = await fetch(API_FILES_DOWNLOAD, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ paths: args.paths }),
        signal: withTimeout(signal, DOWNLOAD_TIMEOUT_MS),
      });
    } catch (e) {
      throw classifyFetchError(e, signal);
    }
    if (!r.ok) {
      throw new ActionError("Download failed", { status: r.status });
    }
    const blob = await r.blob();
    if (signal.aborted) {
      return;
    }
    // Trigger browser download via objectURL anchor
    const url = URL.createObjectURL(blob);
    try {
      // eslint-disable-next-line @typescript-eslint/no-unnecessary-condition -- defensive check after async
      if (signal.aborted) {
        return;
      }
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

interface UploadArgs {
  files: FileList;
  targetDir: string;
}

/** The paths a failed upload batch DID write, carried on the rejection's
 *  `cause`. A partially-failed batch is not rolled back, so the caller can
 *  still attach what landed. Read it with partialUploadOf rather than by
 *  casting: the cause of a rejection is `unknown` by contract. */
interface PartialUpload {
  uploaded: string[];
}

/** Recover the partial batch from a rejected upload's error. Returns [] for
 *  every other failure shape, so callers need no branch. */
export function partialUploadOf(cause: unknown): string[] {
  if (typeof cause !== "object" || cause === null || !("uploaded" in cause)) {
    return [];
  }
  const { uploaded } = cause;
  return Array.isArray(uploaded) ? uploaded.filter((p): p is string => typeof p === "string") : [];
}

export const upload = defineAction<UploadArgs, string[]>({
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
        onComplete: (paths) => {
          resolve(paths);
        },
        onError: (msg, uploaded) => {
          // cause carries the partial batch: the upload is not rolled back, so
          // a caller that attaches paths still wants the ones that landed.
          const partial: PartialUpload = { uploaded };
          reject(new ActionError(msg, { cause: partial }));
        },
      });
    });
  },
  success: (_args, paths) =>
    paths.length === 1 ? "Uploaded 1 file" : `Uploaded ${String(paths.length)} files`,
  error: "Upload failed",
});
