// ---------------------------------------------------------------------------
// The attachment pill — one component, two homes.
//
// The composer's staged row and a sent turn's header draw the SAME pill: same
// badge source (badgeForExt), same label, same click. It lives here rather than
// in attachments.ts because the second consumer is `fundamentals/turn-header.ts`,
// a pure view, and reaching into the composer module from there would drag
// `$.attachmentRow` — a dom.ts getter that THROWS on a missing element — into
// every test that renders a turn. It would also inherit composer semantics: the
// staged pill's `×` removes a file from the next prompt, and a sent turn's pill
// must not be removable. So removal is an opt-in the caller passes, and the
// composer is the only caller that passes it.
//
// STRUCTURE: the body button and the `×` are SIBLINGS inside the `<li>`, never
// nested. A `<button>` cannot contain a `<button>`, so making the body the
// outer element was never available; and with siblings, a click on `×` cannot
// reach the open handler at all, which is a structural guarantee rather than a
// stopPropagation call that a later edit can forget.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { badgeForExt, extOf } from "./file-extensions.js";

/** The two fields a pill draws.
 *
 *  Structurally satisfied by attachments.ts's `AttachedFile` (the staged list)
 *  and by the generated wire `Attachment` (the persisted list), which is what
 *  lets one view serve both without either side converting. */
export interface AttachmentRef {
  path: string;
  name: string;
}

/** Shown when the extension carries no badge of its own. */
const FALLBACK_BADGE = "📎";

/** Open handler, injected — the same pattern `initTurnHeaderCallbacks` uses for
 *  Copy, and for the same reason: opening a file reaches `editor-openers` and
 *  `tabs` behind it, and neither the composer's leaf nor a `fundamentals/` view
 *  may import that subgraph. Default is a no-op so a pill built in a test
 *  renders without wiring. */
let _open: (path: string) => void = () => {
  /* not wired */
};

export function initAttachmentPillCallbacks(cbs: { open: (path: string) => void }): void {
  _open = cbs.open;
}

/** Extension → emoji badge, derived from the file-extensions registry. */
export function iconForAttachment(name: string): string {
  return badgeForExt(extOf(name)) || FALLBACK_BADGE;
}

/** Build one attachment pill. Pass `onRemove` to get the `×`; omit it for a
 *  read-only pill (a sent turn's header). */
export function buildAttachmentPill(
  att: AttachmentRef,
  opts: { onRemove?: (path: string) => void } = {},
): HTMLElement {
  const open = el(
    "button",
    {
      type: "button",
      className: "attachment-open",
      "aria-label": `Open ${att.name}`,
    },
    el("span", { className: "attachment-icon" }, iconForAttachment(att.name)),
    el("span", { className: "attachment-name" }, att.name),
  );
  open.addEventListener("click", () => {
    _open(att.path);
  });

  const pill = el("li", { className: "attachment-pill", title: att.path }, open);

  const onRemove = opts.onRemove;
  if (onRemove !== undefined) {
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
      onRemove(att.path);
    });
    pill.appendChild(close);
  }
  return pill;
}
