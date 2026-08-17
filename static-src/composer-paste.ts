// ---------------------------------------------------------------------------
// Paste into the composer.
//
// One listener, three outcomes, and everything it handles ends up as an
// ordinary attachment in the uploads folder:
//
//   An image. Every desktop screenshot tool already puts a PNG on the
//   clipboard (Win+Shift+S, Cmd+Ctrl+Shift+4, the GNOME and KDE tools), so
//   this is the whole "grab what I am looking at" feature: no getDisplayMedia
//   permission prompt, no window picker, no crop overlay to build, and it works
//   on the same desktops getDisplayMedia does (iOS Safari supports neither, so
//   nothing is lost there either). The user already cropped, with the tool they
//   know. Images are downscaled and renamed before upload.
//
//   Any other file. A PDF or a spreadsheet copied in a file manager arrives the
//   same way and is uploaded as-is. It carries a real filename the user chose,
//   so it is NOT renamed.
//
//   A large text paste. Over the spill threshold, the text is written as a .txt
//   in the uploads folder and attached instead of entering the textarea, so a
//   pasted log or stack trace does not bury the prompt it belongs to. Under the
//   threshold nothing is intercepted and the browser inserts the text, which is
//   the common case and stays byte-identical to having no listener at all.
//
// Two things happen to an image before upload:
//
//   Downscale over a long-edge cap. A 5000x5000 flat-colour PNG is small in
//   bytes and expensive in tokens, because a vision model tiles by pixels — so
//   the byte budget the server enforces is not the whole cost. Done here rather
//   than in Go so it costs no server CPU and no image-scaling dependency.
//
//   Rename. Clipboard images arrive as "image.png" or with no name at all, and
//   the server's write renames over an existing destination without error, so
//   two pastes would collide and the second would destroy the first.
// ---------------------------------------------------------------------------

/** Long-edge ceiling in pixels. Above this a screenshot costs tokens without
 *  telling the model anything more; a full 4K frame scaled to 2000px is still
 *  legible for UI review, which is what these pastes are for. */
const MAX_EDGE = 2000;

/** Lines above which a text paste becomes a file instead of textarea content.
 *  Deliberately high: the composer is where a user pastes an error message or a
 *  config block and expects to see it, so the threshold marks the point where
 *  the paste has stopped being part of the sentence. */
const SPILL_LINES = 50;

/** Characters above which a text paste becomes a file. The second half of the
 *  same test, because 40 very long lines are as unreadable as 400 short ones. */
const SPILL_CHARS = 10_000;

/** MIME types worth preserving through a resize. Everything else re-encodes to
 *  PNG, which is lossless and always accepted; matches the extension set the
 *  server inlines as an image block (internal/command/prompt_attachments.go). */
const KEEP_TYPES = new Set(["image/png", "image/jpeg", "image/webp"]);

/** Extension for a re-encoded blob, so the uploaded name matches its bytes and
 *  the server's extension-keyed MIME lookup agrees with the content. */
function extFor(mime: string): string {
  switch (mime) {
    case "image/jpeg":
      return ".jpg";
    case "image/webp":
      return ".webp";
    default:
      return ".png";
  }
}

/** The shared timestamp for every name this module mints:
 *  `2026-08-15T08-42-11` rather than an epoch so a workspace listing reads in
 *  order and a user can tell which paste is which, and colon-free because a
 *  colon is illegal in a filename on some platforms.
 *
 *  One formatter for images and text so the two conventions cannot drift apart.
 *  Resolution is one second, which is also the collision window: two pastes
 *  inside the same second land on the same name and the server's atomic write
 *  replaces the first. Accepted, because a second paste that fast is a
 *  double-fire rather than two intended files. */
function stamp(now: Date): string {
  return now.toISOString().slice(0, 19).replace(/[:]/g, "-");
}

/** A unique, sortable, human-readable name for a pasted image. */
export function pastedImageName(mime: string, now: Date = new Date()): string {
  return `pasted-${stamp(now)}${extFor(mime)}`;
}

/** A unique, sortable name for a spilled text paste. Always .txt, which the
 *  server routes through its path-reference branch, so the agent reads the file
 *  with its file tools rather than receiving an opaque base64 blob. */
export function pastedTextName(now: Date = new Date()): string {
  return `paste-${stamp(now)}.txt`;
}

