// ---------------------------------------------------------------------------
// Universal file upload with progress indicator.
//
// Uses XMLHttpRequest (not fetch) because fetch doesn't expose upload
// progress. This is one of two places in the app that legitimately
// doesn't go through api-client.ts; the other is transport.ts's SSE.
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";

export interface UploadOptions {
  files: FileList;
  targetDir: string;
  /** Called on successful upload. Receives the resolved workspace paths
   *  (targetDir + filename) for each uploaded file. */
  onComplete?: (paths: string[]) => void;
  onError?: (msg: string) => void;
  /** Optional signal for programmatic cancellation (e.g. chat delete, navigation). */
  signal?: AbortSignal;
}

export function uploadFiles(opts: UploadOptions): void {
  // If already cancelled, bail out before showing any progress UI.
  if (opts.signal?.aborted) {
    opts.onError?.("Upload cancelled");
    return;
  }

  const form = new FormData();
  form.append("dir", opts.targetDir);
  for (const f of opts.files) form.append("files", f);

  const progress = $.uploadProgress;
  const fill = $.uploadProgressFill;
  const label = $.uploadProgressLabel;
  const cancelBtn = $.uploadProgressCancel;

  progress.classList.remove("upload-closed");
  fill.style.width = "0%";
  label.textContent = `Uploading ${String(opts.files.length)} file(s)...`;

  const xhr = new XMLHttpRequest();

  // Wire the user-visible cancel button to xhr.abort(). We still expose
  // opts.signal for programmatic abort (e.g. chat delete, navigation);
  // both paths converge on xhr.abort() below.
  cancelBtn.classList.remove("hidden");
  const onCancelClick = (): void => { xhr.abort(); };
  cancelBtn.addEventListener("click", onCancelClick, { once: true });
  const onSignalAbort = (): void => { xhr.abort(); };
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
    }
  });
  xhr.addEventListener("load", () => {
    teardownCancelUI();
    if (xhr.status >= 200 && xhr.status < 300) {
      fill.style.width = "100%";
      label.textContent = "Upload complete";
      setTimeout(() => { progress.classList.add("upload-closed"); }, 1500);
      const paths: string[] = [];
      const sep = opts.targetDir === "" || opts.targetDir === "." ? "" : `${opts.targetDir.replace(/\/+$/, "")}/`;
      for (const f of opts.files) paths.push(sep + f.name);
      opts.onComplete?.(paths);
    } else {
      let msg = `Upload failed (${String(xhr.status)})`;
      try {
        const body = JSON.parse(xhr.responseText) as { error?: string };
        if (typeof body.error === "string") msg = body.error;
      } catch { /* ignore */ }
      label.textContent = msg;
      setTimeout(() => { progress.classList.add("upload-closed"); }, 2000);
      opts.onError?.(msg);
    }
  });
  xhr.addEventListener("error", () => {
    teardownCancelUI();
    label.textContent = "Upload failed";
    setTimeout(() => { progress.classList.add("upload-closed"); }, 2000);
    opts.onError?.("Upload failed");
  });
  xhr.addEventListener("timeout", () => {
    teardownCancelUI();
    label.textContent = "Upload timed out";
    setTimeout(() => { progress.classList.add("upload-closed"); }, 2000);
    opts.onError?.("Upload timed out");
  });
  xhr.addEventListener("abort", () => {
    teardownCancelUI();
    label.textContent = "Upload cancelled";
    setTimeout(() => { progress.classList.add("upload-closed"); }, 1500);
    opts.onError?.("Upload cancelled");
  });
  if (opts.signal) {
    opts.signal.addEventListener("abort", onSignalAbort, { once: true });
  }
  xhr.send(form);
}
