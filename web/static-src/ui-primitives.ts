// Shared UI builder functions. Eliminates duplicated DOM construction
// patterns across permissions-ui.ts, mcp-panels.ts, and future modules.

import { ICON_CLOSE, iconEl } from "./icons.js";

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
  const chip = document.createElement("span");
  chip.className = opts.chipClass ?? "chip";
  chip.setAttribute("tabindex", "0");

  if (opts.badge !== undefined) {
    const badgeEl = document.createElement("span");
    badgeEl.className = opts.badge.className;
    badgeEl.textContent = opts.badge.text;
    chip.appendChild(badgeEl);
  }

  if (opts.code === true) {
    const code = document.createElement("code");
    code.className = "chip-label";
    code.textContent = opts.label;
    chip.appendChild(code);
  } else {
    const labelEl = document.createElement("span");
    labelEl.className = "chip-label";
    labelEl.textContent = opts.label;
    chip.appendChild(labelEl);
  }

  const removeBtn = document.createElement("button");
  removeBtn.className = "chip-remove";
  removeBtn.type = "button";
  removeBtn.title = opts.removeTitle ?? "Remove";
  removeBtn.appendChild(iconEl(ICON_CLOSE));
  removeBtn.addEventListener("click", () => opts.onRemove());
  chip.appendChild(removeBtn);

  chip.addEventListener("keydown", (e: KeyboardEvent) => {
    if (e.key === "Backspace" || e.key === "Delete") { e.preventDefault(); opts.onRemove(); }
  });
  return chip;
}