/** Clipboard files split by whether they are images, in order. Both lists are
 *  empty for a text paste, which is the common case and must fall through
 *  untouched. Reads dt.files only: a clipboard string entry is not a file and is
 *  handled by the text branch. */
export function clipboardFiles(dt: DataTransfer | null): { images: File[]; others: File[] } {
  if (dt === null) {
    return { images: [], others: [] };
  }
  const images: File[] = [];
  const others: File[] = [];
  for (const f of Array.from(dt.files)) {
    if (f.type.startsWith("image/")) {
      images.push(f);
    } else {
      others.push(f);
    }
  }
  return { images, others };
}

/**
 * Whether a text paste becomes a file rather than textarea content.
 *
 * The newline requirement is not a size heuristic, it is a correctness one: a
 * long single-line paste is a URL, a JWT or an API key, and those belong in the
 * box where the user can see, edit and truncate them. Spilling one to a file
 * would hide the thing they were about to check.
 *
 * Both limits are exclusive, so a paste of exactly SPILL_LINES lines or exactly
 * SPILL_CHARS characters still goes inline.
 */
export function shouldSpillPaste(text: string): boolean {
  if (!text.includes("\n")) {
    return false;
  }
  return text.length > SPILL_CHARS || text.split("\n").length > SPILL_LINES;
}

/** Downscale to MAX_EDGE on the long edge, preserving aspect ratio. Returns the
 *  input untouched when it is already small enough, and on ANY failure — a
 *  browser without OffscreenCanvas, a decode error, an exotic format — because
 *  losing the paste is worse than sending a large image the server will still
 *  cap by bytes. */
export async function downscaleImage(file: File): Promise<File> {
  try {
    if (typeof createImageBitmap !== "function" || typeof OffscreenCanvas !== "function") {
      return file;
    }
    const bmp = await createImageBitmap(file);
    const longEdge = Math.max(bmp.width, bmp.height);
    if (longEdge <= MAX_EDGE) {
      bmp.close();
      return file;
    }
    const scale = MAX_EDGE / longEdge;
    const w = Math.max(1, Math.round(bmp.width * scale));
    const h = Math.max(1, Math.round(bmp.height * scale));
    const canvas = new OffscreenCanvas(w, h);
    const ctx = canvas.getContext("2d");
    if (ctx === null) {
      bmp.close();
      return file;
    }
    ctx.drawImage(bmp, 0, 0, w, h);
    bmp.close();
    const type = KEEP_TYPES.has(file.type) ? file.type : "image/png";
    const blob = await canvas.convertToBlob({ type });
    return new File([blob], pastedImageName(type), { type });
  } catch (err: unknown) {
    console.warn("paste: downscale failed, sending the original", err);
    return file;
  }
}

/** Prepare clipboard images for upload: downscale, then name. */
export async function prepareForUpload(files: File[]): Promise<File[]> {
  const out: File[] = [];
  for (const f of files) {
    const scaled = await downscaleImage(f);
    // downscaleImage renames only when it re-encoded; an untouched file still
    // carries the clipboard's colliding "image.png".
    out.push(scaled === f ? new File([f], pastedImageName(f.type), { type: f.type }) : scaled);
  }
  return out;
}

/** Build the FileList the upload path takes. */
function toFileList(files: File[]): FileList {
  const dt = new DataTransfer();
  for (const f of files) {
    dt.items.add(f);
  }
  return dt.files;
}

/** Wire the paste handler. `onFiles` receives a FileList of everything the
 *  paste produced, which is what the upload action takes. Exactly one listener
 *  belongs on the composer: two would both fire and both preventDefault. */
export function installComposerPaste(
  target: HTMLElement,
  onFiles: (files: FileList) => void,
): void {
  target.addEventListener("paste", (event: ClipboardEvent) => {
    const { images, others } = clipboardFiles(event.clipboardData);
    if (images.length > 0 || others.length > 0) {
      event.preventDefault();
      void (async () => {
        // Only the images are renamed: the others carry a filename the user
        // chose, and replacing it would make the attachment unrecognisable.
        const prepared = await prepareForUpload(images);
        onFiles(toFileList([...prepared, ...others]));
      })();
      return;
    }
    const text = event.clipboardData?.getData("text/plain") ?? "";
    if (!shouldSpillPaste(text)) {
      return; // a normal paste; leave it to the browser
    }
    event.preventDefault();
    const name = pastedTextName();
    onFiles(toFileList([new File([text], name, { type: "text/plain" })]));
  });
}
