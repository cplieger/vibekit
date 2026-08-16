// ---------------------------------------------------------------------------
// Upload policy the composer applies before a byte leaves the browser.
//
// Two jobs, one module because they answer the same question with the same
// numbers: where an upload goes, and which files are worth sending. Both are
// consumed by the composer's drop and paste paths (files-drop.ts,
// composer-paste.ts) and by the limit hint the composer shows.
//
// The file browser and the upload picker are deliberately NOT consumers: they
// upload where the user is looking, which is the whole point of those surfaces.
// ---------------------------------------------------------------------------

/** Where a composer upload lands: one folder at the workspace root, modelled
 *  on an OS Downloads folder. The server creates it on the first upload.
 *
 *  Container-absolute, matching the paths the file browser and picker already
 *  produce, because this string is used twice with two different consumers: as
 *  the upload target (the server resolves it against its granted mounts) and,
 *  prefixed onto each filename, as the attachment path (the prompt builder
 *  resolves it against the workspace). A workspace-relative "uploads" would
 *  satisfy the second and be refused by the first.
 *
 *  Mirrors defaultUploadDir in internal/filehandler/filehandler.go;
 *  TestUploadPolicyMatchesClient pins the two together. */
export const UPLOADS_DIR = "/workspace/uploads";

/** The server's upload ceiling in bytes. Mirrors maxUploadSize in
 *  internal/filehandler/filehandler.go, which applies it to the WHOLE
 *  multipart request body (http.MaxBytesReader) and again to each file inside
 *  it, answering 413 either way.
 *
 *  Duplicated here rather than fetched, because it is a compile-time constant
 *  with no runtime input: an endpoint to report it would be a request per boot
 *  to learn a number that cannot change without a rebuild.
 *  TestUploadPolicyMatchesClient reads this literal and fails on drift, so the
 *  Go const stays the single definition.
 *
 *  It is NOT what the pre-flight enforces — see MAX_UPLOAD_TOTAL_BYTES. */
export const MAX_UPLOAD_BYTES = 50 * 1024 * 1024;

/** Bytes held back from MAX_UPLOAD_BYTES so the pre-flight's verdict survives
 *  the request that carries it.
 *
 *  The server's limit is on the whole multipart BODY, and multipart framing is
 *  part of that body: a boundary line, a Content-Disposition header carrying
 *  the filename and a Content-Type header per part, plus the `dir` field and
 *  the closing boundary. So a file of exactly MAX_UPLOAD_BYTES can never fit in
 *  a request of at most MAX_UPLOAD_BYTES, however small the overhead is.
 *
 *  1 MiB is far more than framing costs (roughly a kilobyte per part at the
 *  longest legal filename, times a 25-file cap). It is sized for the HINT
 *  rather than for the overhead: it leaves a round 49 MB that
 *  uploadLimitHint can state exactly, and an under-promise is the right
 *  direction for a limit a user reads before choosing a file. */
const MULTIPART_RESERVE_BYTES = 1024 * 1024;

/** What one composer upload may actually carry, all files together: the
 *  server's whole-request ceiling minus the framing reserve.
 *
 *  This is the number the pre-flight enforces, and it is a TOTAL, not a
 *  per-file allowance. Checking only per-file was the defect: two 30 MiB files
 *  each passed a 50 MiB per-file test and then failed together against the one
 *  limit the server actually applies, which is exactly the 413 the pre-flight
 *  exists to predict. A single file is capped at the same number, because one
 *  file is a batch of one and nothing larger can fit in the request either. */
export const MAX_UPLOAD_TOTAL_BYTES = MAX_UPLOAD_BYTES - MULTIPART_RESERVE_BYTES;

/** Files one composer gesture may carry. A dropped folder can hand over
 *  hundreds of entries, and the progress bar is a singleton, so a large batch
 *  is a long opaque wait ending in one attachment row per file. */
export const MAX_UPLOAD_FILES = 25;

/** Why a file was refused before upload. */
export interface RejectedFile {
  name: string;
  reason: string;
}

/** What the pre-flight decided. `accepted` preserves input order. */
export interface PreflightResult {
  accepted: File[];
  rejected: RejectedFile[];
}

/** Human-readable byte size for a limit message. Deliberately not
 *  files-shared.ts's formatSize: that one is a table cell's exact size
 *  ("52.4 MB"), and a limit reads better round. */
function limitLabel(bytes: number): string {
  return `${String(Math.round(bytes / (1024 * 1024)))} MB`;
}

/** The composer's one-line statement of the upload limit.
 *
 *  It names the TOTAL, because that is the limit that decides whether a drop
 *  succeeds. "Up to 50 MB per file" was true of no request the server accepts:
 *  a file at 50 MB always 413s, and two files well under it could too. */
export function uploadLimitHint(): string {
  return `Up to ${limitLabel(MAX_UPLOAD_TOTAL_BYTES)} per upload, all files together`;
}

/**
 * Decide which files to upload, before any bytes are sent.
 *
 * Worth doing because the composer's drop and paste paths upload without the
 * user consciously picking a file from a dialog: an over-cap file is a full
 * transfer that can only end in a 413, and a dropped folder is a batch nobody
 * chose. The attachment row cannot do this check — it holds paths and names,
 * so by the time a file is a pill its size is already gone.
 *
 * An empty file is accepted: zero bytes is a legal file and the server writes
 * it happily. Only the three limits reject.
 *
 * The size check runs twice on purpose. Once per file, so a single oversize
 * file is refused for being oversize; then against the RUNNING TOTAL, because
 * the server's ceiling is on the whole multipart request and a batch of
 * individually-legal files can still exceed it. The two produce different
 * sentences, which is the whole reason to keep both: "this file is too big" and
 * "this file does not fit in what is left" are different things to tell someone.
 */
export function preflightUploads(files: readonly File[]): PreflightResult {
  const accepted: File[] = [];
  const rejected: RejectedFile[] = [];
  let total = 0;
  for (const f of files) {
    if (f.size > MAX_UPLOAD_TOTAL_BYTES) {
      rejected.push({
        name: f.name,
        reason: `over the ${limitLabel(MAX_UPLOAD_TOTAL_BYTES)} limit`,
      });
      continue;
    }
    if (accepted.length >= MAX_UPLOAD_FILES) {
      rejected.push({ name: f.name, reason: `over ${String(MAX_UPLOAD_FILES)} files at once` });
      continue;
    }
    if (total + f.size > MAX_UPLOAD_TOTAL_BYTES) {
      rejected.push({
        name: f.name,
        reason: `over the ${limitLabel(MAX_UPLOAD_TOTAL_BYTES)} total for one upload`,
      });
      continue;
    }
    accepted.push(f);
    total += f.size;
  }
  return { accepted, rejected };
}

/** One sentence naming what was refused and why, for a toast. Returns "" when
 *  nothing was refused, so the caller can skip the toast on the common path. */
export function preflightMessage(rejected: readonly RejectedFile[]): string {
  if (rejected.length === 0) {
    return "";
  }
  const first = rejected[0];
  if (first === undefined) {
    return "";
  }
  if (rejected.length === 1) {
    return `Skipped ${first.name}: ${first.reason}`;
  }
  return `Skipped ${String(rejected.length)} files, starting with ${first.name}: ${first.reason}`;
}
