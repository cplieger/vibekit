// ---------------------------------------------------------------------------
// Universal file upload with progress indicator.
//
// Uses XMLHttpRequest (not fetch) because fetch doesn't expose upload
// progress. This is one of two places in the app that legitimately
// doesn't go through api-client.ts; the other is transport.ts's SSE.
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";
import { hasErrorString } from "./actions/index.js";

export interface UploadOptions {
  files: FileList;
  targetDir: string;
  /** Called on successful upload. Receives the resolved workspace paths
   *  (targetDir + filename) for each uploaded file. */
  onComplete?: (paths: string[]) => void;
  /** Called on failure. `uploaded` carries the paths that DID land before the
   *  batch stopped, because a partially-failed batch is not rolled back: the
   *  server reports what it wrote (see respondUploadError in
   *  internal/filebrowse/upload.go) and the caller can still use
   *  those files. Empty on a transport failure, where nothing is known. */
  onError?: (msg: string, uploaded: string[]) => void;
  /** Optional signal for programmatic cancellation (e.g. chat delete, navigation). */
  signal?: AbortSignal;
}

// The three functions below are this module's pure core: response shaping with
// no DOM and no XHR. They are exported so they can be tested directly, the same
// split strings.ts uses for windowOutput; the surrounding uploadFiles is the
// untestable shell (a singleton progress bar plus a live request).

/** Server-returned filenames mapped onto workspace paths under the target
 *  directory. Shared by the success and failure paths so a partial batch's
 *  paths are spelled exactly like a whole one's. */
export function resolvePaths(targetDir: string, names: string[]): string[] {
  const sep = targetDir === "" || targetDir === "." ? "" : `${targetDir.replace(/\/+$/, "")}/`;
  return names.map((name) => sep + name);
}

/** The `uploaded` names out of a response body, empty when the body carries
 *  none (an older server, a proxy error page, an unparseable body). */
export function uploadedNames(responseText: string): string[] {
  try {
    const body = JSON.parse(responseText) as { uploaded?: unknown };
    if (Array.isArray(body.uploaded)) {
      return body.uploaded.filter((n: unknown): n is string => typeof n === "string");
    }
  } catch {
    /* not JSON; no names to recover */
  }
  return [];
}

/** The failure sentence for a batch that got part of the way through.
 *  The server's message says WHY but deliberately names no file (its 413 and
 *  500 bodies are generic sentinels), and the client knows the order it sent,
 *  so the first file after the last success is the one that failed. */
export function batchFailureMessage(
  files: FileList,
  uploadedCount: number,
  serverMsg: string,
): string {
  if (uploadedCount === 0 || uploadedCount >= files.length) {
    return serverMsg;
  }
  const failed = files[uploadedCount];
  const which = failed === undefined ? "the next file" : failed.name;
  return `${String(uploadedCount)} of ${String(files.length)} uploaded, then ${which} failed: ${serverMsg}`;
}

// NOTE: Only one upload at a time is supported. The progress bar is a
// singleton DOM element shared across browser upload and chat drop.
// Concurrent uploads would corrupt the progress display. Serialization
// is enforced by scope: "upload" on the action definition, which queues
// subsequent dispatches until the current upload completes.

