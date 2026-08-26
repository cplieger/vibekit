// Canonical scroll.js mock for test files. Single source of truth for all
// scroll exports — add new exports here when scroll.ts gains them.
import { vi } from "vitest";
// Type-only, so this does NOT pull the module being mocked in at runtime. It is
// what lets a suite drive the reading state (`readingState.mockReturnValue`) with
// the other member of the union: inferred from the default alone, the mock's
// return type would be the literal "following" and "reading" would not typecheck.
import type { ReadingState } from "../scroll.js";

export const scrollMock = {
  getScrollEl: vi.fn(() => document.createElement("div")),
  scrollToBottom: vi.fn(),
  setUserScrolledUp: vi.fn(),
  jumpTo: vi.fn(),
  resetScrollState: vi.fn(),
  setLoadMore: vi.fn(),
  readingState: vi.fn((): ReadingState => "following"),
  onReadingStateChange: vi.fn(),
  setAnchorProvider: vi.fn(),
  setResumeLabel: vi.fn(),
  // The compensation helpers run their mutation, so a mocked scroll module does
  // not silently skip the DOM change the caller was making.
  preserveReadingPosition: vi.fn((mutate: () => void) => {
    mutate();
  }),
  deferWhileReading: vi.fn((mutate: () => void) => {
    mutate();
  }),
  fillViewport: vi.fn(),
  // How much room the transcript has left to scroll. Defaults to a comfortably
  // navigable value rather than 0, because the turn rail hides itself below
  // MIN_SCROLL_PX — a 0 here would silently withdraw the rail from every suite
  // that renders one and assert nothing about why.
  scrollableBy: vi.fn(() => 500),
};
