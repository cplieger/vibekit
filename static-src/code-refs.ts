// ---------------------------------------------------------------------------
// Licensed-code attribution footnote (v3 _kiro/code_references).
//
// KAS flags a completion that reproduces a recognizable chunk of a referenced
// open-source file (when the account's code-reference tracker is enabled). The
// wire carries {license_name, repository, url} per reference — no content span,
// so an attribution can't map to a specific message region; it annotates the
// whole assistant turn. We render a compact, collapsible footnote at the bottom
// of the turn: a scale glyph + count summary that expands to license + source
// link per reference.
//
// syncCodeReferences is idempotent and cheap: it's called on every assistant
// paint (once per streaming chunk), so it no-ops unless the reference count
// changed. Driven off Message.code_references (persisted), so it renders the
// same on live SSE and on reload.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import type { Message, CodeReference } from "./types.js";
import { isSafeURL } from "./url-safety.js";
import { iconEl } from "./icon-el.js";
import { ICON_SCALE_12, ICON_EXTERNAL } from "./icons.js";
import { featureDisabled } from "./governance.js";

const CLS = "code-refs";

/** Ensure `wrap`'s licensed-code footnote matches `m.code_references`.
 *  Appends the footnote when references exist, removes it when they don't,
 *  and rebuilds only when the count changed (so repeated streaming paints
 *  are a no-op). Preserves the open/closed state across rebuilds. */
export function syncCodeReferences(wrap: HTMLElement, m: Message): void {
  const existing = wrap.querySelector<HTMLDetailsElement>(`:scope > .${CLS}`);
  // Gate on the org/account policy: when governance is KNOWN and the
  // code-reference tracker is off, never surface the attribution chip — the
  // feature is disabled server-side, so any hint would imply a capability the
  // account doesn't have. (KAS won't emit references then, but this keeps the
  // UI honest even against a stray persisted one.)
  const refs = featureDisabled("code_reference_tracker") ? [] : (m.code_references ?? []);
  if (refs.length === 0) {
    existing?.remove();
    return;
  }
  // Count is a safe signature: the server sends a monotonically-growing
  // deduped list, so equal length means equal content.
  if (existing !== null && existing.dataset["count"] === String(refs.length)) {
    return;
  }
  const wasOpen = existing?.open ?? false;
  const built = buildCodeRefs(refs, wasOpen);
  if (existing === null) {
    wrap.appendChild(built);
  } else {
    existing.replaceWith(built);
  }
}

/** Build the `<details>` footnote for a non-empty reference list. */
function buildCodeRefs(refs: readonly CodeReference[], open: boolean): HTMLDetailsElement {
  const count = refs.length;
  const details = el("details", {
    className: CLS,
    "data-count": String(count),
    title:
      "This turn reproduced code recognized as referenced open-source. " +
      "Review the license before reusing it.",
  }) as HTMLDetailsElement;
  details.open = open;

  const summaryText = count === 1 ? "1 code reference" : `${String(count)} code references`;
  const summary = el(
    "summary",
    { className: "code-refs-summary" },
    iconEl(ICON_SCALE_12),
    el("span", { className: "code-refs-count" }, summaryText),
  );
  details.appendChild(summary);

  const list = el("ul", { className: "code-refs-list" });
  for (const ref of refs) {
    list.appendChild(buildItem(ref));
  }
  details.appendChild(list);
  return details;
}

/** Build one `<li>` for a reference: license name + a link to the source
 *  (only when the URL is http/https-safe, per url-safety.ts isSafeURL),
 *  else the plain source label. */
function buildItem(ref: CodeReference): HTMLLIElement {
  const item = el("li", { className: "code-refs-item" }) as HTMLLIElement;
  item.appendChild(el("span", { className: "code-refs-license" }, ref.license_name));

  const url = ref.url ?? "";
  const safe = url !== "" && isSafeURL(url);
  const label = sourceLabel(ref, safe ? url : "");
  if (safe) {
    item.appendChild(
      el(
        "a",
        {
          className: "code-refs-link",
          href: url,
          target: "_blank",
          rel: "noopener noreferrer",
        },
        label,
        iconEl(ICON_EXTERNAL),
      ),
    );
  } else {
    item.appendChild(el("span", { className: "code-refs-source" }, label));
  }
  return item;
}

/** Human label for a reference's source: the repository if present, else the
 *  host of the (already-validated) safe URL, else "source". Never returns the
 *  raw URL. safeURL is empty unless the caller confirmed it via isSafeURL, so
 *  the URL parse here can't throw. */
function sourceLabel(ref: CodeReference, safeURL: string): string {
  const repo = (ref.repository ?? "").trim();
  if (repo !== "") {
    return repo;
  }
  if (safeURL !== "") {
    return new URL(safeURL).host || "source";
  }
  return "source";
}
