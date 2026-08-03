// Canonical scroll.js mock for test files. Single source of truth for all
// scroll exports — add new exports here when scroll.ts gains them.
import { vi } from "vitest";

export const scrollMock = {
  getScrollEl: vi.fn(() => document.createElement("div")),
  scrollEl: null as HTMLElement | null,
  scrollToBottom: vi.fn(),
  setUserScrolledUp: vi.fn(),
  resetScrollState: vi.fn(),
  setLoadMore: vi.fn(),
  readingState: vi.fn(() => "following" as const),
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
};
