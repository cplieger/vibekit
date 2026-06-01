// ---------------------------------------------------------------------------
// File browser state: pure state management for the file browser.
// No DOM dependencies — independently unit-testable.
// ---------------------------------------------------------------------------

import type { FileEntry } from "./files-shared.js";

export class FileBrowserState {
  currentPath = ".";
  history: string[] = ["."];
  historyIdx = 0;
  selected = new Set<string>();
  lastClickedName = "";
  entries: FileEntry[] = [];
  entryMap = new Map<string, FileEntry>();
  dirWritable = true;
  sortedNames: string[] = [];

  navigate(path: string): void {
    this.currentPath = path;
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

  reset(): void {
    this.currentPath = ".";
    this.history.length = 0;
    this.history.push(".");
    this.historyIdx = 0;
    this.selected.clear();
    this.lastClickedName = "";
    this.entries = [];
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
