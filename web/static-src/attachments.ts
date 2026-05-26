// ---------------------------------------------------------------------------
// Attachment pill row: files attached to the next prompt.
//
// Instead of injecting file paths into the textarea, attachments appear
// as removable pills in a row below the input. On submit, the list is
// sent alongside the prompt text. The server classifies each by
// extension: document types (PDF, DOCX, etc.) become ACP document
// content blocks; everything else becomes a path reference the agent
// reads via fs_read.
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";
import { badgeForExt } from "./file-extensions.js";
import { reconcile } from "./reconcile.js";

/** One attached file. */
export interface AttachedFile {
  path: string; // workspace-relative
  name: string; // filename.ext (display)
}

const attached: AttachedFile[] = [];

// Extension → icon derived from the file-extensions registry.
function iconFor(name: string): string {
  const dot = name.lastIndexOf(".");
  if (dot === -1) return "📎";
  const ext = name.slice(dot + 1).toLowerCase();
  return badgeForExt(ext) || "📎";
}

/** Add a file to the attachment list. */
export function addAttachment(path: string): void {
  // Deduplicate by path.
  if (attached.some((a) => a.path === path)) return;
  const parts = path.split("/");
  const name = parts[parts.length - 1] ?? path;
  attached.push({ path, name });
  renderPills();
}

/** Remove an attachment by path. */
function removeAttachment(path: string): void {
  const idx = attached.findIndex((a) => a.path === path);
  if (idx === -1) return;
  attached.splice(idx, 1);
  renderPills();
}

/** Take all attachments (clears the list). Returns the array for the
 *  prompt payload. */
export function takeAttachments(): AttachedFile[] {
  const out = [...attached];
  attached.length = 0;
  renderPills();
  return out;
}

/** Clear all attachments without returning them. */
export function clearAttachments(): void {
  attached.length = 0;
  renderPills();
}

function renderPills(): void {
  const row = $.attachmentRow;
  if (attached.length === 0) {
    row.replaceChildren();
    row.classList.add("hidden");
    return;
  }
  row.classList.remove("hidden");
  reconcile(row, attached, {
    key: (a: AttachedFile) => a.path,
    mount: buildAttachmentPill,
  });
}

function buildAttachmentPill(att: AttachedFile): HTMLElement {
  const li = document.createElement("li");
  li.className = "attachment-pill";
  li.title = att.path;

  const icon = document.createElement("span");
  icon.className = "attachment-icon";
  icon.textContent = iconFor(att.name);

  const label = document.createElement("span");
  label.className = "attachment-name";
  label.textContent = att.name;

  const close = document.createElement("button");
  close.type = "button";
  close.className = "attachment-close";
  close.textContent = "×";
  close.setAttribute("aria-label", `Remove ${att.name}`);
  close.addEventListener("click", () => removeAttachment(att.path));

  li.append(icon, label, close);
  return li;
}
