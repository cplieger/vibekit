// Shared UI builder functions. Eliminates duplicated DOM construction
// patterns across permissions-ui.ts, mcp-panels.ts, and future modules.

import { el } from "@cplieger/reactive";
import { ICON_CLOSE } from "./icons.js";
import { iconEl } from "./icon-el.js";

// ---------------------------------------------------------------------------
// Chip — removable tag with optional badge and code styling.
// ---------------------------------------------------------------------------

export interface ChipOptions {
  label: string;
  /** Render label inside <code> (monospace). */
  code?: boolean;
  /** Optional leading badge (e.g. "allow"/"deny" indicator). */
  badge?: { text: string; className: string };
  /** CSS class for the chip container. Default: "chip". */
  chipClass?: string;
  /** Tooltip on the remove button. Default: "Remove". */
  removeTitle?: string;
  /** Called when the user clicks remove or presses Backspace/Delete. */
  onRemove: () => void;
}

/** Build a removable chip element. Keyboard-accessible (Backspace/Delete removes). */
export function buildChip(opts: ChipOptions): HTMLSpanElement {
  const chip = el("span", { className: opts.chipClass ?? "chip", tabindex: "0" });

  if (opts.badge !== undefined) {
    const badgeEl = el("span", { className: opts.badge.className }, opts.badge.text);
    chip.appendChild(badgeEl);
  }

  if (opts.code === true) {
    const code = el("code", { className: "chip-label" }, opts.label);
    chip.appendChild(code);
  } else {
    const labelEl = el("span", { className: "chip-label" }, opts.label);
    chip.appendChild(labelEl);
  }

  const removeBtn = el(
    "button",
    { className: "chip-remove", type: "button", title: opts.removeTitle ?? "Remove" },
    iconEl(ICON_CLOSE),
  );
  removeBtn.addEventListener("click", () => {
    opts.onRemove();
  });
  chip.appendChild(removeBtn);

  chip.addEventListener("keydown", (e: KeyboardEvent) => {
    if (e.key === "Backspace" || e.key === "Delete") {
      e.preventDefault();
      opts.onRemove();
    }
  });
  return chip;
}
