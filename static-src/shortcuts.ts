// ---------------------------------------------------------------------------
// The keyboard shortcut reference sheet.
//
// Seven chords existed with no in-app reference of any kind: the only disclosure
// anywhere in the UI was two tooltips (`Search this chat (Ctrl+F)` and
// `Rename (F2)`), neither of which names a binding keys.ts registers. Seven
// shortcuts nobody can discover are seven shortcuts nobody uses.
//
// The generated half is GENERATED, from keys.ts's own registry, so a chord added
// there appears here with no second edit. Transcribing the table would have
// produced a list that silently rots, which is how the existing `description`
// field ended up with no reader at all.
//
// The authored half exists because three bindings live outside that registry and
// a sheet missing them is wrong for the reader: Escape (keys.ts, above the
// modifier gate), Ctrl+F (app.ts's capture-phase listener, which routes to
// find-in-chat.ts or files-search.ts by the active tab's kind, so it earns two
// rows), and F2 (files.ts). Each row names its owner in a comment so the pairing
// can be re-checked.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { byId } from "./dom.js";
import { openModal } from "./modals.js";
import { registeredShortcuts, type ShortcutBinding } from "./keys.js";

/** One line of the sheet: what it does, and every chord that does it. Reached
 *  through SheetGroup rather than named by any consumer, so not exported. */
interface SheetRow {
  readonly description: string;
  /** Each chord is its keys in press order, already labelled for display. */
  readonly chords: readonly (readonly string[])[];
}

export interface SheetGroup {
  readonly name: string;
  readonly rows: readonly SheetRow[];
}

/** The heading the bindings that are not in keys.ts's registry file under. */
const OTHER_GROUP = "Elsewhere";

/** Bindings no `register()` call owns, so they cannot be generated. */
const OTHER_ROWS: readonly SheetRow[] = [
  // keys.ts, handled above the Ctrl/Cmd gate.
  { description: "Close a dialog, or clear the file browser selection", chords: [["Esc"]] },
  // app.ts's capture-phase listener, dispatched by the ACTIVE TAB's kind: a
  // files or editor tab reaches files-search.ts, everything else
  // find-in-chat.ts. Two rows because it is genuinely two bindings on one chord;
  // a second press falls through to the browser's own find in both.
  { description: "Search this conversation", chords: [["Ctrl", "F"]] },
  { description: "Find in files, from a file browser or editor tab", chords: [["Ctrl", "F"]] },
  // files.ts, on a selected row in the file browser.
  { description: "Rename in the file browser", chords: [["F2"]] },
  // keys.ts, the binding that opens this sheet.
  { description: "Show this list", chords: [["?"]] },
];

/** Display label for a registered key. Single letters read as capitals because
 *  that is how a keycap is printed; the named and punctuation keys are already
 *  what a reader sees on theirs. */
function keyLabel(key: string): string {
  return key.length === 1 ? key.toUpperCase() : key;
}

/** The keys of one registered chord, in press order. The modifier is labelled
 *  `Ctrl` alone rather than detected: the handler accepts Ctrl OR Cmd, and the
 *  sheet says so once in its own note instead of sniffing the platform, which
 *  gets iPadOS-on-MacIntel wrong. */
function chordOf(binding: ShortcutBinding): readonly string[] {
  const keys = ["Ctrl"];
  if (binding.shift) {
    keys.push("Shift");
  }
  keys.push(keyLabel(binding.key));
  return keys;
}

/** Fold the registry into grouped rows, merging the chords that do the same
 *  thing. Ctrl+K and Ctrl+N are both "New conversation", so they are one row
 *  with two chords rather than the same sentence printed twice. Group and row
 *  order are first-seen, i.e. registration order. */
function generatedGroups(bindings: readonly ShortcutBinding[]): readonly SheetGroup[] {
  const groups = new Map<string, Map<string, string[][]>>();
  for (const binding of bindings) {
    let rows = groups.get(binding.group);
    if (rows === undefined) {
      rows = new Map();
      groups.set(binding.group, rows);
    }
    const chords = rows.get(binding.description);
    const chord = [...chordOf(binding)];
    if (chords === undefined) {
      rows.set(binding.description, [chord]);
    } else {
      chords.push(chord);
    }
  }
  return [...groups].map(([name, rows]) => ({
    name,
    rows: [...rows].map(([description, chords]) => ({ description, chords })),
  }));
}

/** The whole sheet as data. Pure, so the content is testable with no DOM. */
export function sheetGroups(): readonly SheetGroup[] {
  return [...generatedGroups(registeredShortcuts()), { name: OTHER_GROUP, rows: OTHER_ROWS }];
}

function renderChord(keys: readonly string[]): HTMLElement {
  const span = el("span", { className: "shortcut-chord" });
  for (const key of keys) {
    span.append(el("kbd", null, key));
  }
  return span;
}

function renderRow(row: SheetRow): HTMLElement {
  const keys = el("span", { className: "shortcut-keys" });
  row.chords.forEach((chord, i) => {
    if (i > 0) {
      keys.append(el("span", { className: "shortcut-or" }, "or"));
    }
    keys.append(renderChord(chord));
  });
  return el(
    "div",
    { className: "shortcut-row" },
    el("span", { className: "shortcut-desc" }, row.description),
    keys,
  );
}

function renderGroup(group: SheetGroup): HTMLElement {
  const section = el(
    "section",
    { className: "shortcut-group" },
    el("h3", { className: "shortcut-group-title" }, group.name),
  );
  for (const row of group.rows) {
    section.append(renderRow(row));
  }
  return section;
}

/** Open the reference sheet, rebuilding its contents first.
 *
 *  Rebuilt on every open rather than once: the registry is populated by
 *  initKeyboardShortcuts, so a sheet built at module load would be empty, and a
 *  build-once-on-first-open would still be a second piece of lifecycle to get
 *  wrong for no saving on a list of this size. */
export function openShortcutsSheet(): void {
  const list = byId<HTMLDivElement>("shortcuts-list");
  list.replaceChildren(...sheetGroups().map(renderGroup));
  openModal(byId<HTMLDivElement>("shortcuts-modal"));
}