export function uploadFiles(opts: UploadOptions): void {
  // If already cancelled, bail out before showing any progress UI.
  if (opts.signal?.aborted) {
    opts.onError?.("Upload cancelled", []);
    return;
  }

  const form = new FormData();
  form.append("dir", opts.targetDir);
  for (const f of opts.files) {
    form.append("files", f);
  }

  const progress = $.uploadProgress;
  const fill = $.uploadProgressFill;
  const label = $.uploadProgressLabel;
  const cancelBtn = $.uploadProgressCancel;

  progress.classList.remove("upload-closed");
  progress.setAttribute("role", "progressbar");
  progress.setAttribute("aria-valuemin", "0");
  progress.setAttribute("aria-valuemax", "100");
  progress.setAttribute("aria-valuenow", "0");
  progress.setAttribute("aria-label", `Uploading ${String(opts.files.length)} file(s)`);
  fill.style.width = "0%";
  label.textContent = `Uploading ${String(opts.files.length)} file(s)...`;

  const xhr = new XMLHttpRequest();

  // Wire the user-visible cancel button to xhr.abort(). We still expose
  // opts.signal for programmatic abort (e.g. chat delete, navigation);
  // both paths converge on xhr.abort() below.
  cancelBtn.classList.remove("hidden");
  const onCancelClick = (): void => {
    xhr.abort();
  };
  cancelBtn.addEventListener("click", onCancelClick, { once: true });
  const onSignalAbort = (): void => {
    xhr.abort();
  };
  const teardownCancelUI = (): void => {
    cancelBtn.classList.add("hidden");
    cancelBtn.removeEventListener("click", onCancelClick);
    opts.signal?.removeEventListener("abort", onSignalAbort);
  };

  xhr.open("POST", "/api/file/upload");
  xhr.timeout = 300_000; // 5 minutes
  xhr.upload.addEventListener("progress", (e: ProgressEvent) => {
    if (e.lengthComputable) {
      const pct = Math.round((e.loaded / e.total) * 100);
      fill.style.width = `${String(pct)}%`;
      label.textContent = `Uploading... ${String(pct)}%`;
      progress.setAttribute("aria-valuenow", String(pct));
    } else {
      // Total size unknown: mark the progressbar indeterminate. Per ARIA, an
      // indeterminate progressbar omits aria-valuenow rather than reporting 0.
      progress.removeAttribute("aria-valuenow");
      label.textContent = "Uploading...";
    }
  });
  xhr.addEventListener("load", () => {
    teardownCancelUI();
    if (xhr.status >= 200 && xhr.status < 300) {
      fill.style.width = "100%";
      label.textContent = "Upload complete";
      progress.setAttribute("aria-valuenow", "100");
      setTimeout(() => {
        progress.classList.add("upload-closed");
      }, 1500);
      // Use server-returned filenames (sanitized via filepath.Base) when
      // available; fall back to client names if the server returns no array.
      const names = uploadedNames(xhr.responseText);
      const paths =
        names.length > 0
          ? resolvePaths(opts.targetDir, names)
          : resolvePaths(
              opts.targetDir,
              Array.from(opts.files, (f) => f.name),
            );
      opts.onComplete?.(paths);
    } else {
      let serverMsg = `Upload failed (${String(xhr.status)})`;
      try {
        const body: unknown = JSON.parse(xhr.responseText);
        if (hasErrorString(body)) {
          serverMsg = body.error;
        }
      } catch {
        /* ignore */
      }
      const landed = resolvePaths(opts.targetDir, uploadedNames(xhr.responseText));
      const msg = batchFailureMessage(opts.files, landed.length, serverMsg);
      label.textContent = msg;
      setTimeout(() => {
        progress.classList.add("upload-closed");
      }, 2000);
      opts.onError?.(msg, landed);
    }
  });
  xhr.addEventListener("error", () => {
    teardownCancelUI();
    label.textContent = "Upload failed";
    setTimeout(() => {
      progress.classList.add("upload-closed");
    }, 2000);
    opts.onError?.("Upload failed", []);
  });
  xhr.addEventListener("timeout", () => {
    teardownCancelUI();
    label.textContent = "Upload timed out";
    setTimeout(() => {
      progress.classList.add("upload-closed");
    }, 2000);
    opts.onError?.("Upload timed out", []);
  });
  xhr.addEventListener("abort", () => {
    teardownCancelUI();
    label.textContent = "Upload cancelled";
    setTimeout(() => {
      progress.classList.add("upload-closed");
    }, 1500);
    opts.onError?.("Upload cancelled", []);
  });
  if (opts.signal) {
    opts.signal.addEventListener("abort", onSignalAbort, { once: true });
  }
  xhr.send(form);
}
