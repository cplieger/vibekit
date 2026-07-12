// ---------------------------------------------------------------------------
// Attachment pill row: files attached to the next prompt.
//
// Instead of injecting file paths into the textarea, attachments appear
// as removable pills in a row below the input. On submit, the list is
// sent alongside the prompt text. The server classifies each by
// extension: supported document types (PDF, DOCX, XLSX, DOC, XLS, CSV)
// are inlined as ACP embedded `resource` blocks; everything else becomes
// a path reference the agent reads via fs_read.
// ---------------------------------------------------------------------------

import { el, createCollection, bindList, effect } from "@cplieger/reactive";
import { $ } from "./dom.js";
import { badgeForExt } from "./file-extensions.js";

/** One attached file. */
export interface AttachedFile {
  path: string; // workspace-relative
  name: string; // filename.ext (display)
}

// Ordered keyed collection of attachments (keyed by workspace path). The
// pill row is rendered via bindList (per-pill reactivity) and a hidden-toggle
// effect — no manual renderPills() calls.
const attached = createCollection<AttachedFile>((a) => a.path);

let bound = false;
function ensureBound(): void {
  if (bound) {
    return;
  }
  bound = true;
  const row = $.attachmentRow;
  bindList(row, attached, { mount: (att) => buildAttachmentPill(att) });
  effect(() => {
    row.classList.toggle("hidden", attached.ids.value.length === 0);
  });
}

// Extension → icon derived from the file-extensions registry.
function iconFor(name: string): string {
  const dot = name.lastIndexOf(".");
  if (dot === -1) {
    return "📎";
  }
  const ext = name.slice(dot + 1).toLowerCase();
  return badgeForExt(ext) || "📎";
}

/** Add a file to the attachment list. */
export function addAttachment(path: string): void {
  // Deduplicate by path.
  if (attached.has(path)) {
    return;
  }
  const parts = path.split("/");
  const name = parts[parts.length - 1] ?? path;
  ensureBound();
  attached.upsert({ path, name });
}

/** Remove an attachment by path. */
function removeAttachment(path: string): void {
  attached.remove(path);
}

/** Take all attachments (clears the list). Returns the array for the
 *  prompt payload. */
export function takeAttachments(): AttachedFile[] {
  const out = attached.items();
  attached.clear();
  return out;
}

/** Clear all attachments without returning them. */
export function clearAttachments(): void {
  attached.clear();
}

function buildAttachmentPill(att: AttachedFile): HTMLElement {
  const icon = el("span", { className: "attachment-icon" }, iconFor(att.name));

  const label = el("span", { className: "attachment-name" }, att.name);

  const close = el(
    "button",
    {
      type: "button",
      className: "attachment-close",
      "aria-label": `Remove ${att.name}`,
    },
    "×",
  );
  close.addEventListener("click", () => {
    removeAttachment(att.path);
  });

  return el("li", { className: "attachment-pill", title: att.path }, icon, label, close);
}
