// ---------------------------------------------------------------------------
// File browser state: pure state management for the file browser.
// No DOM dependencies — independently unit-testable.
// ---------------------------------------------------------------------------

import { FB_ROOT, type FileEntry } from "./files-shared.js";

export class FileBrowserState {
  currentPath: string;
  history: string[];
  historyIdx = 0;
  selected = new Set<string>();
  lastClickedName = "";
  entries: FileEntry[] = [];
  /** Whether the browse route has ANSWERED for this directory. `entries` initialises
   *  to `[]`, so an empty directory and one this client has never read are otherwise
   *  the same state, and the rows placeholder must arm only for the second. */
  answered = false;
  entryMap = new Map<string, FileEntry>();
  dirWritable = true;
  sortedNames: string[] = [];

  /** True until this browser's ORIGIN folder loads, so an unreachable one falls back
   *  to the mounts listing ONCE and a later failure keeps the error row. Per browser
   *  rather than per module: N browsers share one fetch holder, so a shared arm would
   *  be spent by another tab's first transient error. */
  pendingRestore = true;

  /** A browser opened at `at`, with both nav buttons DISABLED by construction. The
   *  alternative is `navigate`, which PUSHES, so a fresh tab would render with Back
   *  enabled and walk to a mounts listing it was never at. */
  constructor(at: string = FB_ROOT) {
    this.currentPath = at;
    this.history = [at];
  }

  navigate(path: string): void {
    this.currentPath = path;
    this.answered = false;
    this.selected.clear();
    this.lastClickedName = "";
    this.history.length = this.historyIdx + 1;
    this.history.push(path);
    this.historyIdx = this.history.length - 1;
  }

  goBack(): boolean {
    if (this.historyIdx <= 0) {
      return false;
    }
    this.historyIdx--;
    this.currentPath = this.history[this.historyIdx]!; // eslint-disable-line @typescript-eslint/no-non-null-assertion
    this.selected.clear();
    this.lastClickedName = "";
    return true;
  }

  goForward(): boolean {
    if (this.historyIdx >= this.history.length - 1) {
      return false;
    }
    this.historyIdx++;
    this.currentPath = this.history[this.historyIdx]!; // eslint-disable-line @typescript-eslint/no-non-null-assertion
    this.selected.clear();
    this.lastClickedName = "";
    return true;
  }

  /** Point this browser at a directory named from OUTSIDE its own trail: a document
   *  history entry, or a pasted deep link.
   *
   *  Adjacent-first, because while the browser is active the two trails are one: a
   *  document Back steps `historyIdx` back rather than pushing, so repeated presses
   *  cannot grow `history` without bound. Anything else pushes. */
  pointTo(dir: string): void {
    if (dir === this.currentPath) {
      return;
    }
    if (this.history[this.historyIdx - 1] === dir) {
      this.goBack();
      return;
    }
    if (this.history[this.historyIdx + 1] === dir) {
      this.goForward();
      return;
    }
    this.navigate(dir);
  }

  /** Back to the mounts listing, NOT to the tab's origin: the one caller is the
   *  auto-heal, and a heal back to an unreachable origin would loop. */
  reset(): void {
    this.currentPath = FB_ROOT;
    this.history.length = 0;
    this.history.push(FB_ROOT);
    this.historyIdx = 0;
    this.selected.clear();
    this.lastClickedName = "";
    this.entries = [];
    this.answered = false;
    this.entryMap.clear();
    this.dirWritable = true;
    this.sortedNames = [];
  }

  selectEntry(name: string): void {
    this.selected.add(name);
    this.lastClickedName = name;
  }

  deselectEntry(name: string): void {
    this.selected.delete(name);
    this.lastClickedName = name;
  }

  deselectAll(): void {
    this.selected.clear();
  }
}
