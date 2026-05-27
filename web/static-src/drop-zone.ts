// ---------------------------------------------------------------------------
// Shared drag-drop zone helper. Handles the boilerplate of dragenter/
// dragleave counting, overlay show/hide, and dropEffect. Consumers
// provide the container, overlay element, and drop handler.
// ---------------------------------------------------------------------------

/** Options for installing a drop zone on a container element. */
export interface DropZoneOptions {
  /** The element that receives drag events. */
  container: HTMLElement;
  /** The overlay element to show/hide (must have a "hidden" class toggle). */
  overlay: HTMLElement;
  /** Called on dragover for custom logic (e.g. folder hover targeting).
   *  Return value is unused; the default dropEffect="copy" is always set. */
  onDragOver?: (e: DragEvent) => void;
  /** Called on dragleave when the drag fully exits the container. */
  onDragLeave?: () => void;
  /** Called when files are dropped. Receives the FileList. */
  onDrop: (files: FileList) => void;
}

/**
 * Install drag-drop event listeners on a container with overlay feedback.
 * Handles the dragenter/dragleave counter pattern so nested elements don't
 * cause flicker. The overlay is shown on first enter, hidden on full leave
 * or drop.
 *
 * A11y: announces drop-target activation via a lazily-created sr-only
 * aria-live region. The deprecated aria-dropeffect attribute (removed in
 * WAI-ARIA 1.2) is NOT used.
 */
export function installDropZone(opts: DropZoneOptions): void {
  let dragCounter = 0;
  let liveRegion: HTMLElement | null = null;

  function ensureLiveRegion(): HTMLElement {
    if (liveRegion !== null) {
      return liveRegion;
    }
    const el = document.createElement("div");
    el.className = "sr-only";
    el.setAttribute("aria-live", "assertive");
    el.setAttribute("aria-atomic", "true");
    document.body.appendChild(el);
    liveRegion = el;
    return el;
  }

  opts.container.addEventListener("dragenter", (e: DragEvent) => {
    e.preventDefault();
    dragCounter++;
    if (dragCounter === 1) {
      opts.overlay.classList.remove("hidden");
      ensureLiveRegion().textContent = "Drop target active, release to upload files";
    }
  });

  opts.container.addEventListener("dragleave", (e: DragEvent) => {
    e.preventDefault();
    dragCounter--;
    if (dragCounter <= 0) {
      dragCounter = 0;
      opts.overlay.classList.add("hidden");
      if (liveRegion !== null) {
        liveRegion.textContent = "";
      }
      opts.onDragLeave?.();
    }
  });

  opts.container.addEventListener("dragover", (e: DragEvent) => {
    e.preventDefault();
    if (e.dataTransfer !== null) {
      e.dataTransfer.dropEffect = "copy";
    }
    opts.onDragOver?.(e);
  });

  opts.container.addEventListener("drop", (e: DragEvent) => {
    e.preventDefault();
    dragCounter = 0;
    opts.overlay.classList.add("hidden");
    if (liveRegion !== null) {
      liveRegion.textContent = "";
    }
    if (e.dataTransfer !== null && e.dataTransfer.files.length > 0) {
      opts.onDrop(e.dataTransfer.files);
    }
    opts.onDragLeave?.();
  });
}
