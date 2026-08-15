// ---------------------------------------------------------------------------
// Paste an image into the composer.
//
// Every desktop screenshot tool already puts a PNG on the clipboard
// (Win+Shift+S, Cmd+Ctrl+Shift+4, the GNOME and KDE tools), so this is the
// whole "grab what I am looking at" feature: no getDisplayMedia permission
// prompt, no window picker, no crop overlay to build, and it works on the same
// desktops getDisplayMedia does (iOS Safari supports neither, so nothing is
// lost there either). The user already cropped, with the tool they know.
//
// Two things happen before upload:
//
//   Downscale over a long-edge cap. A 5000x5000 flat-colour PNG is small in
//   bytes and expensive in tokens, because a vision model tiles by pixels — so
//   the byte budget the server enforces is not the whole cost. Done here rather
//   than in Go so it costs no server CPU and no image-scaling dependency.
//
//   Rename. Clipboard images arrive as "image.png" or with no name at all, and
//   uploads land in the workspace root, so two pastes would collide and the
//   second would overwrite the first.
// ---------------------------------------------------------------------------

/** Long-edge ceiling in pixels. Above this a screenshot costs tokens without
 *  telling the model anything more; a full 4K frame scaled to 2000px is still
 *  legible for UI review, which is what these pastes are for. */
const MAX_EDGE = 2000;

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

/** A unique, sortable, human-readable name for a pasted image.
 *  `2026-08-15T08-42-11` rather than an epoch so a workspace listing reads in
 *  order and a user can tell which paste is which. */
export function pastedImageName(mime: string, now: Date = new Date()): string {
  const stamp = now.toISOString().slice(0, 19).replace(/[:]/g, "-");
  return `pasted-${stamp}${extFor(mime)}`;
}

/** Image files on a clipboard event, in order. Empty when the paste is text,
 *  which is the common case and must fall through untouched. */
export function clipboardImages(dt: DataTransfer | null): File[] {
  if (dt === null) {
    return [];
  }
  return Array.from(dt.files).filter((f) => f.type.startsWith("image/"));
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

/** Wire the paste handler. `onFiles` receives a FileList built from the
 *  prepared files, which is what the upload action takes. */
export function installImagePaste(target: HTMLElement, onFiles: (files: FileList) => void): void {
  target.addEventListener("paste", (event: ClipboardEvent) => {
    const images = clipboardImages(event.clipboardData);
    if (images.length === 0) {
      return; // a text paste; leave it alone
    }
    event.preventDefault();
    void (async () => {
      const prepared = await prepareForUpload(images);
      const dt = new DataTransfer();
      for (const f of prepared) {
        dt.items.add(f);
      }
      onFiles(dt.files);
    })();
  });
}
